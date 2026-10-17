"""Private-test kit for the greenfield gym (design/subsystems/greenfield-eval.md).

PRIVACY RULE: nothing from test source reaches an agent. Failures emit
SYMPTOMS: the request made, the response seen, and a one-line user-style
complaint. The runner collects JSON; the harness turns failures into
rework feedback using only the symptom fields.
"""
import json
import os
import subprocess
import sys
import threading
import urllib.request
import urllib.error
from http.server import BaseHTTPRequestHandler, HTTPServer

BASE = os.environ.get("BASE_URL", "").rstrip("/")
BIN = os.environ.get("BIN", "")

_results = []


def check(name, ok, symptom=""):
    _results.append({"name": name, "ok": bool(ok), "symptom": "" if ok else symptom})
    mark = "ok " if ok else "FAIL"
    line = f"{mark} {name}"
    if not ok and symptom:
        line += f"  · {symptom}"
    print(line, file=sys.stderr)
    return ok


def finish():
    passed = sum(1 for r in _results if r["ok"])
    print(json.dumps({"passed": passed, "failed": len(_results) - passed,
                      "checks": _results}))
    sys.exit(0 if passed == len(_results) else 1)


# ---- HTTP apps -------------------------------------------------------------

def req(method, path, body=None, expect=None, raw=False):
    """Returns (status, parsed-or-text, symptom-string)."""
    url = BASE + path
    data = None
    headers = {}
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    r = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(r, timeout=10) as resp:
            text = resp.read().decode(errors="replace")
            status = resp.status
    except urllib.error.HTTPError as e:
        text = e.read().decode(errors="replace")
        status = e.code
    except Exception as e:
        return 0, None, f"{method} {path} → no response ({e.__class__.__name__})"
    symptom = f"{method} {path} " + (json.dumps(body) if body is not None else "") + \
              f" → {status} {text[:160]}"
    if raw:
        return status, text, symptom
    try:
        parsed = json.loads(text) if text.strip() else None
    except ValueError:
        parsed = text
    return status, parsed, symptom


# ---- CLI apps --------------------------------------------------------------

def run_bin(args, stdin=""):
    """Returns (exit, stdout, stderr, symptom)."""
    try:
        p = subprocess.run([BIN] + args, input=stdin, capture_output=True,
                           text=True, timeout=30)
    except Exception as e:
        return -1, "", "", f"{os.path.basename(BIN)} {' '.join(args)} → failed to run ({e.__class__.__name__})"
    symptom = (f"{os.path.basename(BIN)} {' '.join(args)} (stdin {len(stdin)}B) → "
               f"exit {p.returncode}; stdout: {p.stdout[:200]!r}; stderr: {p.stderr[:120]!r}")
    return p.returncode, p.stdout, p.stderr, symptom


# ---- controllable clock (roomie/qkue) --------------------------------------
#
# The clock is a SEPARATE process (started by the harness before the app,
# since apps may poll CLOCK_URL at boot). Tests steer it via POST /set.

CLOCK = os.environ.get("CLOCK_URL", "http://127.0.0.1:18099")


def set_now(rfc3339):
    r = urllib.request.Request(CLOCK + "/set",
                               data=json.dumps({"now": rfc3339}).encode(),
                               headers={"Content-Type": "application/json"},
                               method="POST")
    with urllib.request.urlopen(r, timeout=5):
        pass


class _Clock(BaseHTTPRequestHandler):
    now = "2030-01-07T08:00:00Z"  # a Monday, mid-morning, far future

    def do_GET(self):
        body = json.dumps({"now": _Clock.now}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        _Clock.now = json.loads(self.rfile.read(n))["now"]
        self.send_response(204)
        self.end_headers()

    def log_message(self, *a):
        pass


def clock_main(port=18099):
    HTTPServer(("0.0.0.0", port), _Clock).serve_forever()


if __name__ == "__main__":
    clock_main(int(sys.argv[1]) if len(sys.argv) > 1 else 18099)
