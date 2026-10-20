#!/usr/bin/env python3
"""The tour's freshness stamp (site/tour/stamp.json).

  python3 scripts/tour-stamp.py write   after make tour has taken the shots
  python3 scripts/tour-stamp.py check   exits 1 when the shots are out of date

The stamp is a SHA-256 over the relative path and contents of every file the
shots can depend on: the server and stock content, the web app's source and
build configuration, and the tour's own stops and camera. Broad directories,
not a dependency graph: any change in them makes the stamp stale, and the
release script then refuses to build until make tour runs again. The tour's
outputs are not inputs.
"""
import hashlib
import json
import os
import sys

INPUTS = ["cmd", "internal", "stock", "go.mod", "go.sum", "web", "site/tour/tour.yaml", "site/tour/shoot.mjs", "site/tour/chat.jsonl"]
SKIP_DIRS = {"web/dist", "web/node_modules", ".git"}


def files(root):
    for top in INPUTS:
        path = os.path.join(root, top)
        if os.path.isfile(path):
            yield top
            continue
        for dirpath, dirnames, filenames in os.walk(path):
            rel = os.path.relpath(dirpath, root)
            dirnames[:] = sorted(d for d in dirnames if os.path.join(rel, d) not in SKIP_DIRS)
            for name in sorted(filenames):
                yield os.path.join(rel, name)


def digest(root):
    h = hashlib.sha256()
    for rel in sorted(files(root)):
        h.update(rel.encode() + b"\0")
        with open(os.path.join(root, rel), "rb") as f:
            h.update(hashlib.sha256(f.read()).digest())
    return h.hexdigest()


def main(argv):
    root = os.environ.get("TOUR_ROOT") or os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    stamp = os.path.join(root, "site", "tour", "stamp.json")
    if argv[1:] == ["write"]:
        with open(stamp, "w") as f:
            json.dump({"inputs": digest(root)}, f, indent=2)
            f.write("\n")
        return 0
    if argv[1:] == ["check"]:
        try:
            with open(stamp) as f:
                recorded = json.load(f).get("inputs")
        except (OSError, ValueError):
            recorded = None
        if recorded != digest(root):
            print("the tour is out of date: run make tour and commit site/tour", file=sys.stderr)
            return 1
        return 0
    print("usage: tour-stamp.py write|check", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
