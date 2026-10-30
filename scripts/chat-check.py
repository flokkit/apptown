#!/usr/bin/env python3
"""A chat on a running AppTown receives a message and answers it.

    APPTOWN_TOKEN=<CLI token> python3 scripts/chat-check.py <url> <project> [<seconds>]

Through the public HTTP API: open a change on <project> with no run
(POST /api/v1/projects/{slug}/changes), since only a change's chat runs on a
worker, open its chat there (POST /api/v1/projects/{slug}/console, changeId
and placement "worker"), send it one
message (POST /api/v1/console/{sid}/say), and wait up to <seconds> (300) for
the helper's answer (GET /api/v1/console/{sid}/messages): a helper message
with text after ours. A helper message marked as an error, the chat's
runtime gone, or no answer in time fails it. The chat is ended and the
change abandoned either way (DELETE /api/v1/console/{sid}, POST
/api/v1/changes/{id}/abandon). It needs the worker, a model and the agent
image to work together, which is what the release check is for.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

MESSAGE = "This is AppTown's release check. Reply with the single word: ready"


def call(base, method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base.rstrip("/") + path, data=data, method=method)
    req.add_header("Authorization", "Bearer " + os.environ["APPTOWN_TOKEN"])
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            raw = r.read()
            return json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        raise SystemExit(f"chat check: {method} {path}: HTTP {e.code}: {e.read()[:300].decode(errors='replace')}")


def main(argv):
    if len(argv) not in (3, 4):
        print(__doc__, file=sys.stderr)
        return 2
    base, slug = argv[1], argv[2]
    limit = float(argv[3]) if len(argv) == 4 else 300
    change = call(base, "POST", f"/api/v1/projects/{slug}/changes", {"title": "Release check: a chat answers", "type": "chore"})["change"]["id"]
    try:
        return converse(base, slug, change, limit)
    finally:
        call(base, "POST", f"/api/v1/changes/{change}/abandon", {"reason": "the release check's chat is done"})


def converse(base, slug, change, limit):
    opened = call(base, "POST", f"/api/v1/projects/{slug}/console", {"changeId": change, "placement": "worker"})
    sid = opened["session"]["id"]
    print(f"chat check: chat {sid} opened on the worker, on change {change}")
    try:
        call(base, "POST", f"/api/v1/console/{sid}/say", {"text": MESSAGE})
        start = time.monotonic()
        while time.monotonic() - start < limit:
            got = call(base, "GET", f"/api/v1/console/{sid}/messages")
            messages = got.get("messages") or []
            ours = [m["i"] for m in messages if m.get("role") == "you" and m.get("text") == MESSAGE]
            answers = [m for m in messages if ours and m.get("role") == "helper" and m["i"] > ours[-1] and (m.get("text") or m.get("error"))]
            if answers:
                if answers[0].get("error"):
                    print(f"chat check: the helper's turn ended in an error: {answers[0].get('text')}", file=sys.stderr)
                    return 1
                print(f"chat check: answered in {time.monotonic() - start:.0f}s: {answers[0]['text'][:80]!r}")
                return 0
            if got.get("state") in ("dead", "unavailable"):
                print(f"chat check: the chat is {got['state']}", file=sys.stderr)
                return 1
            time.sleep(3)
        print(f"chat check: no answer within {limit:.0f}s", file=sys.stderr)
        return 1
    finally:
        call(base, "DELETE", f"/api/v1/console/{sid}")


if __name__ == "__main__":
    sys.exit(main(sys.argv))
