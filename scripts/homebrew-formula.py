#!/usr/bin/env python3
"""The Homebrew formula for the apptown CLI, from a release's SHA256SUMS.

    python3 scripts/homebrew-formula.py <version> <SHA256SUMS> > apptown.rb

<version> is the tag without its v (1.0.0), as scripts/release-archives.sh
takes it; SHA256SUMS is the one that script writes and the release carries.
The formula is deploy/homebrew/apptown.rb.in with the version and each
archive's sha256 filled in. The release workflow (.github/workflows/images.yml,
job homebrew) runs this on a final release tag and pushes the result to
github.com/flokkit/homebrew-tap as Formula/apptown.rb.

Refuses, naming the problem, a version with its v, a line that is not
"<sha256>  <name>", an archive missing for any platform, a duplicate, and a
placeholder left unfilled: a formula with a wrong or missing hash would be
pushed to everyone who installs it.
"""

import os
import re
import sys

# The platforms scripts/release-archives.sh builds (its platforms= line;
# homebrew-formula_test.py holds the two lists equal).
PLATFORMS = ("linux-amd64", "linux-arm64", "darwin-arm64", "darwin-amd64")
TEMPLATE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "deploy", "homebrew", "apptown.rb.in")
LINE = re.compile(r"^([0-9a-f]{64})  \*?(\S+)$")


def formula(version, sums_text, template):
    """Answers the formula text, or raises ValueError naming the problem."""
    if version.startswith("v"):
        raise ValueError(f"version without its v: {version[1:]}")
    hashes = {}
    for n, raw in enumerate(sums_text.splitlines(), 1):
        if not raw.strip():
            continue
        m = LINE.match(raw)
        if not m:
            raise ValueError(f"SHA256SUMS:{n}: not '<sha256>  <name>': {raw!r}")
        digest, name = m.groups()
        for p in PLATFORMS:
            if name == f"apptown-{version}-{p}.tar.gz":
                if p in hashes:
                    raise ValueError(f"SHA256SUMS:{n}: {name} is listed twice")
                hashes[p] = digest
    missing = [f"apptown-{version}-{p}.tar.gz" for p in PLATFORMS if p not in hashes]
    if missing:
        raise ValueError("SHA256SUMS has no line for " + ", ".join(missing))
    out = template.replace("@VERSION@", version)
    for p, digest in hashes.items():
        out = out.replace(f"@{p}@", digest)
    left = sorted(set(re.findall(r"@[A-Za-z0-9-]+@", out)))
    if left:
        raise ValueError("the template has placeholders nothing fills: " + ", ".join(left))
    return out


def main(argv):
    if len(argv) != 3:
        print(f"usage: {argv[0]} <version> <SHA256SUMS>", file=sys.stderr)
        return 2
    with open(argv[2]) as f:
        sums = f.read()
    with open(TEMPLATE) as f:
        template = f.read()
    try:
        sys.stdout.write(formula(argv[1], sums, template))
    except ValueError as e:
        print(f"homebrew-formula: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
