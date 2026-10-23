#!/usr/bin/env python3
"""No compiled program is committed to this repository.

AppTown ships projects the check repo/no-build-artifacts, whose
no-build-artifacts-tracked names a committed ELF, Mach-O or Windows
executable or Java class file by its format signature. This repository keeps
the same rule, with the same script: it is read from
stock/guidance/repo/no-build-artifacts.md, not copied, so a change to the
stock check changes this gate.

The script runs over the index, written out to a temporary directory (git
checkout-index), so it judges exactly what is tracked and staged: a binary a
developer built into the worktree and did not add is not a finding. Exit 0
passes, 1 names the files, anything else is an error, never a pass.

    python3 scripts/build-artifacts-check.py [repo]
"""
import json
import subprocess
import sys
import tempfile
from pathlib import Path

GUIDANCE = "stock/guidance/repo/no-build-artifacts.md"
CHECK = "no-build-artifacts-tracked"


def stock_script(root):
    """Answers the check's sh, from the guidance's frontmatter checks: line."""
    for line in (root / GUIDANCE).read_text().splitlines():
        if line.startswith("checks: "):
            for check in json.loads(line[len("checks: "):]):
                if check.get("name") == CHECK and check.get("sh"):
                    return check["sh"]
    print(f"{GUIDANCE} declares no sh check {CHECK}", file=sys.stderr)
    sys.exit(2)  # not 1: a missing check is an error, not a finding


def main():
    root = Path(sys.argv[1] if len(sys.argv) > 1 else ".").resolve()
    script = stock_script(root)
    with tempfile.TemporaryDirectory() as tmp:
        tree = Path(tmp) / "tree"
        if subprocess.run(["git", "-C", str(root), "checkout-index", "-a", f"--prefix={tree}/"]).returncode != 0:
            print("could not write out the index to judge it", file=sys.stderr)
            return 2
        tree.mkdir(exist_ok=True)  # an empty index writes nothing
        run = subprocess.run(["sh", "-c", script], cwd=tree, capture_output=True, text=True)
    if run.returncode == 0:
        print("no compiled program is tracked")
        return 0
    if run.returncode == 1:
        print(f"compiled programs are tracked ({GUIDANCE}, {CHECK}); untrack them with git rm --cached:")
        print(run.stdout, end="")
        return 1
    print(f"{CHECK} could not judge the tree (exit {run.returncode}):\n{run.stdout}{run.stderr}", file=sys.stderr)
    return 2


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as e:  # exit 1 means findings; anything unforeseen is an error
        print(f"{CHECK} could not run: {e!r}", file=sys.stderr)
        sys.exit(2)
