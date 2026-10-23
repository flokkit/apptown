"""Regressions for scripts/build-artifacts-check.py: the stock check guards this repository's tracked files."""
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
CHECK = HERE / "build-artifacts-check.py"
GUIDANCE = HERE.parent / "stock/guidance/repo/no-build-artifacts.md"


class BuildArtifactsCheck(unittest.TestCase):
    def setUp(self):
        self.repo = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.repo)
        subprocess.run(["git", "init", "-q", str(self.repo)], check=True)
        g = self.repo / "stock/guidance/repo"
        g.mkdir(parents=True)
        shutil.copy(GUIDANCE, g / "no-build-artifacts.md")
        (self.repo / "main.go").write_text("package main\n")
        self.add("stock", "main.go")

    def add(self, *paths):
        subprocess.run(["git", "-C", str(self.repo), "add", *paths], check=True)

    def run_check(self):
        return subprocess.run([sys.executable, str(CHECK), str(self.repo)], capture_output=True, text=True)

    def test_sources_alone_pass(self):
        self.assertEqual(self.run_check().returncode, 0)

    def test_a_tracked_executable_fails_and_is_named(self):
        d = self.repo / "cmd/app"
        d.mkdir(parents=True)
        (d / "app").write_bytes(b"\x7fELF\x02\x01\x01" + b"\0" * 64)
        self.add("cmd/app/app")
        r = self.run_check()
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("cmd/app/app", r.stdout)

    def test_an_untracked_build_is_not_a_finding(self):
        (self.repo / "app").write_bytes(b"\xcf\xfa\xed\xfe" + b"\0" * 64)
        self.assertEqual(self.run_check().returncode, 0)

    def test_guidance_without_the_check_is_an_error(self):
        (self.repo / "stock/guidance/repo/no-build-artifacts.md").write_text("---\nname: repo/no-build-artifacts\nchecks: []\n---\n")
        r = self.run_check()
        self.assertNotIn(r.returncode, (0, 1), r.stdout + r.stderr)

    def test_a_file_that_cannot_be_read_is_an_error_not_a_pass(self):
        # The stock script must not pass a tree it could not read: here od,
        # which reads each file's signature, fails.
        d = self.repo / "cmd/app"
        d.mkdir(parents=True)
        (d / "app").write_bytes(b"\x7fELF" + b"\0" * 64)
        self.add("cmd/app/app")
        stubs = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, stubs)
        (stubs / "od").write_text("#!/bin/sh\necho od: cannot read >&2\nexit 2\n")
        (stubs / "od").chmod(0o755)
        env = dict(os.environ, PATH=f"{stubs}:{os.environ['PATH']}")
        r = subprocess.run([sys.executable, str(CHECK), str(self.repo)], capture_output=True, text=True, env=env)
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)


if __name__ == "__main__":
    unittest.main()
