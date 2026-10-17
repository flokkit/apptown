#!/usr/bin/env python3
"""Print the Go packages a branch's changes can affect: every package whose
files changed since its merge base with BASE (default origin/main), plus every
package that imports one of them, directly or not, or whose tests do.

`make precheck` tests these before a branch goes to the gate, so a change that
breaks a package it never edited (a fixture that builds a changed service) is
found locally. The gate's full run stays the witness; this only saves a cycle.
Uncommitted changes count. A changed file that is not Go source seeds the
nearest Go package at or above it, since that package may embed it (stock/
embeds the shipped guides and seeds, dbschema embeds schema.sql); a change to
go.mod or go.sum seeds every package. Prints nothing when nothing reaches a
Go package (web/ and design/ changes, say), and exits non-zero when it cannot
tell.
"""
import os
import subprocess
import sys

MODULE = "flokkit.io/forge"
SKIP = MODULE + "/web/node_modules"


def run(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True).stdout


def changed_files(base):
    merge_base = run("git", "merge-base", "HEAD", base).strip()
    files = set(run("git", "diff", "--name-only", merge_base, "--").split())
    files |= set(run("git", "diff", "--name-only", "HEAD", "--").split())
    files |= set(run("git", "ls-files", "--others", "--exclude-standard").split())
    return {f for f in files if not f.startswith("web/node_modules/")}


def seeds(files, deps):
    """The packages the changed files belong to: a Go file its directory's, any
    other file the nearest package at or above it (which may embed it); go.mod
    or go.sum every package."""
    if files & {"go.mod", "go.sum"}:
        return set(deps)
    out = set()
    for f in files:
        d = os.path.dirname(f)
        while True:
            pkg = MODULE if d == "" else MODULE + "/" + d
            if pkg in deps:
                out.add(pkg)
                break
            if f.endswith(".go") or d == "":
                break  # a Go file outside any listed package (a script), or no package above
            d = os.path.dirname(d)
    return out


def graph():
    """Each package and what it, or its tests, depend on."""
    template = "{{.ImportPath}}{{range .Deps}} {{.}}{{end}}{{range .TestImports}} {{.}}{{end}}{{range .XTestImports}} {{.}}{{end}}"
    out = {}
    for line in run("go", "list", "-e", "-f", template, "./...").splitlines():
        fields = line.split()
        if fields and not fields[0].startswith(SKIP):
            out[fields[0]] = set(fields[1:])
    return out


def affected(changed, deps):
    """Changed packages, and every package depending on one of them. A test
    import's own dependencies are followed through the graph."""
    hit = set(changed)
    grew = True
    while grew:
        grew = False
        for pkg, uses in deps.items():
            if pkg not in hit and uses & hit:
                hit.add(pkg)
                grew = True
    return hit


def main():
    base = sys.argv[1] if len(sys.argv) > 1 else "origin/main"
    deps = graph()
    for pkg in sorted(affected(seeds(changed_files(base), deps), deps)):
        print(pkg)


if __name__ == "__main__":
    main()
