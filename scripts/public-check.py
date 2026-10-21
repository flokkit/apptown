#!/usr/bin/env python3
"""The public gate: what a push publishes holds nothing private.

This repository is public. The maintainers keep their own material in a
separate private repository checked out at private/ (ignored here), so a
mistake is the only way it reaches a commit: a forced add, a copied file, a
host name pasted into a comment. This check reads the committed tree of HEAD
(git archive HEAD, so exactly what a push publishes) and refuses it when:

1. a path is under private/, or is one of the files the maintainers keep
   beside it (.gitlab-ci.yml, notes.txt, presso* at the root);
2. a text file names one of the maintainers' home-lab hosts or addresses.
   scripts/public-check-names.txt lists their SHA-256, not the names, so this
   check does not publish what it keeps out. Every host-name-like token, each
   of its parent domains (a subdomain is caught by its domain's hash) and
   every IPv4 address is hashed and looked up. An address's hash is easy to
   reverse: the point is not to advertise the names, not secrecy;
3. gitleaks finds a secret. Without gitleaks the check fails: no scan is not
   a clean scan.

Usage: public-check.py [--root DIR] [--names FILE]
       public-check.py --hash NAME...    (lines for public-check-names.txt)
GITLEAKS names the gitleaks binary (default: gitleaks on PATH).
"""
import argparse
import hashlib
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
EXCLUDED = re.compile(r"^(private/|\.gitlab-ci\.yml$|notes\.txt$|presso[^/]*$)")
TOKEN = re.compile(rb"[a-z0-9][a-z0-9.-]*[a-z0-9]|[a-z0-9]")


def digest(name):
    return hashlib.sha256(name.strip().lower().encode()).hexdigest()


def read_names(path):
    out = set()
    for line in Path(path).read_text().splitlines():
        line = line.split("#", 1)[0].strip()
        if line:
            out.add(line.lower())
    return out


def candidates(token):
    """The token and every parent domain of it: a.b.c gives a.b.c, b.c, c."""
    yield token
    while b"." in token:
        token = token.split(b".", 1)[1]
        if token:
            yield token


def home_names(tree, files, names):
    seen = {}
    problems = []
    for rel in files:
        data = (tree / rel).read_bytes()
        if b"\0" in data[:8192]:
            continue
        hits = set()
        for token in set(TOKEN.findall(data.lower())):
            for c in candidates(token):
                h = seen.get(c)
                if h is None:
                    h = seen[c] = hashlib.sha256(c).hexdigest()
                if h in names:
                    hits.add(h[:12])
        if hits:
            problems.append(f"{rel}: names a maintainers' home-lab host or address (hash {', '.join(sorted(hits))}; private/scripts/home-names.txt says which)")
    return problems


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", default=None)
    ap.add_argument("--names", default=str(HERE / "public-check-names.txt"))
    ap.add_argument("--hash", nargs="+", metavar="NAME")
    args = ap.parse_args()
    if args.hash:
        for n in args.hash:
            print(digest(n))
        return 0

    root = Path(args.root) if args.root else HERE.parent
    gitleaks = os.environ.get("GITLEAKS", "gitleaks")
    if not shutil.which(gitleaks):
        print(f"public check: {gitleaks} is not installed, so the tree was not scanned for secrets "
              "(brew install gitleaks, or see .github/workflows/ci.yml)", file=sys.stderr)
        return 1
    names = read_names(args.names)
    with tempfile.TemporaryDirectory() as tmp:
        tree = Path(tmp) / "tree"
        tree.mkdir()
        archive = subprocess.run(["git", "-C", str(root), "archive", "HEAD"], capture_output=True)
        if archive.returncode != 0:
            print(f"public check: cannot read HEAD's tree: {archive.stderr.decode().strip()}", file=sys.stderr)
            return 1
        subprocess.run(["tar", "-x", "-C", str(tree)], input=archive.stdout, check=True)
        files = sorted(str(p.relative_to(tree)) for p in tree.rglob("*") if p.is_file() and not p.is_symlink())

        problems = [f"{f}: private material is never committed here (it belongs in the private repository)"
                    for f in files if EXCLUDED.match(f)]
        problems += home_names(tree, files, names)
        leaks = subprocess.run([gitleaks, "dir", "--no-banner", "--no-color", "--verbose", "--redact", "--exit-code", "1", str(tree)],
                               capture_output=True, text=True)
        if leaks.returncode != 0:
            report = (leaks.stdout + leaks.stderr).replace(str(tree) + "/", "")
            problems.append("gitleaks found something (a test fixture that only looks like a secret carries an inline gitleaks:allow):\n" + report.strip())
    if problems:
        print("public check: HEAD's tree is not fit to publish:", file=sys.stderr)
        for p in problems:
            print("  " + p, file=sys.stderr)
        return 1
    print(f"public check: {len(files)} files, no private path, no home-lab name, no secret")
    return 0


if __name__ == "__main__":
    sys.exit(main())
