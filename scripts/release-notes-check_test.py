"""Tests for scripts/release-notes-check.sh: a version's notes need both
sections the compatibility guide promises, and an HTTP change named when
the API description changed since the previous release tag."""
import os
import shutil
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
NOTES = "The release.\n\n## HTTP API changes\n\n{http}\n\n## Deprecations\n\nNone.\n"


def git(cwd, *args):
    subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True,
                   env={**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t",
                        "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"})


class ReleaseNotesCheck(unittest.TestCase):
    def setUp(self):
        self.repo = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.repo)
        for d in ("scripts", "releases", "stock/docs/api"):
            os.makedirs(os.path.join(self.repo, d))
        shutil.copy(os.path.join(HERE, "release-notes-check.sh"), os.path.join(self.repo, "scripts"))
        self.write("stock/docs/api/openapi.json", "{}\n")
        git(self.repo, "init", "-q")
        git(self.repo, "add", "-A")
        git(self.repo, "commit", "-q", "-m", "seed")
        git(self.repo, "tag", "-a", "v1.0.0", "-m", "previous")

    def write(self, name, body):
        with open(os.path.join(self.repo, name), "w") as f:
            f.write(body)

    def check(self, notes=None, api_changed=False):
        if notes is not None:
            self.write("releases/v1.0.1.md", notes)
        if api_changed:
            self.write("stock/docs/api/openapi.json", '{"paths": {}}\n')
        git(self.repo, "add", "-A")
        git(self.repo, "commit", "-q", "--allow-empty", "-m", "candidate")
        return subprocess.run(["sh", "scripts/release-notes-check.sh", "1.0.1"], cwd=self.repo, capture_output=True, text=True)

    def test_complete_notes_pass(self):
        r = self.check(NOTES.format(http="None."))
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_missing_notes_or_a_missing_section_are_refused(self):
        self.assertIn("no releases/v1.0.1.md", self.check().stderr)
        r = self.check("The release.\n\n## HTTP API changes\n\nNone.\n")
        self.assertEqual(r.returncode, 1)
        self.assertIn('"## Deprecations"', r.stderr)

    def test_an_api_change_needs_a_named_http_change(self):
        r = self.check(NOTES.format(http="None."), api_changed=True)
        self.assertEqual(r.returncode, 1)
        self.assertIn("the API description changed since v1.0.0", r.stderr)
        r = self.check(NOTES.format(http="- `GET /api/v1/things` is new."))
        self.assertEqual(r.returncode, 0, r.stderr)


if __name__ == "__main__":
    unittest.main()
