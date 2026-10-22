#!/usr/bin/env python3
"""AppTown's own npm installs run no dependency's install script.

Fails when web/.npmrc does not set ignore-scripts=true, when a committed
package-lock.json has a package with an install script that
web/install-scripts.txt does not list, and when the list names a package
that no longer has one. npm records the scripts itself, as hasInstallScript
in the lockfile. Seeds and testdata lockfiles belong to projects and
fixtures, not to AppTown's build.

    python3 scripts/npm-scripts-check.py [root]
"""
import json
import os
import sys
from pathlib import Path

NPMRC = "web/.npmrc"
LIST = "web/install-scripts.txt"


def lockfiles(root):
    skip = {"node_modules", "testdata", ".git", "private"}
    found = []
    for d, dirs, files in os.walk(root):
        rel = Path(d).relative_to(root)
        dirs[:] = sorted(x for x in dirs if x not in skip and (rel / x).parts[:2] != ("stock", "seeds"))
        if "package-lock.json" in files:
            found.append(Path(d) / "package-lock.json")
    return sorted(found)


def with_scripts(lock):
    """The names of the packages in a lockfile that have an install script."""
    names = set()
    for key, pkg in json.loads(lock.read_text()).get("packages", {}).items():
        if key and pkg.get("hasInstallScript"):
            names.add(pkg.get("name") or key.rpartition("node_modules/")[2])
    return names


def listed(root):
    names = {}
    for n, raw in enumerate((root / LIST).read_text().splitlines(), 1):
        line = raw.split("#", 1)[0].strip()
        if not line:
            continue
        parts = line.split(None, 1)
        if len(parts) < 2:
            raise SystemExit(f"{LIST}:{n}: a line needs the package and why it works without its script")
        names[parts[0]] = n
    return names


def check(root):
    errors = []
    settings = [l.split("#", 1)[0].replace(" ", "") for l in (root / NPMRC).read_text().splitlines()] if (root / NPMRC).is_file() else []
    if "ignore-scripts=true" not in settings:
        errors.append(f"{NPMRC}: does not set ignore-scripts=true, so npm ci runs every dependency's install script")
    allowed = listed(root) if (root / LIST).is_file() else {}
    seen = set()
    for lock in lockfiles(root):
        for name in sorted(with_scripts(lock)):
            seen.add(name)
            if name not in allowed:
                errors.append(f"{lock.relative_to(root)}: {name} has an install script; it will not run. "
                              f"Check that {name} works without it, then add it to {LIST} with why")
    for name, n in sorted(allowed.items()):
        if name not in seen:
            errors.append(f"{LIST}:{n}: no lockfile has {name} with an install script any more; remove the line")
    return errors


def main(argv):
    root = Path(argv[1]) if len(argv) > 1 else Path(".")
    errors = check(root)
    for e in errors:
        print(e)
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
