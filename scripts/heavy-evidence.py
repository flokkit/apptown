#!/usr/bin/env python3
"""The heavy evidence for a release tag (images.yml, heavy-evidence).

    python3 scripts/heavy-evidence.py <repository> <commit>

Images and archives are built only for a commit whose heavy-main.yml run
passed on its first attempt (design/operations/github.md). That workflow runs
every heavy test fresh, reusing no stored result; heavy.yml's queue runs may
reuse Go's cached results, so they qualify a merge but are not release
evidence. A run counts when it is on <commit>, ran on a push to main or by
hand, its latest conclusion is success, and its first attempt's conclusion is
success too. A run that failed and then passed on a
re-run does not count: a re-run of failed jobs keeps the green jobs' results
and replaces the failure inside one run. A new run started by hand on the
same commit is a complete run of its own. The newest applicable run decides;
an older success cannot hide a newer failure or unfinished run. Each run must
name the exact commit and have complete execution jobs on both the first and
latest attempt; admission and reuse are not release evidence.

The API is read with `gh api` (GH_TOKEN). A lookup that fails is not
evidence: this exits non-zero.
"""

import json
import subprocess
import sys

from heavy_execution import complete_page

EVENTS = ("push", "workflow_dispatch")
WORKFLOW = "heavy-main.yml"


def api(path):
    r = subprocess.run(["gh", "api", path], capture_output=True, text=True, timeout=60)
    if r.returncode != 0:
        raise RuntimeError(f"gh api {path} failed ({r.returncode}): {r.stderr.strip()}")
    return json.loads(r.stdout)


def main(argv):
    if len(argv) != 3:
        print("usage: heavy-evidence.py <repository> <commit>", file=sys.stderr)
        return 2
    repo, sha = argv[1], argv[2]
    try:
        page = api(f"repos/{repo}/actions/workflows/{WORKFLOW}/runs?head_sha={sha}&per_page=100")
        runs = page["workflow_runs"]
        if page["total_count"] != len(runs):
            raise RuntimeError("incomplete heavy run listing")
        passed, refused = [], []
        passed_ids, applicable = set(), []
        for run in runs:
            url = run["html_url"]
            if run["head_sha"] != sha:
                raise RuntimeError(f"run {run['id']} names a different commit: {run['head_sha']}")
            eligible = run["event"] in EVENTS and (
                run["event"] == "workflow_dispatch"
                or run["event"] == "push" and run["head_branch"] == "main"
            )
            if eligible:
                applicable.append(run)
            if not eligible:
                refused.append(f"{url}: a {run['event']} run, not a push to main or a run by hand")
            elif run["status"] != "completed":
                refused.append(f"{url}: {run['status']}")
            elif run["conclusion"] != "success":
                refused.append(f"{url}: {run['conclusion']} (attempt {run['run_attempt']})")
            elif run["run_attempt"] == 1:
                passed.append(url)
                passed_ids.add(run["id"])
            else:
                first = api(f"repos/{repo}/actions/runs/{run['id']}/attempts/1")["conclusion"]
                if first == "success":
                    passed.append(f"{url} (passed on attempt 1, and on re-run {run['run_attempt']})")
                    passed_ids.add(run["id"])
                else:
                    refused.append(f"{url}: attempt 1 {first}; passed only on re-run {run['run_attempt']}")
        # A green admission/reuse aggregate is not release execution evidence.
        for run in applicable:
            if run['id'] not in passed_ids:
                continue
            attempts = {1, run['run_attempt']}
            if not all(complete_page(api(f"repos/{repo}/actions/runs/{run['id']}/attempts/{attempt}/jobs?per_page=100"))
                       for attempt in sorted(attempts)):
                passed_ids.remove(run['id'])
                refused.append(f"{run['html_url']}: lacks full successful execution jobs")
        passed = [message for message in passed if any(
            run['id'] in passed_ids and (message == run['html_url'] or message.startswith(run['html_url'] + ' (')) for run in applicable)]
    except (RuntimeError, KeyError, ValueError, TypeError, subprocess.TimeoutExpired) as e:
        print(f"::error::cannot read heavy-main.yml's runs on {sha}: {e}")
        return 1
    for r in refused:
        print(f"not evidence: {r}")
    for p in passed:
        print(f"passed first time: {p}")
    latest = max(applicable, key=lambda r: (r["created_at"], r["id"]), default=None)
    if latest is not None and latest["id"] not in passed_ids:
        print(f"::error::newest heavy-main.yml run decides: {latest['html_url']} did not pass on its first attempt")
        return 1
    if not passed:
        print(f"::error::no heavy-main.yml run on {sha} passed on its first attempt; a re-run that passed does not count. "
              "Fix the cause in a new commit, or start a new run by hand on this commit (Actions, heavy-main, Run workflow), "
              "then re-run this workflow")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
