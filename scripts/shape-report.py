#!/usr/bin/env python3
"""The shape report: the shape checker's
sibling. Prints the five facts a domain-model decision needs, each with file:line
evidence, over production Go code under internal/ and cmd/ (never _test.go). A
report, not a gate: no baseline, always exits 0. `--json` prints the same five
reports as one JSON document. Reuses scripts/shape-check.py's package lists and
helpers (MOD, SUBSYSTEMS, FACADES, pkg_dir) rather than restating them.
"""
import importlib.util
import json
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

def _load_shape_check():
    spec = importlib.util.spec_from_file_location("shape_check", os.path.join(ROOT, "scripts", "shape-check.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod

_sc = _load_shape_check()
MOD, SUBSYSTEMS, FACADES, pkg_dir = _sc.MOD, _sc.SUBSYSTEMS, _sc.FACADES, _sc.pkg_dir

def production_files(*bases):
    """Every .go file under the given ROOT-relative bases, production code only:
    no _test.go, no testdata/. Cannot see build tags or files excluded by them."""
    out = []
    for base in bases:
        for dirpath, dirnames, filenames in os.walk(os.path.join(ROOT, base)):
            dirnames[:] = [d for d in dirnames if d != "testdata"]
            for f in sorted(filenames):
                if f.endswith(".go") and not f.endswith("_test.go"):
                    out.append(os.path.relpath(os.path.join(dirpath, f), ROOT))
    return sorted(out)

def read(relpath):
    with open(os.path.join(ROOT, relpath), encoding="utf-8") as f:
        return f.read()

def line_of(text, pos):
    return text.count("\n", 0, pos) + 1

def package_index():
    """Declared Go package name -> its directory, one entry per directory under
    internal/. Cannot tell two same-named packages in different directories apart
    (keeps the first); cannot see import aliases, so a caller that renames an
    import on the way in will not match here."""
    idx, seen = {}, set()
    for relpath in production_files("internal"):
        d = os.path.dirname(relpath)
        if d in seen:
            continue
        seen.add(d)
        m = re.search(r"^package (\w+)", read(relpath), re.M)
        if m:
            idx.setdefault(m.group(1), d)
    return idx

def dir_has_service(d):
    if not d:
        return False
    for relpath in production_files(d):
        if os.path.dirname(relpath) != d:
            continue  # a package's own files only, not a nested sub-package
        if re.search(r"^type Service (struct|interface)\b", read(relpath), re.M):
            return True
    return False

WRITE_PAT = re.compile(r"\bINSERT\s+INTO\s+(\w+)|\bUPDATE\s+(\w+)\s+SET\b|\bDELETE\s+FROM\s+(\w+)", re.I)

def _writes_in(text):
    for m in WRITE_PAT.finditer(text):
        table = m.group(1) or m.group(2) or m.group(3)
        yield table.lower(), line_of(text, m.start())

def _owner_of(relpath):
    """The writing package for an ordinary production file: the subsystem or
    library directory (internal/forge/<x>/... -> x; internal/forge/*.go -> forge;
    internal/<lib>/... -> lib), or cmd/<name> for a command."""
    parts = relpath.split(os.sep)
    if parts[0] == "cmd":
        return "cmd/" + parts[1]
    rest = parts[1:]
    if rest[0] == "forge":
        rest = rest[1:]
        if not rest or rest[0].endswith(".go"):
            return "forge"
    return rest[0]

def _query_files(dirpath, filenames):
    """A directory's sqlc query files: queries.sql, and any x.sql that sqlc has
    generated x.sql.go from (an owner may keep its named SQL as several files)."""
    return sorted(f for f in filenames if f.endswith(".sql") and (f == "queries.sql" or f + ".go" in filenames))

def _sqlc_dirs():
    """Directories that look sqlc-generated (query files beside generated .go),
    mapped to the package they belong to: the directory's own parent, unless that
    parent is itself named 'internal' (a subsystem's private sqlc folder), in
    which case the grandparent -- the subsystem -- owns it."""
    dirs = {}
    for dirpath, _, filenames in os.walk(os.path.join(ROOT, "internal")):
        if _query_files(dirpath, filenames):
            rel = os.path.relpath(dirpath, ROOT)
            parts = rel.split(os.sep)
            parent = parts[-2] if len(parts) >= 2 else None
            owner = parts[-3] if parent == "internal" and len(parts) >= 3 else parent
            dirs[rel] = owner
    return dirs

def tables_written_from_many_packages():
    """For every INSERT/UPDATE/DELETE against a table -- hand SQL in Go source and
    sqlc's query files -- the packages that write it, grouped by table. Cannot see
    a table name built at runtime (string concatenation, a variable): only a
    literal identifier right after the SQL keyword. A sqlc directory's writes are
    attributed to the package that owns it, not to the generated package name."""
    sqlc = _sqlc_dirs()
    sqlc_dir_set = set(sqlc)

    def under_sqlc(relpath):
        d = os.path.dirname(relpath)
        while True:
            if d in sqlc_dir_set:
                return True
            nd = os.path.dirname(d)
            if nd == d:
                return False
            d = nd

    by_table = {}  # table -> pkg -> [ "file:line" ]

    for relpath in production_files("internal", "cmd"):
        if under_sqlc(relpath):
            continue
        text = read(relpath)
        for table, ln in _writes_in(text):
            by_table.setdefault(table, {}).setdefault(_owner_of(relpath), set()).add(f"{relpath}:{ln}")

    for d, owner in sqlc.items():
        for name in _query_files(d, os.listdir(os.path.join(ROOT, d))):
            qsql = os.path.join(d, name)
            text = read(qsql)
            for table, ln in _writes_in(text):
                by_table.setdefault(table, {}).setdefault(owner or d, set()).add(f"{qsql}:{ln}")

    items = []
    for table in sorted(by_table):
        pkgs = by_table[table]
        if len(pkgs) < 2:
            continue
        items.append({
            "table": table,
            "packages": sorted(pkgs),
            "evidence": [ev for p in sorted(pkgs) for ev in sorted(pkgs[p])],
        })
    return items

def parse_app_struct():
    """(name, type, line) for each field of internal/forge/app.go's App struct,
    in source order. Cannot see fields declared several to a line."""
    relpath = "internal/forge/app.go"
    text = read(relpath)
    m = re.search(r"^type App struct \{\n(.*?)^\}", text, re.M | re.S)
    if not m:
        return []
    base = line_of(text, m.start(1))
    fields = []
    for i, line in enumerate(m.group(1).splitlines()):
        raw = re.split(r"\s+//", line.strip(), maxsplit=1)[0].strip()
        if not raw or raw.startswith("//"):
            continue
        parts = raw.split(None, 1)
        if len(parts) != 2:
            continue
        fields.append((parts[0], parts[1].strip(), base + i))
    return fields

PART_TYPE = re.compile(r"^\*([a-z]\w*)\.([A-Z]\w*)$")
STRUCT_MULTI = re.compile(r"^type (\w+) struct \{\n(.*?)^\}", re.M | re.S)
STRUCT_ONE = re.compile(r"^type (\w+) struct\{(.*?)\}", re.M)

def _struct_field_names(body):
    names = []
    for part in re.split(r"[;\n]", body):
        part = part.strip()
        if not part or part.startswith("//"):
            continue
        part = re.split(r"\s+//", part, maxsplit=1)[0].strip()
        if not part:
            continue
        names.append(part.split()[0].lstrip("*"))
    return names

def app_fields_that_are_parts():
    """Three shapes of an App field that is really a subsystem's internal part,
    not a door: (a) a *pkg.X pointer where X isn't pkg's Service, so pkg has a
    proper door but this field reaches around it; (b) a sync.Once, paired with
    the field declared right after it (source order only -- may not be the field
    it actually guards); (c) an unexported struct type, defined at the top level
    of internal/forge, that itself holds a db handle. Cannot see fields reached
    only through a further level of indirection (a struct holding the *pkg.X)."""
    fields = parse_app_struct()
    idx = package_index()
    items = []

    for name, typ, ln in fields:
        m = PART_TYPE.match(typ)
        if m and m.group(2) != "Service" and dir_has_service(idx.get(m.group(1))):
            items.append({
                "kind": "part-typed field",
                "field": name,
                "reason": f"{typ} is a part of {m.group(1)}, which has a Service",
                "evidence": [f"internal/forge/app.go:{ln}"],
            })

    for i, (name, typ, ln) in enumerate(fields):
        if typ == "sync.Once":
            guarded = fields[i + 1] if i + 1 < len(fields) else None
            items.append({
                "kind": "sync.Once field",
                "field": name,
                "reason": f"guards {guarded[0]} (the next field)" if guarded else "guards nothing after it",
                "evidence": [f"internal/forge/app.go:{ln}"] + ([f"internal/forge/app.go:{guarded[2]}"] if guarded else []),
            })

    app_field_types = {m.group(1) for _, typ, _ in fields for m in [re.match(r"^\*?(\w+)$", typ)] if m}
    for relpath in production_files("internal/forge"):
        if os.path.dirname(relpath) != "internal/forge":
            continue
        text = read(relpath)
        for pat in (STRUCT_MULTI, STRUCT_ONE):
            for sm in pat.finditer(text):
                tname, body = sm.group(1), sm.group(2)
                if not tname[0].islower():
                    continue
                if tname not in app_field_types:
                    continue
                if "db" in _struct_field_names(body):
                    items.append({
                        "kind": "field type holds a db handle",
                        "field": tname,
                        "reason": f"{tname} has a db field of its own",
                        "evidence": [f"{relpath}:{line_of(text, sm.start())}"],
                    })
    return items

def _all_structs():
    for relpath in production_files("internal", "cmd"):
        text = read(relpath)
        for pat in (STRUCT_MULTI, STRUCT_ONE):
            for m in pat.finditer(text):
                fields = _struct_field_names(m.group(2))
                yield {
                    "pkg": os.path.dirname(relpath),
                    "name": m.group(1),
                    "fields": set(fields),
                    "file": relpath,
                    "line": line_of(text, m.start()),
                }

def matching_type_pairs():
    """Pairs of structs in different packages whose field-name sets overlap 80%
    or more by Jaccard similarity, both with at least 4 fields: two shapes that
    may be one idea wearing two names. Cannot tell a coincidence (both happen to
    have Name/ID/CreatedAt) from a real duplicate -- that judgement is the
    reader's; it only surfaces candidates with evidence to check."""
    structs = [s for s in _all_structs() if len(s["fields"]) >= 4]
    items = []
    for i in range(len(structs)):
        a = structs[i]
        for j in range(i + 1, len(structs)):
            b = structs[j]
            if a["pkg"] == b["pkg"]:
                continue
            shared = a["fields"] & b["fields"]
            union = a["fields"] | b["fields"]
            sim = len(shared) / len(union) if union else 0
            if sim >= 0.8:
                items.append({
                    "a": f"{a['pkg']}.{a['name']}",
                    "b": f"{b['pkg']}.{b['name']}",
                    "similarity": round(sim, 2),
                    "shared_fields": sorted(shared),
                    "evidence": [f"{a['file']}:{a['line']}", f"{b['file']}:{b['line']}"],
                })
    items.sort(key=lambda it: -it["similarity"])
    return items

def app_reach_per_package():
    """For every package internal/forge (the App) imports directly: how many
    times its own top-level files (internal/forge/*.go, not its subsystem
    subdirectories) say pkg.Exported, in how many of those files, and how many
    times they call a door a.<Field>.<Method>( where <Field> is an App field of
    that package's Service. Sorted by references, most-referenced first. Cannot
    see a reference through a local alias or an embedded field. Evidence is
    sampled (first 5), not exhaustive, when a package runs to hundreds of hits."""
    out = subprocess.run(["go", "list", "-f", '{{join .Imports "\\n"}}', "./internal/forge"],
                          cwd=ROOT, capture_output=True, text=True)
    imports = [l for l in out.stdout.split() if l.startswith(MOD)]
    fields = parse_app_struct()

    top_level = [f for f in production_files("internal/forge") if os.path.dirname(f) == "internal/forge"]
    texts = {f: read(f) for f in top_level}

    items = []
    for imp in sorted(set(imports)):
        qual = imp.rsplit("/", 1)[-1]
        door_fields = [name for name, typ, _ in fields if typ == f"*{qual}.Service"]
        ref_pat = re.compile(r"\b" + re.escape(qual) + r"\.[A-Z]\w*")
        door_pats = [re.compile(r"\ba\." + re.escape(f) + r"\.\w+\(") for f in door_fields]

        refs, files_hit = [], 0
        door_calls, door_sample = 0, []
        for relpath, text in texts.items():
            hits = [line_of(text, m.start()) for m in ref_pat.finditer(text)]
            if hits:
                files_hit += 1
                refs.extend((relpath, ln) for ln in hits)
            for dp in door_pats:
                for m in dp.finditer(text):
                    door_calls += 1
                    if len(door_sample) < 5:
                        door_sample.append(f"{relpath}:{line_of(text, m.start())}")
        if not refs and not door_calls:
            continue
        sample = [f"{r}:{ln}" for r, ln in refs[:5]]
        items.append({
            "package": qual,
            "references": len(refs),
            "files": files_hit,
            "door_calls": door_calls,
            "door_fields": door_fields,
            "evidence": sample,
            "door_evidence": door_sample,
        })
    items.sort(key=lambda it: -it["references"])
    return items

def _split_top_commas(s):
    parts, depth, cur = [], 0, []
    for ch in s:
        if ch in "([{":
            depth += 1
        elif ch in ")]}":
            depth -= 1
        if ch == "," and depth == 0:
            parts.append("".join(cur))
            cur = []
        else:
            cur.append(ch)
    parts.append("".join(cur))
    return parts

ACTOR_NAMES = {"who", "by", "actor", "caller"}

def _actor_params(params_text):
    groups, pending = [], []
    for part in _split_top_commas(params_text):
        part = part.strip()
        if not part:
            continue
        tokens = part.split()
        if len(tokens) == 1:
            pending.append(tokens[0])
            continue
        name, typ = tokens[0], " ".join(tokens[1:])
        groups.append((pending + [name], typ))
        pending = []
    hits = []
    for names, typ in groups:
        if typ == "string":
            hits.extend(n for n in names if n in ACTOR_NAMES)
    return hits

def _method_signatures(text):
    for m in re.finditer(r"^func \(\w+ \*?(\w+)\) ([A-Za-z_]\w*)\(", text, re.M):
        start = m.end() - 1
        depth, i = 0, start
        while i < len(text):
            if text[i] == "(":
                depth += 1
            elif text[i] == ")":
                depth -= 1
                if depth == 0:
                    break
            i += 1
        yield m.group(1), m.group(2), text[start + 1:i], line_of(text, m.start())

def doors_with_an_actor_string():
    """Methods on a subsystem's *Service, or (any receiver) on a facade, taking a
    parameter literally named who/by/actor/caller of type string: a door that
    trusts its caller to say who is acting, as against one that reads a Principal
    from context. Cannot see an actor threaded through a struct parameter instead
    of a bare string, or named anything else (requester, editor, ...)."""
    items = []

    def scan(pkgdir, only_service):
        if not os.path.isdir(os.path.join(ROOT, pkgdir)):
            return
        for relpath in production_files(pkgdir):
            text = read(relpath)
            for recv, method, params, ln in _method_signatures(text):
                if only_service and recv != "Service":
                    continue
                if not method[0].isupper():
                    continue
                hits = _actor_params(params)
                if hits:
                    items.append({
                        "signature": f"({recv}) {method}({params.strip()})",
                        "actor_params": hits,
                        "evidence": [f"{relpath}:{ln}"],
                    })

    for p in SUBSYSTEMS:
        scan(pkg_dir(p), only_service=True)
    for p in FACADES:
        scan(pkg_dir(p), only_service=False)
    return items

# ---------------------------------------------------------------------------

REPORTS = [
    ("tables_written_from_many_packages", "Tables written from more than one package", tables_written_from_many_packages),
    ("app_fields_that_are_parts", "App fields that are parts", app_fields_that_are_parts),
    ("matching_type_pairs", "Type pairs with matching field sets across packages", matching_type_pairs),
    ("app_reach_per_package", "The App's reach per package", app_reach_per_package),
    ("doors_with_an_actor_string", "Doors that take an actor string", doors_with_an_actor_string),
]

LINE = {
    "tables_written_from_many_packages": lambda it: f"{it['table']}: {', '.join(it['packages'])}",
    "app_fields_that_are_parts": lambda it: f"{it['field']} ({it['kind']}): {it['reason']}",
    "matching_type_pairs": lambda it: f"{it['a']} ~ {it['b']}  ({it['similarity']*100:.0f}%): {', '.join(it['shared_fields'])}",
    "doors_with_an_actor_string": lambda it: it["signature"],
}

def main():
    results = {key: fn() for key, _, fn in REPORTS}
    if "--json" in sys.argv:
        print(json.dumps(results, indent=2))
        return
    for key, title, _ in REPORTS:
        items = results[key]
        print(f"\n=== {title} ({len(items)}) ===")
        if key == "app_reach_per_package":
            print(f"{'package':14} {'refs':>6} {'files':>6} {'door calls':>10}  door fields")
            for it in items:
                print(f"{it['package']:14} {it['references']:6} {it['files']:6} {it['door_calls']:10}  {','.join(it['door_fields'])}")
                for ev in it["evidence"]:
                    print("    ref", ev)
                for ev in it["door_evidence"]:
                    print("    door", ev)
            continue
        if not items:
            print("  none")
        for it in items:
            print(" ", LINE[key](it))
            for ev in it["evidence"]:
                print("     ", ev)

if __name__ == "__main__":
    main()
