#!/usr/bin/env python3
"""The candidate run a release tag publishes (images.yml, candidate).

    python3 scripts/candidate-evidence.py <repository> <commit> <tag>

A release publishes the bytes its candidate built (candidate.yml): the run
on the tag candidate/<tag>/<first 12 characters of the commit>, on exactly
<commit>, whose latest attempt succeeded. The newest such run decides: an
older success cannot hide a newer failure or a run still going. It prints
that run's id, and nothing else, on stdout; anything else is an error on
stderr and a non-zero exit, and nothing is published.

The pre-push hook asks the same question before a v* tag leaves the
machine. The API is read with `gh api` (GH_TOKEN). A lookup that fails is
not evidence.
"""

import json
import subprocess
import sys


def api(path):
    r = subprocess.run(["gh", "api", path], capture_output=True, text=True, timeout=60)
    if r.returncode != 0:
        raise RuntimeError(f"gh api {path} failed ({r.returncode}): {r.stderr.strip()}")
    return json.loads(r.stdout)


def main(argv):
    if len(argv) != 4:
        print("usage: candidate-evidence.py <repository> <commit> <tag>", file=sys.stderr)
        return 2
    repo, sha, tag = argv[1:]
    branch = f"candidate/{tag}/{sha[:12]}"
    try:
        page = api(f"repos/{repo}/actions/workflows/candidate.yml/runs?head_sha={sha}&event=push&per_page=100")
        runs = page["workflow_runs"]
        if page["total_count"] != len(runs):
            raise RuntimeError("incomplete candidate run listing")
        mine = [r for r in runs if r["head_sha"] == sha and r["head_branch"] == branch]
    except (RuntimeError, KeyError, ValueError, TypeError, subprocess.TimeoutExpired) as e:
        print(f"::error::cannot read candidate.yml's runs on {sha}: {e}", file=sys.stderr)
        return 1
    if not mine:
        print(f"::error::no candidate run on {sha} for {tag}: push the tag {branch} and let it finish first", file=sys.stderr)
        return 1
    newest = max(mine, key=lambda r: (r["created_at"], r["id"]))
    if newest["status"] != "completed" or newest["conclusion"] != "success":
        state = newest["status"] if newest["status"] != "completed" else newest["conclusion"]
        print(f"::error::the newest candidate run for {tag} on {sha} is {state}: {newest['html_url']}", file=sys.stderr)
        return 1
    print(newest["id"])
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
