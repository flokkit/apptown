#!/usr/bin/env python3
"""Check literal SQL write targets against table owners; inventory dynamic Execs.

Go's parser extracts literals (not comments). This intentionally is not a SQL
parser or an authorization boundary: only INSERT/UPDATE/DELETE are enforced.
Unknown dynamic Exec expressions require a reviewed declaration fingerprint.
"""
import importlib.util
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("shape_report", ROOT / "scripts/shape-report.py")
report = importlib.util.module_from_spec(spec)
spec.loader.exec_module(report)

assembly_spec = importlib.util.spec_from_file_location("schema_assembly", ROOT / "scripts/schema-assemble.py")
assembly = importlib.util.module_from_spec(assembly_spec)
assembly_spec.loader.exec_module(assembly)

IDENT = r'(?:"(?:[^"]|"")+"|[A-Za-z_][\w$]*)'
TABLE = IDENT + r'(?:\s*\.\s*' + IDENT + r')?'
TARGET = re.compile(r'\b(?:INSERT\s+INTO|DELETE\s+FROM)\s+(?:ONLY\s+)?(' + TABLE + r')(?=\s|\(|;|$)|\bUPDATE\s+(?:ONLY\s+)?(' + TABLE + r')(?=\s+(?:(?:AS\s+)?' + IDENT + r'\s+)?SET\b)', re.I)
# Discard comments and SQL values before matching DML. Preserve quoted identifiers.
TOKENS = re.compile(r"--[^\n]*|/\*[\s\S]*?\*/|'(?:[^']|'')*'|\$([A-Za-z_]\w*|)\$[\s\S]*?\$\1\$|\"(?:[^\"]|\"\")*\"")
SQL_START = re.compile(r'^\s*(?:WITH|SELECT|INSERT|UPDATE|DELETE|DO|CREATE|ALTER|DROP|LOCK|TRUNCATE)\b', re.I)


def targets(sql):
    text = TOKENS.sub(lambda m: m[0] if m[0].startswith('"') else ' ', sql)
    if not SQL_START.match(text):
        return []
    found = []
    for m in TARGET.finditer(text):
        parts = re.split(r'\s*\.\s*', (m[1] or m[2]))
        parts = [p[1:-1].replace('""', '"') if p.startswith('"') else p.lower() for p in parts]
        # Only public is the application schema; do not alias other schemas to it.
        found.append(parts[-1] if len(parts) == 1 or parts[0] == 'public' else '.'.join(parts))
    return found


def go_items(root=ROOT):
    out = subprocess.run(['go', 'run', str(ROOT / 'scripts/schema-sql-literals.go'), str(root)],
                         cwd=root, text=True, capture_output=True, check=True)
    return json.loads(out.stdout)


def sql_items(root=ROOT):
    result = []
    for path in sorted((root / 'internal').rglob('queries.sql')):
        text = path.read_text()
        # The named query is the unit that owns its SQL, including any write CTEs.
        matches = list(re.finditer(r'^-- name: (\w+)\b.*$', text, re.M))
        for i, m in enumerate(matches):
            end = matches[i + 1].start() if i + 1 < len(matches) else len(text)
            result.append(dict(file=str(path.relative_to(root)), line=text.count('\n', 0, m.start()) + 1,
                               declaration=m[1], text=text[m.end():end]))
    return result


def writer(path):
    # A generated leaf is owned by its enclosing subsystem.
    return report._owner_of(path)


def dynamic_key(item):
    return (item['file'], item['declaration'], item['text'], item['declaration_hash'])



def check(items, owners, exceptions):
    errors, used_foreign, used_dynamic = [], set(), set()
    foreign = {(x['file'], x['declaration'], x['table'], x['declaration_hash']): x for x in exceptions['foreign']}
    dynamic = {dynamic_key(x): x for x in exceptions['dynamic']}
    if len(foreign) != len(exceptions['foreign']) or len(dynamic) != len(exceptions['dynamic']):
        errors.append('duplicate write exception/review')
    for entry in exceptions['foreign'] + exceptions['dynamic']:
        if not entry.get('reason', '').strip():
            errors.append(f"write exception needs a reason: {entry.get('file')}")
    literal_count = 0
    for item in items:
        where = f"{item['file']}:{item['line']} ({item['declaration']})"
        if item.get('dynamic'):
            key = dynamic_key(item)
            if key not in dynamic:
                errors.append(f"{where}: unreviewed dynamic Exec: {item['text']}")
            else:
                used_dynamic.add(key)
                entry = dynamic[key]
                for table in entry['tables']:
                    if owners.get(table) != writer(item['file']):
                        errors.append(f"{where}: dynamic target {table} is not owned by {writer(item['file'])}")
            continue
        for table in targets(item['text']):
            # Only the numbered migration declarations may contain foreign DML.
            if item['file'] == 'internal/forge/dbschema/schema.go' and item['declaration'] == 'migrations':
                continue
            literal_count += 1
            owner = owners.get(table)
            if owner == writer(item['file']):
                continue
            key = (item['file'], item['declaration'], table, item.get('declaration_hash', ''))
            if key in foreign and owner == foreign[key]['owner']:
                used_foreign.add(key)
            else:
                errors.append(f"{where}: writes {table}, owned by {owner or 'UNKNOWN'}, from {writer(item['file'])}")
    for key in foreign.keys() - used_foreign:
        errors.append(f"stale foreign-write exception: {key[:3]}")
    for key in dynamic.keys() - used_dynamic:
        errors.append(f"stale dynamic-write review: {key[:2]}")
    return errors, literal_count


def main():
    owners = assembly.table_owners()
    declared = set(re.findall(r'CREATE TABLE (?:IF NOT EXISTS )?(\w+)', (ROOT / 'internal/forge/dbschema/schema.sql').read_text()))
    if declared != set(owners):
        raise SystemExit(f"schema owner inventory differs: missing {declared - set(owners)}, stale {set(owners) - declared}")
    exceptions = json.loads((ROOT / 'scripts/schema-write-exceptions.json').read_text())
    items = go_items() + sql_items()
    if '--inventory' in sys.argv:
        print(json.dumps(items, indent=2))
        return
    errors, count = check(items, owners, exceptions)
    for error in errors:
        print(error)
    print(f"schema ownership {count} literal writes, {len(exceptions['dynamic'])} reviewed dynamic Execs, "
          f"{len(exceptions['foreign'])} foreign exceptions: {'FAIL' if errors else 'ok'}")
    sys.exit(bool(errors))


if __name__ == '__main__':
    main()
