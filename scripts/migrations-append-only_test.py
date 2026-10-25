"""Exercise the actual gate against released locks in isolated Git repositories."""
import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest

SCRIPT = pathlib.Path(__file__).with_name("migrations-append-only.sh")
RELEASED = "baseline 147\n148 aaaa\n149 bbbb\n150 cccc\n"


class MigrationLockTest(unittest.TestCase):
    def test_unavailable_base_tree_is_not_an_absent_lock(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            bindir = root / "bin"
            bindir.mkdir()
            git = bindir / "git"
            git.write_text('#!/bin/sh\ncase "$1" in rev-parse) exit 0;; ls-tree) exit "$TREE_STATUS";; *) exit 99;; esac\n')
            git.chmod(0o755)
            for status in (0, 128):
                with self.subTest(status=status):
                    env = {**os.environ, "PATH": str(bindir) + os.pathsep + os.environ["PATH"], "TREE_STATUS": str(status)}
                    result = subprocess.run(["sh", str(SCRIPT), "fixture"], env=env, capture_output=True, text=True)
                    self.assertEqual(result.returncode == 0, status == 0, result.stdout + result.stderr)
                    self.assertEqual("has no lock yet: ok" in result.stdout, status == 0)

    def test_released_suffix_is_immutable(self):
        cases = {
            "unchanged": (RELEASED, True),
            "append": (RELEASED + "151 dddd\n", True),
            "partial collapse": ("baseline 148\n149 bbbb\n150 cccc\n", True),
            "partial and append": ("baseline 148\n149 bbbb\n150 cccc\n151 dddd\n", True),
            "full collapse": ("baseline 150\n", True),
            "full and append": ("baseline 150\n151 dddd\n", True),
            "backward": ("baseline 146\n148 aaaa\n149 bbbb\n150 cccc\n", False),
            "unreleased": ("baseline 151\n", False),
            "changed retained digest": ("baseline 148\n149 changed\n150 cccc\n", False),
            "missing retained row": ("baseline 148\n150 cccc\n", False),
            "reordered retained rows": ("baseline 148\n150 cccc\n149 bbbb\n", False),
            "changed without collapse": ("baseline 147\n148 changed\n149 bbbb\n150 cccc\n", False),
            "malformed baseline": ("baseline 148 extra\n149 bbbb\n150 cccc\n", False),
        }
        with tempfile.TemporaryDirectory() as tmp:
            repo = pathlib.Path(tmp)
            (repo / "scripts").mkdir()
            shutil.copy(SCRIPT, repo / "scripts" / SCRIPT.name)
            lock = repo / "internal/apptown/dbschema/migrations.lock"
            lock.parent.mkdir(parents=True)
            lock.write_text(RELEASED)
            def git(*args):
                subprocess.run(["git", *args], cwd=repo, check=True, capture_output=True)
            git("init", "-q")
            git("add", ".")
            git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "released")
            for name, (current, accept) in cases.items():
                with self.subTest(name=name):
                    lock.write_text(current)
                    result = subprocess.run(["sh", "scripts/migrations-append-only.sh", "HEAD"], cwd=repo, capture_output=True, text=True)
                    self.assertEqual(result.returncode == 0, accept, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
