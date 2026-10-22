#!/usr/bin/env python3
"""Write the third-party notices an AppTown release carries: for every Go module
compiled into its binaries and every package in its web bundle, the name,
version and the licence, copyright and notice files it ships, verbatim.

A module under the MPL also gets the address of its exact source (MPL 2.0
section 3.2(a)): the Go module proxy's archive of that version.

  python3 scripts/third-party-notices.py [main packages...] > THIRD_PARTY_NOTICES

The main packages default to apptownd, apptown and apptown-worker. Modules are read
for linux and darwin, so a release for either names everything it may hold.
The web side walks web/package.json's runtime dependencies through
web/node_modules; it is skipped, and says so, when node_modules is absent.
"""
import glob
import json
import os
import subprocess
import sys

MODULE = "github.com/flokkit/apptown"
MAINS = ["./cmd/apptownd", "./cmd/apptown", "./cmd/apptown-worker"]
NOTICE_FILES = ("LICENSE", "LICENCE", "COPYING", "NOTICE", "COPYRIGHT")



def read_text(path):
    with open(path, errors="replace") as fh:
        return fh.read()

def notice_files(d):
    out = []
    for f in sorted(glob.glob(os.path.join(d, "*"))):
        name = os.path.basename(f).upper()
        if os.path.isfile(f) and name.startswith(NOTICE_FILES):
            out.append(f)
    return out


def go_modules(mains):
    """module path → (version, dir), over linux and darwin builds."""
    mods = {}
    template = "{{with .Module}}{{.Path}} {{.Version}} {{.Dir}}{{end}}"
    for goos in ("linux", "darwin"):
        env = {**os.environ, "GOOS": goos, "CGO_ENABLED": "0"}
        out = subprocess.run(["go", "list", "-deps", "-f", template, *mains],
                             check=True, capture_output=True, text=True, env=env).stdout
        for line in out.splitlines():
            parts = line.split(" ", 2)
            if len(parts) == 3 and parts[0] != MODULE:
                mods[parts[0]] = (parts[1], parts[2])
    return mods


def web_packages(web):
    """Every installed package instance in the runtime dependency closure of
    web/, as (name, version, directory). A dependency resolves as Node
    resolves it: in the importing package's own node_modules, then each
    enclosing one up to web/node_modules, so a nested version is the one
    listed when that is what the importer bundles."""
    root = os.path.join(web, "node_modules")
    if not os.path.isdir(root):
        return None

    def resolve(name, importer):
        d = importer
        while True:
            candidate = os.path.join(d, "node_modules", name)
            if os.path.isfile(os.path.join(candidate, "package.json")):
                return os.path.realpath(candidate)
            if os.path.realpath(d) == os.path.realpath(web):
                return None
            d = os.path.dirname(d)
            if not d.startswith(os.path.realpath(web)) and not d.startswith(web):
                return None

    with open(os.path.join(web, "package.json")) as f:
        todo = [(n, web) for n in (json.load(f).get("dependencies") or {})]
    seen = {}
    while todo:
        name, importer = todo.pop()
        d = resolve(name, importer)
        if d is None or d in seen:
            continue
        with open(os.path.join(d, "package.json")) as f:
            meta = json.load(f)
        seen[d] = (name, meta.get("version", ""))
        todo.extend((dep, d) for dep in (meta.get("dependencies") or {}))
    return sorted((name, version, d) for d, (name, version) in seen.items())


def readme_licence(d):
    """A package that keeps its licence in its README: the section headed
    License or Licence, verbatim, up to the next heading of the same or a
    higher level."""
    for f in sorted(glob.glob(os.path.join(d, "*"))):
        if not os.path.basename(f).upper().startswith("README"):
            continue
        with open(f, errors="replace") as fh:
            lines = fh.read().splitlines()
        for i, line in enumerate(lines):
            stripped = line.lstrip("#").strip().lower()
            level = len(line) - len(line.lstrip("#"))
            if level and stripped in ("license", "licence", "license (mit)", "licence (mit)"):
                out = [line]
                for nxt in lines[i + 1:]:
                    nl = len(nxt) - len(nxt.lstrip("#"))
                    if nl and nl <= level:
                        break
                    out.append(nxt)
                return os.path.basename(f), "\n".join(out).strip()
    return None


def proxy_escape(s):
    """The module proxy's case encoding: an upper-case letter is ! and its
    lower case."""
    return "".join("!" + c.lower() if c.isupper() else c for c in s)


def emit(title, files, extra=""):
    print("=" * 78)
    print(title)
    if extra:
        print(extra)
    if not files:
        print("(no licence file in the distributed package; see its upstream)")
    for f in files:
        print("-" * 78)
        print(os.path.basename(f))
        print()
        with open(f, errors="replace") as fh:
            print(fh.read().rstrip())
    print()


def main():
    mains = sys.argv[1:] or MAINS
    print("Third-party software in this AppTown release")
    print("AppTown itself is licensed under the Apache License 2.0 (LICENSE, NOTICE).")
    print()
    for path, (version, d) in sorted(go_modules(mains).items()):
        files = notice_files(d)
        extra = ""
        if any("Mozilla Public License" in read_text(f) for f in files):
            extra = f"Source code (MPL-2.0 section 3.2(a)): https://proxy.golang.org/{proxy_escape(path)}/@v/{proxy_escape(version)}.zip"
        emit(f"Go module {path} {version}", files, extra)
    pkgs = web_packages(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "web"))
    if pkgs is None:
        print("(web/node_modules absent: the web bundle's packages are not listed)")
        return
    for name, version, d in pkgs:
        files = notice_files(d)
        if files:
            emit(f"npm package {name} {version}", files)
            continue
        section = readme_licence(d)
        if section:
            print("=" * 78)
            print(f"npm package {name} {version}")
            print("-" * 78)
            print(f"{section[0]}, licence section")
            print()
            print(section[1])
            print()
            continue
        with open(os.path.join(d, "package.json")) as f:
            meta = json.load(f)
        emit(f"npm package {name} {version}", [],
             f"Declared licence: {meta.get('license', 'none declared')}; author: {meta.get('author', 'not stated')}")


if __name__ == "__main__":
    main()
