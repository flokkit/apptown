"""Tests for scripts/release-prepare.sh: the previous release's upgrade
fixture comes from its GitHub Release only when it matches the digest GitHub
records, and lands under internal/apptown/testdata/upgrade/<version>. gh is
a stub: the release list, the asset digest and the download."""
import hashlib
import io
import os
import shutil
import subprocess
import tarfile
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
NOTES = "The release.\n\n## HTTP API changes\n\nNone.\n\n## Deprecations\n\nNone.\n"
GH = """#!/bin/sh
case "$1 $2" in
"release list") echo v1.0.0 ;;
"api repos/flokkit/apptown/releases/tags/v1.0.0") cat "$STUB/digest" ;;
"release download") while [ $# -gt 0 ]; do [ "$1" = -D ] && cp "$STUB/upgrade-fixture-1.0.0.tar.gz" "$2/"; shift; done ;;
*) echo "unexpected gh $*" >&2; exit 2 ;;
esac
"""


def git(cwd, *args):
    subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True,
                   env={**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t",
                        "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"})


class ReleasePrepare(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.root)
        self.repo, self.stub, self.bin = (os.path.join(self.root, d) for d in ("repo", "stub", "bin"))
        for d in ("repo/scripts", "repo/releases", "repo/stock/docs/api", "repo/internal/apptown/testdata/upgrade", "stub", "bin"):
            os.makedirs(os.path.join(self.root, d))
        for s in ("release-prepare.sh", "release-notes-check.sh"):
            shutil.copy(os.path.join(HERE, s), os.path.join(self.repo, "scripts"))
        with open(os.path.join(self.repo, "stock/docs/api/openapi.json"), "w") as f:
            f.write("{}\n")
        with open(os.path.join(self.repo, "releases/v1.0.1.md"), "w") as f:
            f.write(NOTES)
        git(self.repo, "init", "-q")
        git(self.repo, "add", "-A")
        git(self.repo, "commit", "-q", "-m", "seed")
        git(self.repo, "tag", "-a", "v1.0.0", "-m", "previous")
        with open(os.path.join(self.bin, "gh"), "w") as f:
            f.write(GH)
        os.chmod(os.path.join(self.bin, "gh"), 0o755)
        buf = io.BytesIO()
        with tarfile.open(fileobj=buf, mode="w:gz") as t:
            body = b'{"reads": []}\n'
            info = tarfile.TarInfo("1.0.0/reads.json")
            info.size = len(body)
            t.addfile(info, io.BytesIO(body))
        self.tarball = buf.getvalue()
        with open(os.path.join(self.stub, "upgrade-fixture-1.0.0.tar.gz"), "wb") as f:
            f.write(self.tarball)

    def prepare(self, digest):
        with open(os.path.join(self.stub, "digest"), "w") as f:
            f.write(digest + "\n")
        env = {**os.environ, "PATH": self.bin + os.pathsep + os.environ["PATH"], "STUB": self.stub}
        return subprocess.run(["sh", "scripts/release-prepare.sh", "1.0.1"], cwd=self.repo, capture_output=True, text=True, env=env)

    def fixture(self):
        return os.path.join(self.repo, "internal/apptown/testdata/upgrade/1.0.0/reads.json")

    def test_the_previous_fixture_lands_when_its_digest_matches(self):
        r = self.prepare("sha256:" + hashlib.sha256(self.tarball).hexdigest())
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertTrue(os.path.exists(self.fixture()))

    def test_a_fixture_whose_digest_differs_is_refused(self):
        r = self.prepare("sha256:" + "0" * 64)
        self.assertEqual(r.returncode, 1)
        self.assertIn("GitHub records", r.stderr)
        self.assertFalse(os.path.exists(self.fixture()))


if __name__ == "__main__":
    unittest.main()
