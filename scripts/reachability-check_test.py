"""Tests for scripts/reachability-check.sh: a deadcode that does not finish
fails the check with its error and leaves the baseline as it was; one that
finishes is held to the baseline. Each case runs a copy of the script beside
its own baseline file, so the repository's baseline is never read or
written. DEADCODE is a stub that prints given output and exits with a given
status.
"""

import os
import shutil
import subprocess
import tempfile
import unittest

SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "reachability-check.sh")

STUB = """#!/bin/sh
printf '%s' "$STUB_OUTPUT"
printf '%s' "$STUB_ERROR" >&2
exit "${STUB_STATUS:-0}"
"""

UNREACHABLE = "internal/a/a.go:1:6: unreachable func: A\ninternal/b/b.go:2:6: unreachable func: B\n"


class ReachabilityCheck(unittest.TestCase):
    def setUp(self):
        d = tempfile.TemporaryDirectory()
        self.addCleanup(d.cleanup)
        os.mkdir(os.path.join(d.name, "scripts"))
        self.script = os.path.join(d.name, "scripts", "reachability-check.sh")
        shutil.copy(SCRIPT, self.script)
        self.baseline = os.path.join(d.name, "scripts", "reachability-baseline")
        with open(self.baseline, "w") as f:
            f.write("1\n")
        self.stub = os.path.join(d.name, "deadcode")
        with open(self.stub, "w") as f:
            f.write(STUB)
        os.chmod(self.stub, 0o755)

    def check(self, *args, output="", error="", status=0):
        env = {**os.environ, "DEADCODE": self.stub, "STUB_OUTPUT": output,
               "STUB_ERROR": error, "STUB_STATUS": str(status)}
        return subprocess.run(["sh", self.script, *args], env=env, capture_output=True, text=True)

    def recorded(self):
        with open(self.baseline) as f:
            return f.read()

    def test_a_deadcode_that_fails_fails_the_check_with_its_error(self):
        for args in ([], ["--tighten"]):
            r = self.check(*args, error="deadcode: packages contain errors\n", status=1)
            self.assertNotEqual(r.returncode, 0, r.stdout)
            self.assertIn("deadcode failed (exit 1)", r.stderr)
            self.assertIn("packages contain errors", r.stderr)
            self.assertNotIn("ok", r.stdout)
            self.assertEqual(self.recorded(), "1\n", "a failed analysis changed the baseline")

    def test_a_deadcode_that_finishes_within_the_baseline_passes(self):
        r = self.check()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("reachability unreachable 0  baseline 1  ok", r.stdout)

    def test_a_deadcode_that_finishes_above_the_baseline_fails(self):
        r = self.check(output=UNREACHABLE)
        self.assertNotEqual(r.returncode, 0, r.stdout)
        self.assertIn("reachability unreachable 2  baseline 1  WORSE", r.stdout)


if __name__ == "__main__":
    unittest.main()
