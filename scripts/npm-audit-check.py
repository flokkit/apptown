#!/usr/bin/env python3
"""No known high or critical advisory in the npm packages AppTown ships or seeds.

Audits web/package-lock.json without dev dependencies (what the binary
embeds) and every stock/seeds/*/package-lock.json with them (every new
project starts from those versions, build tools included). Fails naming the
lockfile, package and advisory for each high or critical advisory that
scripts/npm-audit-exceptions.txt does not list; fails when an exception is
malformed or no audited lockfile reports it any more; and fails when npm
could not produce a report (the registry or its advisory service
unreachable): not checked is not passed.

It asks the registry's advisory service, so CI runs it (ci.yml's web job),
not the offline make check. Nothing is installed: npm reads the lockfile.

    python3 scripts/npm-audit-check.py [root]

APPTOWN_NPM names the npm to run (the tests' stub); default npm.
"""
import json
import os
import re
import subprocess
import sys
from pathlib import Path

EXCEPTIONS = "scripts/npm-audit-exceptions.txt"
FAILING = ("high", "critical")
GHSA = re.compile(r"GHSA(-[23456789cfghjmpqrvwx]{4}){3}")
DATE = re.compile(r"\d{4}-\d{2}-\d{2}")


def targets(root):
    """Answers [(lockfile directory, include dev dependencies)]."""
    found = [(root / "web", False)]
    for lock in sorted((root / "stock" / "seeds").glob("*/package-lock.json")):
        found.append((lock.parent, True))
    return found


def audit(directory, dev):
    """Answers (advisories, None) or (None, why npm could not check).

    Each advisory is (ghsa id, package, severity, title)."""
    cmd = [os.environ.get("APPTOWN_NPM", "npm"), "audit", "--package-lock-only", "--json"]
    if not dev:
        cmd.append("--omit=dev")
    try:
        out = subprocess.run(cmd, cwd=directory, capture_output=True, text=True, timeout=300)
    except (OSError, subprocess.TimeoutExpired) as e:
        return None, str(e)
    try:
        report = json.loads(out.stdout)
    except ValueError:
        report = None
    if not isinstance(report, dict) or "vulnerabilities" not in report or "metadata" not in report:
        why = report.get("message") if isinstance(report, dict) else None
        lines = (out.stderr.strip() or out.stdout.strip() or f"npm exited {out.returncode}").splitlines()
        return None, why or lines[-1]
    found = set()
    for entry in report["vulnerabilities"].values():
        for via in entry.get("via", []):
            # A string names another vulnerable package; its own entry
            # carries the advisory.
            if not isinstance(via, dict):
                continue
            m = GHSA.search(via.get("url", ""))
            ident = m.group(0) if m else f"npm advisory {via.get('source')}"
            found.add((ident, via.get("name", "?"), via.get("severity", "?"), via.get("title", "")))
    return sorted(found), None


def exceptions(root):
    """Answers ({ghsa id: line number}, errors)."""
    allowed, errors = {}, []
    path = root / EXCEPTIONS
    if not path.is_file():
        return allowed, errors
    for n, raw in enumerate(path.read_text().splitlines(), 1):
        line = raw.split("#", 1)[0].strip()
        if not line:
            continue
        parts = line.split(None, 2)
        if len(parts) < 3 or not GHSA.fullmatch(parts[0]) or not DATE.fullmatch(parts[1]):
            errors.append(f"{EXCEPTIONS}:{n}: a line is one advisory's GHSA id, the date the exception was taken (YYYY-MM-DD) and why")
            continue
        allowed[parts[0]] = n
    return allowed, errors


def check(root):
    allowed, errors = exceptions(root)
    reported = set()
    for directory, dev in targets(root):
        lock = (directory / "package-lock.json").relative_to(root)
        advisories, why = audit(directory, dev)
        if advisories is None:
            errors.append(f"{lock}: could not check: {why}")
            continue
        for ident, name, severity, title in advisories:
            reported.add(ident)
            if severity in FAILING and ident not in allowed:
                errors.append(f"{lock}: {name}: {severity}: {ident} {title}; take the fixed version, "
                              f"or, if there is none, add a dated line to {EXCEPTIONS} with why")
    # Only when every lockfile was checked can an exception be called stale.
    if not any("could not check" in e for e in errors):
        for ident, n in sorted(allowed.items()):
            if ident not in reported:
                errors.append(f"{EXCEPTIONS}:{n}: no audited lockfile reports {ident} any more; remove the line")
    return errors


def main(argv):
    root = Path(argv[1]) if len(argv) > 1 else Path(".")
    errors = check(root.resolve())
    for e in errors:
        print(e)
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
