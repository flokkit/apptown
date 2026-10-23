#!/usr/bin/env python3
"""The heavy evidence for a release tag (images.yml, heavy-evidence).

    python3 scripts/heavy-evidence.py <repository> <commit>

Images and archives are built only for a commit whose heavy.yml run passed
on its first attempt (design/operations/github.md). A run counts when it is
on <commit>, ran on a push to main or by hand (a pull request's run tested a
merge, not the commit), its latest conclusion is success, and its first
attempt's conclusion is success too. A run that failed and then passed on a
re-run does not count: a re-run of failed jobs keeps the green jobs' results
and replaces the failure inside one run. A new run started by hand on the
same commit is a complete run of its own and counts when it passes first
time; the red runs on the commit are printed beside it.

The API is read with `gh api` (GH_TOKEN). A lookup that fails is not
evidence: this exits non-zero.
"""

import json
import subprocess
import sys

EVENTS = ("push", "workflow_dispatch")


def api(path):
    r = subprocess.run(["gh", "api", path], capture_output=True, text=True)
    if r.returncode != 0:
        raise RuntimeError(f"gh api {path} failed ({r.returncode}): {r.stderr.strip()}")
    return json.loads(r.stdout)


def main(argv):
    if len(argv) != 3:
        print("usage: heavy-evidence.py <repository> <commit>", file=sys.stderr)
        return 2
    repo, sha = argv[1], argv[2]
    try:
        runs = api(f"repos/{repo}/actions/workflows/heavy.yml/runs?head_sha={sha}&per_page=100")["workflow_runs"]
        passed, refused = [], []
        for run in runs:
            url = run["html_url"]
            if run["event"] not in EVENTS:
                refused.append(f"{url}: a {run['event']} run")
            elif run["status"] != "completed":
                refused.append(f"{url}: {run['status']}")
            elif run["conclusion"] != "success":
                refused.append(f"{url}: {run['conclusion']} (attempt {run['run_attempt']})")
            elif run["run_attempt"] == 1:
                passed.append(url)
            else:
                first = api(f"repos/{repo}/actions/runs/{run['id']}/attempts/1")["conclusion"]
                if first == "success":
                    passed.append(f"{url} (passed on attempt 1, and on re-run {run['run_attempt']})")
                else:
                    refused.append(f"{url}: attempt 1 {first}; passed only on re-run {run['run_attempt']}")
    except (RuntimeError, KeyError, ValueError) as e:
        print(f"::error::cannot read heavy.yml's runs on {sha}: {e}")
        return 1
    for r in refused:
        print(f"not evidence: {r}")
    for p in passed:
        print(f"passed first time: {p}")
    if not passed:
        print(f"::error::no heavy.yml run on {sha} passed on its first attempt; a re-run that passed does not count. "
              "Fix the cause in a new commit, or start a new run by hand on this commit (Actions, heavy, Run workflow), "
              "then re-run this workflow")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
