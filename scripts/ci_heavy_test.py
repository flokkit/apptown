"""Tests for scripts/ci-heavy.sh: with CI_HEAVY_REQUIRE set, a job passes only
when go test's verbose output shows each named test passing; the pull step
pulls every listed image, retrying, and fails naming one it could not pull.
docker is a stub that prints a given go test output and exits with a given
status, and fails the pulls it is told to.
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
	[ -z "${STUB_CALLS:-}" ] || printf '%s\n' "$*" >>"$STUB_CALLS"
	printf '%s' "$STUB_OUTPUT"
	exit "${STUB_STATUS:-0}" ;;
pull) printf '%s\n' "$*" >>"$STUB_PULLED"
	case " ${STUB_PULL_FAIL:-} " in *" $3 "*) exit 1 ;; esac
	if [ -n "${STUB_FAIL_ONCE:-}" ] && [ -f "$STUB_FAIL_ONCE" ]; then rm "$STUB_FAIL_ONCE"; exit 1; fi ;;
esac
exit 0
"""

LIST = os.path.join(os.path.dirname(os.path.dirname(SCRIPT)), "deploy/ci-heavy/test-images.txt")


def listed():
    with open(LIST) as f:
        return [l.split("#")[0].strip() for l in f if l.split("#")[0].strip()]


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

    def run_job(self, output, status=0, require="TestA TestB", mode="test", **stub):
        self.job += 1
        job = f"citest{os.getpid()}-{self.job}"
        # The stub's cleanup removes nothing; the job's folder is the test's to remove.
        self.addCleanup(shutil.rmtree, f"/tmp/apptown-ci-{job}", ignore_errors=True)
        self.pulled = os.path.join(self.dir.name, f"pulled-{self.job}")
        open(self.pulled, "w").close()
        env = {**os.environ, "PATH": self.path, "CI_HEAVY_ID": job,
               "STUB_OUTPUT": output, "STUB_STATUS": str(status),
               "STUB_PULLED": self.pulled, "CI_HEAVY_PULL_PAUSE": "0", **stub}
        if require is not None:
            env["CI_HEAVY_REQUIRE"] = require
        env["STUB_CALLS"] = os.path.join(self.dir.name, "calls")
        args = ["test", "./..."] if mode == "test" else [mode]
        return subprocess.run(["sh", SCRIPT] + args, env=env, capture_output=True, text=True,
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

    # The release's tour job: make tour runs with the checkout writable (it
    # writes web/dist and site/tour), and a failed tour fails the job.
    def test_the_tour_runs_make_tour_writable_and_keeps_its_status(self):
        r = self.run_job("", status=2, require=None, mode="tour")
        self.assertEqual(r.returncode, 2, r.stderr)
        r = self.run_job("", status=0, require=None, mode="tour")
        self.assertEqual(r.returncode, 0, r.stderr)
        with open(os.path.join(self.dir.name, "calls")) as f:
            call = f.read()
        self.assertIn("make tour", call)
        root = os.path.dirname(os.path.dirname(SCRIPT))
        self.assertIn(f"-v {root}:{root} ", call)


    def pulls(self):
        with open(self.pulled) as f:
            return [line.split()[-1] for line in f.read().splitlines()]

    # The pull step: every listed image, before the tests.
    def test_the_pull_step_pulls_every_listed_image(self):
        r = self.run_job("", require=None, mode="pull")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.pulls(), listed())

    def test_a_pull_that_keeps_failing_fails_the_step_by_name(self):
        image = listed()[0]
        r = self.run_job("", require=None, mode="pull", STUB_PULL_FAIL=image)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn(f"could not pull {image}", r.stdout)
        self.assertEqual(self.pulls().count(image), 3)
        # The rest are still pulled, so one run names every image that failed.
        self.assertEqual(set(self.pulls()), set(listed()))

    def test_a_pull_that_fails_once_is_retried(self):
        image = listed()[0]
        # The stub fails the first pull it is asked for, once.
        marker = os.path.join(self.dir.name, "fail-once")
        open(marker, "w").close()
        r = self.run_job("", require=None, mode="pull", STUB_FAIL_ONCE=marker)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(self.pulls().count(image), 2)


if __name__ == "__main__":
    unittest.main()
