"""Tests for scripts/ci-heavy.sh's required-tests check: with CI_HEAVY_REQUIRE
set, a job passes only when go test's verbose output shows each named test
passing. docker is a stub that prints a given go test output and exits with
a given status.
"""

import os
import shutil
import subprocess
import tempfile
import unittest

SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "ci-heavy.sh")

DOCKER_STUB = """#!/bin/sh
case "$1" in
run) case "$*" in *busybox*) exit 0 ;; esac
	printf '%s' "$STUB_OUTPUT"
	exit "${STUB_STATUS:-0}" ;;
esac
exit 0
"""


class RequiredTests(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)
        bindir = os.path.join(self.dir.name, "bin")
        os.mkdir(bindir)
        with open(os.path.join(bindir, "docker"), "w") as f:
            f.write(DOCKER_STUB)
        os.chmod(os.path.join(bindir, "docker"), 0o755)
        self.path = bindir + os.pathsep + os.environ["PATH"]
        self.job = 0

    def run_job(self, output, status=0, require="TestA TestB"):
        self.job += 1
        job = f"citest{os.getpid()}-{self.job}"
        # The stub's cleanup removes nothing; the job's folder is the test's to remove.
        self.addCleanup(shutil.rmtree, f"/tmp/apptown-ci-{job}", ignore_errors=True)
        env = {**os.environ, "PATH": self.path, "CI_HEAVY_ID": job,
               "STUB_OUTPUT": output, "STUB_STATUS": str(status)}
        if require is not None:
            env["CI_HEAVY_REQUIRE"] = require
        return subprocess.run(["sh", SCRIPT, "test", "./..."], env=env, capture_output=True, text=True,
                              cwd=os.path.dirname(os.path.dirname(SCRIPT)))

    def test_every_required_test_passing_passes(self):
        r = self.run_job("--- PASS: TestA (0.01s)\n    --- PASS: TestB (1.20s)\nok\n")
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_a_skipped_required_test_fails_the_job_by_name(self):
        r = self.run_job("--- PASS: TestA (0.01s)\n--- SKIP: TestB (0.00s)\nok\n")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("did not pass", r.stderr)
        self.assertIn("TestB", r.stderr)

    def test_no_tests_to_run_fails_the_job(self):
        r = self.run_job("testing: warning: no tests to run\nok\n")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("TestA TestB", r.stderr)

    def test_a_longer_name_does_not_stand_in_for_a_required_one(self):
        r = self.run_job("--- PASS: TestAB (0.01s)\n--- PASS: TestB (0.01s)\n")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("TestA", r.stderr)

    def test_go_tests_own_failure_status_is_kept(self):
        r = self.run_job("--- FAIL: TestA (0.01s)\nFAIL\n", status=3, require=None)
        self.assertEqual(r.returncode, 3)
        r = self.run_job("--- PASS: TestA (0.01s)\n--- FAIL: TestB (0.01s)\nFAIL\n", status=1)
        self.assertEqual(r.returncode, 1)


if __name__ == "__main__":
    unittest.main()
