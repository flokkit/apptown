"""Regressions for scripts/npm-scripts-check.py: AppTown's own npm installs run no install script."""
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

CHECK = Path(__file__).with_name("npm-scripts-check.py")


def lock(*with_script):
    packages = {"": {"name": "web"}, "node_modules/react": {"version": "18.3.1"}}
    for name in with_script:
        packages["node_modules/" + name] = {"version": "1.0.0", "hasInstallScript": True}
    return json.dumps({"lockfileVersion": 3, "packages": packages})


class NpmScriptsCheckTest(unittest.TestCase):
    def setUp(self):
        self.tree = tempfile.TemporaryDirectory(prefix="apptown-npm-scripts-check-")
        self.addCleanup(self.tree.cleanup)
        self.root = Path(self.tree.name)
        self.write("web/.npmrc", "# why\nignore-scripts=true\n")
        self.write("web/install-scripts.txt", "# header\nesbuild  finds its binary at run time\n")
        self.write("web/package-lock.json", lock("esbuild"))

    def write(self, rel, text):
        p = self.root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)

    def run_check(self):
        return subprocess.run([sys.executable, str(CHECK), str(self.root)], capture_output=True, text=True)

    def assertFails(self, *want):
        out = self.run_check()
        self.assertEqual(out.returncode, 1, out.stdout + out.stderr)
        for w in want:
            self.assertIn(w, out.stdout)

    def test_listed_scripts_with_scripts_off_pass(self):
        out = self.run_check()
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)

    # Kills: the setting removed or turned off, so npm ci runs every script.
    def test_scripts_left_on_fails(self):
        self.write("web/.npmrc", "ignore-scripts=false\n")
        self.assertFails("web/.npmrc: does not set ignore-scripts=true")
        (self.root / "web/.npmrc").unlink()
        self.assertFails("web/.npmrc: does not set ignore-scripts=true")

    # Kills: a new dependency with an install script arriving unnoticed,
    # including one nested under another package.
    def test_an_unlisted_package_with_a_script_fails(self):
        self.write("web/package-lock.json", lock("esbuild", "vite/node_modules/fsevents"))
        self.assertFails("web/package-lock.json: fsevents has an install script")

    # Kills: a line kept for a package that no longer has a script.
    def test_a_stale_line_fails(self):
        self.write("web/package-lock.json", lock())
        self.assertFails("web/install-scripts.txt:2: no lockfile has esbuild")

    # Seeds and fixtures belong to projects, not to AppTown's build.
    def test_seed_and_testdata_lockfiles_are_not_read(self):
        self.write("stock/seeds/app/package-lock.json", lock("sharp"))
        self.write("internal/x/testdata/app/package-lock.json", lock("sharp"))
        out = self.run_check()
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)


if __name__ == "__main__":
    unittest.main()
