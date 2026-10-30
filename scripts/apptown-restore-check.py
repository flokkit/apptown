#!/usr/bin/env python3
"""Check a restored AppTown against what its source held at the backup.

    apptown-restore-check.py capture <source url> <file>     # before the backup
    apptown-restore-check.py verify  <target url> <file>     # after the restore

APPTOWN_TOKEN is a CLI token made on the source before the backup: on the target
it must still authenticate, which is the check that credentials came across.
`capture` reads only. `verify` reads only and exits non-zero naming every
difference.
"""
import json
import os
import sys
import urllib.error
import urllib.request

# Fields of a change's page that are computed at read time from the live
# state of the folder, the run or the clock, not recorded. Stored reports and
# usage are records and are compared.
VOLATILE = {"editing", "next", "stateDetail", "diffstat", "committedDiff"}


def get(base, path, token=None):
    req = urllib.request.Request(base.rstrip("/") + path)
    req.add_header("Authorization", "Bearer " + (token if token is not None else os.environ["APPTOWN_TOKEN"]))
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, json.load(r)
    except urllib.error.HTTPError as e:
        return e.code, None


def must(base, path):
    status, body = get(base, path)
    if status != 200:
        sys.exit(f"{path}: HTTP {status}")
    return body


def projects(base):
    body = must(base, "/api/v1/projects")
    return sorted(p["slug"] for p in (body if isinstance(body, list) else body["projects"]))


def kept(before, after):
    """What the source recorded is still there, unchanged. A newer AppTown
    may answer with fields the source's version did not have (an upgrade
    adds them); those are not differences."""
    if isinstance(before, dict) and isinstance(after, dict):
        return all(kept(v, after.get(k)) for k, v in before.items())
    if isinstance(before, list) and isinstance(after, list):
        return len(before) == len(after) and all(kept(b, a) for b, a in zip(before, after))
    return before == after


def recorded_change(base, cid):
    body = must(base, f"/api/v1/changes/{cid}")
    out = {k: v for k, v in body.items() if k not in VOLATILE}
    # Read-time words inside recorded sections: a commit's age ("24 hours
    # ago") and a release's current observation of its deployment.
    for c in out.get("commits") or []:
        c.pop("when", None)
    for r in (out.get("evidence") or {}).get("releases") or []:
        for live in ("observed", "observedAt", "observationStale", "serving", "reason"):  # the reason explains the observation
            r.pop(live, None)
    return out


def capture(base, out):
    snap = {"projects": projects(base), "changes": {}, "mains": {}, "audit": must(base, "/api/v1/audit?limit=1")}
    for slug in snap["projects"]:
        main = must(base, f"/api/v1/projects/{slug}").get("main") or {}
        snap["mains"][slug] = (main.get("recorded") or {}).get("sha")
    # Every shipped change in the history AppTown keeps on its activity
    # page, plus the newest changes of each project: enough to cover runs,
    # steps, evidence and approvals of real work without reading everything.
    ids = set()
    for item in must(base, "/api/v1/activity").get("history", []):
        ids.add(item["changeId"])
    for slug in snap["projects"]:
        status, body = get(base, f"/api/v1/projects/{slug}/changes")
        if status == 200:
            rows = body if isinstance(body, list) else body.get("changes", [])
            ids.update(c["id"] for c in rows[:3])
    for cid in sorted(ids):
        snap["changes"][str(cid)] = recorded_change(base, cid)
    with open(out, "w") as f:
        json.dump(snap, f, indent=1, sort_keys=True)
    print(f"captured {len(snap['projects'])} projects, {len(snap['changes'])} changes, newest audit entry {snap['audit'][0]['id']}")


def verify(base, path):
    snap = json.load(open(path))
    problems = []
    if get(base, "/api/v1/projects", token="apptown-restore-check-not-a-token")[0] not in (401, 403):
        problems.append("a token that never existed was not refused")
    status, _ = get(base, "/api/v1/projects")
    if status != 200:
        sys.exit(f"the source's CLI token does not authenticate on the target: HTTP {status}")
    have = projects(base)
    if have != snap["projects"]:
        problems.append(f"projects differ: missing {sorted(set(snap['projects']) - set(have))}, extra {sorted(set(have) - set(snap['projects']))}")
    newest = snap["audit"][0]
    status, entries = get(base, "/api/v1/audit?limit=200")
    if status != 200 or not any(e["id"] == newest["id"] and e["kind"] == newest["kind"] for e in entries):
        problems.append(f"the audit log lacks its newest entry before the backup ({newest['id']} {newest['kind']})")
    for cid, before in snap["changes"].items():
        after = recorded_change(base, cid)
        for key in sorted(before):
            if not kept(before[key], after.get(key)):
                problems.append(f"change {cid}: {key} differs")
    # Each repository holds the main the source recorded. A project whose
    # source had no record is named as unverified, never counted as proof.
    proved, unverified = 0, []
    for slug in have:
        expected = snap["mains"].get(slug)
        if not expected:
            unverified.append(slug)
            continue
        main = must(base, f"/api/v1/projects/{slug}").get("main") or {}
        if (main.get("recorded") or {}).get("sha") != expected:
            problems.append(f"{slug}: the record of main is {(main.get('recorded') or {}).get('sha')}, the source's was {expected}")
        elif main.get("hub") != expected:
            problems.append(f"{slug}: the repository's main is {main.get('hub') or 'unreadable'}, not the recorded {expected}")
        else:
            proved += 1
    print(f"repositories: {proved} hold their recorded main; unverified (no record on the source): {', '.join(unverified) or 'none'}")
    for p in problems:
        print("DIFFERS:", p)
    print(f"checked {len(have)} projects and {len(snap['changes'])} changes: {len(problems)} differences")
    sys.exit(1 if problems else 0)


if __name__ == "__main__":
    cmd, args = sys.argv[1], sys.argv[2:]
    {"capture": capture, "verify": verify}[cmd](*args)
