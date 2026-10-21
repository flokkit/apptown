"""Regressions for scripts/public-check.py: a tree that would publish private
material, a home-lab name or a secret is refused; a clean tree passes."""
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

CHECK = Path(__file__).with_name("public-check.py")
HOME = "home.example"  # stands in for a real home-lab domain
# Assembled at run time, so this file itself holds no string a secret
# scanner matches; the repository's own public check scans it too.
FAKE_KEY = "AKIA" + "QYLPMN5HHHFPZAM2"
FAKE_SECRET = "aws_secret_access_key = " + "wJalrXUtnFEMI" + "/K7MDENG/bPxRfiCYEXAMPLEKEY9x"


class PublicCheck(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp)
        self.names = self.tmp / "names.txt"
        hashes = subprocess.run([sys.executable, str(CHECK), "--hash", HOME, "10.9.8.7", "lab-box"],
                                capture_output=True, text=True, check=True).stdout
        self.names.write_text("# test names\n" + hashes)

    def repo(self, files):
        root = self.tmp / "repo"
        if root.exists():
            shutil.rmtree(root)
        root.mkdir()
        for rel, text in files.items():
            p = root / rel
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text(text)
        git = ["git", "-C", str(root), "-c", "user.name=t", "-c", "user.email=t@example.com"]
        subprocess.run(git[:3] + ["init", "-q"], check=True)
        subprocess.run(git + ["add", "-f", "-A"], check=True)
        subprocess.run(git + ["commit", "-q", "-m", "t"], check=True)
        return root

    def run_check(self, files, env=None):
        root = self.repo(files)
        r = subprocess.run([sys.executable, str(CHECK), "--root", str(root), "--names", str(self.names)],
                           capture_output=True, text=True, env={**os.environ, **(env or {})})
        return r.returncode, r.stdout + r.stderr

    CLEAN = {"README.md": "# App\nSee https://example.com and 192.168.1.20.\n", "main.go": "package main\n"}

    def test_a_clean_tree_passes(self):
        code, out = self.run_check(self.CLEAN)
        self.assertEqual(code, 0, out)

    def test_private_paths_and_the_maintainers_root_files_are_refused(self):
        for rel in ["private/design/plans/x.md", ".gitlab-ci.yml", "notes.txt", "presso-1.txt"]:
            with self.subTest(rel):
                code, out = self.run_check({**self.CLEAN, rel: "x\n"})
                self.assertEqual(code, 1, out)
                self.assertIn(f"{rel}: private material", out)

    def test_a_home_lab_name_its_subdomains_and_addresses_are_refused(self):
        for text in [f"host {HOME}\n", f"https://git.{HOME.upper()}/x.git\n", "ssh root@10.9.8.7\n", "on lab-box.\n"]:
            with self.subTest(text):
                code, out = self.run_check({**self.CLEAN, "docs/a.md": text})
                self.assertEqual(code, 1, out)
                self.assertIn("docs/a.md: names a maintainers' home-lab host", out)
                self.assertNotIn(HOME, out.lower())

    def test_a_name_that_only_contains_a_home_name_passes(self):
        code, out = self.run_check({**self.CLEAN, "a.md": "notahome.example.org mylab-box 110.9.8.7\n"})
        self.assertEqual(code, 0, out)

    def test_a_secret_is_refused(self):
        code, out = self.run_check({**self.CLEAN, "config.ini": f"[aws]\naws_access_key_id = {FAKE_KEY}\n{FAKE_SECRET}\n"})
        self.assertEqual(code, 1, out)
        self.assertIn("gitleaks found something", out)
        self.assertNotIn(FAKE_KEY, out)

    def test_without_gitleaks_nothing_passes(self):
        code, out = self.run_check(self.CLEAN, env={"GITLEAKS": "no-such-gitleaks"})
        self.assertEqual(code, 1, out)
        self.assertIn("not scanned for secrets", out)

    def test_it_reads_the_commit_not_the_working_tree(self):
        root = self.repo(self.CLEAN)
        (root / "private").mkdir()
        (root / "private" / "plan.md").write_text(f"on {HOME}\n")
        r = subprocess.run([sys.executable, str(CHECK), "--root", str(root), "--names", str(self.names)],
                           capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)


if __name__ == "__main__":
    unittest.main()
