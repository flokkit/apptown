"""Tests for scripts/heavy-evidence.py: a release's images need a heavy-main.yml
run (fresh execution, never a queue run that may reuse cached results) on the
commit that passed on its first attempt. gh is a stub that serves
recorded API responses by path from STUB_DIR and fails for any other path.
"""

import json
import os
import subprocess
import sys
import tempfile
import unittest

from heavy_execution import EXECUTION_JOBS

SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "heavy-evidence.py")
REPO = "flokkit/apptown"
SHA = "0123456789abcdef0123456789abcdef01234567"

GH_STUB = """#!/bin/sh
# gh api <path>: the recorded response for <path>, or a 404.
[ "$1" = api ] || exit 2
f="$STUB_DIR/$(printf '%s' "$2" | tr '/?&=' '____')"
[ -f "$f" ] || { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }
cat "$f"
"""


def run(id, event="push", attempt=1, conclusion="success", status="completed"):
    return {"id": id, "event": event, "status": status, "conclusion": conclusion,
            "run_attempt": attempt, "head_sha": SHA,
            "created_at": f"2026-10-25T00:{id:02d}:00Z",
            "head_branch": "gh-readonly-queue/main/pr-94-abc" if event == "merge_group" else "main",
            "html_url": f"https://github.com/{REPO}/actions/runs/{id}"}


