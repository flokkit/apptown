"""Tests for scripts/candidate-evidence.py: a release tag publishes only a
successful candidate run on its exact commit and version, the newest such
run deciding. gh is a stub that serves recorded API responses by path."""

import json
import os
import subprocess
import tempfile
import unittest

SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "candidate-evidence.py")
REPO = "flokkit/apptown"
SHA = "0123456789abcdef0123456789abcdef01234567"
TAG = "v1.0.0-rc.12"

GH_STUB = """#!/bin/sh
[ "$1" = api ] || exit 2
f="$STUB_DIR/$(printf '%s' "$2" | tr '/?&=' '____')"
[ -f "$f" ] || { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }
cat "$f"
"""


def run(id, tag=TAG, conclusion="success", status="completed"):
    return {"id": id, "status": status, "conclusion": conclusion, "head_sha": SHA,
            "head_branch": f"candidate/{tag}/{SHA[:12]}", "created_at": f"2026-10-29T00:{id:02d}:00Z",
            "html_url": f"https://github.com/{REPO}/actions/runs/{id}"}


class CandidateEvidence(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)
        self.bin = os.path.join(self.dir.name, "bin")
        self.stub = os.path.join(self.dir.name, "api")
        os.mkdir(self.bin)
        os.mkdir(self.stub)
        gh = os.path.join(self.bin, "gh")
        with open(gh, "w") as f:
            f.write(GH_STUB)
        os.chmod(gh, 0o755)

    def runs(self, *runs):
        path = f"repos/{REPO}/actions/workflows/candidate.yml/runs?head_sha={SHA}&event=push&per_page=100"
        with open(os.path.join(self.stub, path.translate(str.maketrans("/?&=", "____"))), "w") as f:
            json.dump({"total_count": len(runs), "workflow_runs": list(runs)}, f)

    def check(self):
        env = {**os.environ, "PATH": self.bin + os.pathsep + os.environ["PATH"], "STUB_DIR": self.stub}
        return subprocess.run(["python3", SCRIPT, REPO, SHA, TAG], capture_output=True, text=True, env=env)

    def test_the_newest_successful_candidate_is_published(self):
        self.runs(run(3, conclusion="failure"), run(4))
        r = self.check()
        self.assertEqual((r.returncode, r.stdout), (0, "4\n"), r.stderr)

    def test_a_newer_failure_or_unfinished_run_is_not_hidden_by_an_older_success(self):
        for newest in (run(5, conclusion="failure"), run(5, status="in_progress", conclusion=None)):
            self.runs(run(4), newest)
            r = self.check()
            self.assertEqual((r.returncode, r.stdout), (1, ""))

    def test_a_candidate_for_another_version_is_not_evidence(self):
        self.runs(run(4, tag="v1.0.0-rc.11"))
        r = self.check()
        self.assertEqual((r.returncode, r.stdout), (1, ""))
        self.assertIn("no candidate run", r.stderr)

    def test_a_failed_lookup_is_not_evidence(self):
        r = self.check()
        self.assertEqual((r.returncode, r.stdout), (1, ""))


if __name__ == "__main__":
    unittest.main()