class HeavyEvidence(unittest.TestCase):
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

    def record(self, path, body):
        with open(os.path.join(self.stub, path.translate(str.maketrans("/?&=", "____"))), "w") as f:
            json.dump(body, f)

    def runs(self, *runs):
        self.record(f"repos/{REPO}/actions/workflows/heavy-main.yml/runs?head_sha={SHA}&per_page=100",
                    {"total_count": len(runs), "workflow_runs": list(runs)})

        for run in runs:
            for attempt in {1, run['run_attempt']}:
                self.jobs(run['id'], attempt)

    def jobs(self, id, attempt=1, jobs=None, total=None):
        if jobs is None:
            jobs = [{'name': name, 'status': 'completed', 'conclusion': 'success'} for name in EXECUTION_JOBS]
        self.record(f"repos/{REPO}/actions/runs/{id}/attempts/{attempt}/jobs?per_page=100",
                    {'total_count': len(jobs) if total is None else total, 'jobs': jobs})

    def first_attempt(self, id, conclusion):
        self.record(f"repos/{REPO}/actions/runs/{id}/attempts/1",
                    {"id": id, "run_attempt": 1, "status": "completed", "conclusion": conclusion})

    def check(self):
        env = dict(os.environ, PATH=self.bin + os.pathsep + os.environ["PATH"], STUB_DIR=self.stub)
        r = subprocess.run([sys.executable, SCRIPT, REPO, SHA], capture_output=True, text=True, env=env)
        return r.returncode, r.stdout + r.stderr

    def test_a_first_attempt_pass_on_main_is_evidence(self):
        self.runs(run(1))
        code, out = self.check()
        self.assertEqual(code, 0, out)
        self.assertIn("passed first time: https://github.com/flokkit/apptown/actions/runs/1", out)

    def test_a_pass_only_on_a_rerun_is_not_evidence(self):
        self.runs(run(2, attempt=2))
        self.first_attempt(2, "failure")
        code, out = self.check()
        self.assertEqual(code, 1, out)
        self.assertIn("attempt 1 failure; passed only on re-run 2", out)
        self.assertIn("::error::newest heavy-main.yml run decides", out)

    def test_a_rerun_of_a_first_attempt_pass_is_still_evidence(self):
        self.runs(run(3, attempt=2))
        self.first_attempt(3, "success")
        code, out = self.check()
        self.assertEqual(code, 0, out)

    def test_a_new_run_by_hand_that_passes_first_time_is_evidence_beside_a_red_one(self):
        # Main's run failed and was left red; another failed and passed on a
        # re-run. Both are listed beside the new run that counts.
        self.runs(run(5, event="workflow_dispatch"), run(4, attempt=2), run(3, conclusion="failure"))
        self.first_attempt(4, "failure")
        code, out = self.check()
        self.assertEqual(code, 0, out)
        self.assertIn("passed first time: https://github.com/flokkit/apptown/actions/runs/5", out)
        self.assertIn("not evidence: https://github.com/flokkit/apptown/actions/runs/4", out)
        self.assertIn("not evidence: https://github.com/flokkit/apptown/actions/runs/3: failure", out)

    def test_a_red_run_and_an_unfinished_run_are_not_evidence(self):
        self.runs(run(6, conclusion="failure"), run(7, status="in_progress", conclusion=None))
        code, out = self.check()
        self.assertEqual(code, 1, out)

    def test_a_pull_request_run_is_not_evidence(self):
        self.runs(run(8, event="pull_request"))
        code, out = self.check()
        self.assertEqual(code, 1, out)
        self.assertIn("a pull_request run", out)

    def test_main_push_on_exact_commit_is_evidence(self):
        self.runs(run(10))
        code, out = self.check()
        self.assertEqual(code, 0, out)

    def test_a_queue_run_is_not_evidence(self):
        # A queue run may reuse cached results: it is not full execution.
        self.runs(run(10, event="merge_group"))
        code, out = self.check()
        self.assertEqual(code, 1, out)
        self.assertIn("a merge_group run", out)

    def test_rerun_does_not_erase_first_failure(self):
        self.runs(run(10, attempt=2))
        self.first_attempt(10, "failure")
        code, out = self.check()
        self.assertEqual(code, 1, out)

    def test_newer_failure_or_pending_run_outvotes_older_success(self):
        for status, conclusion in [("completed", "failure"), ("in_progress", None)]:
            with self.subTest(status=status):
                self.runs(run(11, event="workflow_dispatch", status=status, conclusion=conclusion), run(10))
                code, out = self.check()
                self.assertEqual(code, 1, out)
                self.assertIn("newest heavy-main.yml run decides", out)

    def test_wrong_sha_is_never_evidence(self):
        wrong = dict(run(10), head_sha="f" * 40)
        self.runs(wrong)
        code, out = self.check()
        self.assertEqual(code, 1, out)
        self.assertIn("different commit", out)

    def test_other_branch_push_or_queue_is_not_evidence(self):
        for event, branch in [("push", "feature"), ("merge_group", "gh-readonly-queue/other/pr-94")]:
            with self.subTest(event=event):
                self.runs(dict(run(10, event=event), head_branch=branch))
                code, out = self.check()
                self.assertEqual(code, 1, out)

    def test_release_refuses_reuse_partial_duplicate_and_truncated_execution(self):
        full = [{'name': name, 'status': 'completed', 'conclusion': 'success'} for name in EXECUTION_JOBS]
        for jobs, total in [(full[:-1], None), (full + [full[0]], None), (full, len(full) + 1),
                            ([dict(job, conclusion='skipped') if job['name'] != 'heavy-ok' else job for job in full], None)]:
            with self.subTest(jobs=jobs, total=total):
                self.runs(run(10))
                self.jobs(10, jobs=jobs, total=total)
                code, out = self.check()
                self.assertEqual(code, 1, out)
                self.assertIn('lacks full successful execution', out)

    def test_both_first_and_latest_rerun_require_complete_execution(self):
        for incomplete in [1, 2]:
            self.runs(run(10, event='workflow_dispatch', attempt=2))
            self.first_attempt(10, 'success')
            self.jobs(10, incomplete, jobs=[])
            code, out = self.check()
            self.assertEqual(code, 1, out)

    def test_truncated_runs_refuse_even_when_visible_run_passed(self):
        self.runs(run(10))
        self.record(f"repos/{REPO}/actions/workflows/heavy-main.yml/runs?head_sha={SHA}&per_page=100",
                    {'total_count': 101, 'workflow_runs': [run(10)]})
        self.assertEqual(self.check()[0], 1)

    def test_no_run_is_not_evidence(self):
        self.runs()
        code, out = self.check()
        self.assertEqual(code, 1, out)

    def test_a_failed_lookup_is_not_evidence(self):
        code, out = self.check()
        self.assertEqual(code, 1, out)
        self.assertIn("::error::cannot read", out)

    def test_a_failed_attempt_lookup_is_not_evidence(self):
        self.runs(run(1), run(9, attempt=2))
        code, out = self.check()
        self.assertEqual(code, 1, out)
        self.assertIn("::error::cannot read", out)


if __name__ == "__main__":
    unittest.main()
