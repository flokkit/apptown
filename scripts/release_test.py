"""release.sh builds only from a clean checkout at exactly its tag: an
untagged commit and a dirty tree are refused before anything is built."""
import os
import sys
import shutil
import subprocess
import tarfile
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))


def git(cwd, *args):
    subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True,
                   env={**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t",
                        "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"})


class ReleaseRefuses(unittest.TestCase):
    def setUp(self):
        self.repo = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.repo)
        os.makedirs(os.path.join(self.repo, "scripts"))
        shutil.copy(os.path.join(HERE, "release.sh"), os.path.join(self.repo, "scripts"))
        git(self.repo, "init", "-q")
        git(self.repo, "add", "-A")
        git(self.repo, "commit", "-q", "-m", "seed")

    def release(self):
        return subprocess.run(["sh", "scripts/release.sh", "1.0.0"], cwd=self.repo, capture_output=True, text=True)

    def test_an_untagged_commit_is_refused(self):
        r = self.release()
        self.assertEqual(r.returncode, 1)
        self.assertIn("is not the tag v1.0.0", r.stderr)
        self.assertFalse(os.path.exists(os.path.join(self.repo, "dist")))

    def test_a_dirty_tree_is_refused_even_at_the_tag(self):
        git(self.repo, "tag", "-a", "v1.0.0", "-m", "one")
        with open(os.path.join(self.repo, "scripts", "release.sh"), "a") as f:
            f.write("\n# local edit\n")
        r = self.release()
        self.assertEqual(r.returncode, 1)
        self.assertIn("the checkout has changes", r.stderr)
        self.assertFalse(os.path.exists(os.path.join(self.repo, "dist")))


GO_STUB = r"""#!/bin/sh
# go build ... -o PATH ...: make the file; go run scripts/release-facts.go: the facts.
if [ "$1" = run ]; then printf 'WORKER_PROTOCOL=1-1\nCLI_PROTOCOL=1\nAGENT_IMAGE=forge-agent:000000000000\n'; exit 0; fi
while [ $# -gt 0 ]; do [ "$1" = -o ] && { mkdir -p "$(dirname "$2")"; echo binary > "$2"; }; shift; done
"""


NOTES = "The first release.\n\n## HTTP API changes\n\nNone.\n\n## Deprecations\n\nNone.\n"


class ReleaseChecksums(unittest.TestCase):
    """A release reports success only with a checksum for every archive."""

    def setUp(self):
        self.repo = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.repo)
        for d in ["scripts", "web", "deploy/forge-agent", "deploy/systemd", "bin"]:
            os.makedirs(os.path.join(self.repo, d))
        shutil.copy(os.path.join(HERE, "release.sh"), os.path.join(self.repo, "scripts"))
        shutil.copy(os.path.join(HERE, "third-party-notices.py"), os.path.join(self.repo, "scripts"))
        shutil.copy(os.path.join(HERE, "tour-stamp.py"), os.path.join(self.repo, "scripts"))
        os.makedirs(os.path.join(self.repo, "site", "tour"))
        os.makedirs(os.path.join(self.repo, "releases"))
        os.makedirs(os.path.join(self.repo, "stock", "docs", "api"))
        for name, body in [("deploy/forge-agent/Dockerfile", "FROM scratch\n"), ("deploy/systemd/forged.service", "[Unit]\n"), (".gitignore", "/dist/\n/bin/\n/calls.log\n"), ("LICENSE", "Apache License\n"), ("NOTICE", "Flokkit Forge\n"),
                           ("releases/v1.0.0.md", NOTES), ("stock/docs/api/openapi.json", "{}\n")]:
            with open(os.path.join(self.repo, name), "w") as f:
                f.write(body)
        self.bin = os.path.join(self.repo, "bin")
        self.stub("go", GO_STUB)
        self.calls = os.path.join(self.repo, "calls.log")
        self.stub("npm", f"#!/bin/sh\necho npm \"$@\" >> {self.calls}\n")
        self.stub("make", f"#!/bin/sh\necho make \"$@\" >> {self.calls}\n")
        subprocess.run([sys.executable, "scripts/tour-stamp.py", "write"], cwd=self.repo, check=True)
        git(self.repo, "init", "-q")
        git(self.repo, "add", "-A")
        git(self.repo, "commit", "-q", "-m", "seed")
        git(self.repo, "tag", "-a", "v1.0.0", "-m", "one")

    def stub(self, name, body):
        path = os.path.join(self.bin, name)
        with open(path, "w") as f:
            f.write(body)
        os.chmod(path, 0o755)

    def release(self):
        env = {**os.environ, "PATH": self.bin + os.pathsep + os.environ["PATH"]}
        return subprocess.run(["sh", "scripts/release.sh", "1.0.0"], cwd=self.repo, capture_output=True, text=True, env=env)

    def test_every_archive_has_its_checksum(self):
        r = self.release()
        self.assertEqual(r.returncode, 0, r.stderr)
        out = os.path.join(self.repo, "dist", "release", "v1.0.0")
        archives = sorted(n for n in os.listdir(out) if n.endswith(".tar.gz"))
        self.assertEqual(len(archives), 3)
        with open(os.path.join(out, "SHA256SUMS")) as f:
            named = sorted(line.split()[-1] for line in f.read().splitlines())
        self.assertEqual(named, archives)
        # Apache-2.0 travels with every copy: each archive holds the licence,
        # the notice and the third-party notices.
        for a in archives:
            with tarfile.open(os.path.join(out, a)) as t:
                held = {os.path.basename(m.name) for m in t.getmembers()}
            self.assertTrue({"LICENSE", "NOTICE", "THIRD_PARTY_NOTICES"} <= held, a)

    def test_a_failing_checksum_tool_fails_the_release(self):
        self.stub("sha256sum", "#!/bin/sh\nexit 1\n")
        self.stub("shasum", "#!/bin/sh\nexit 127\n")
        r = self.release()
        self.assertNotEqual(r.returncode, 0)
        self.assertNotIn("release v1.0.0:", r.stdout)

    def test_the_upgrade_test_runs_after_the_web_packages_install(self):
        # make upgrade-test builds web/dist, which Go embeds: the packages it
        # builds from are installed first, in a fresh checkout too.
        r = self.release()
        self.assertEqual(r.returncode, 0, r.stderr)
        with open(self.calls) as f:
            calls = f.read().splitlines()
        self.assertLess(calls.index("npm ci --silent"), calls.index("make upgrade-test"))

    def retag(self, notes):
        """Commit these notes (None removes them) and move the tag onto them."""
        path = os.path.join(self.repo, "releases", "v1.0.0.md")
        if notes is None:
            os.remove(path)
        else:
            with open(path, "w") as f:
                f.write(notes)
        git(self.repo, "add", "-A")
        git(self.repo, "commit", "-q", "-m", "notes")
        git(self.repo, "tag", "-f", "-a", "v1.0.0", "-m", "one")

    def test_the_notes_travel_with_the_release(self):
        r = self.release()
        self.assertEqual(r.returncode, 0, r.stderr)
        with open(os.path.join(self.repo, "dist", "release", "v1.0.0", "RELEASE-NOTES.md")) as f:
            notes = f.read()
        self.assertIn("The first release.", notes)
        self.assertIn("## Deprecations", notes)
        self.assertIn("## This build", notes)

    def test_a_release_without_its_notes_builds_nothing(self):
        for notes, said in [(None, "no release notes at releases/v1.0.0.md"),
                            (NOTES.replace("## Deprecations", "## Other"), 'has no "## Deprecations" section'),
                            (NOTES.replace("## HTTP API changes", "## API"), 'has no "## HTTP API changes" section')]:
            self.retag(notes)
            r = self.release()
            self.assertEqual(r.returncode, 1, r.stderr)
            self.assertIn(said, r.stderr)
            self.assertFalse(os.path.exists(os.path.join(self.repo, "dist")))

    def test_a_changed_api_description_needs_its_http_changes_named(self):
        # v0.9.0 is the previous release; v1.0.0 changes the API description.
        git(self.repo, "tag", "-a", "v0.9.0", "-m", "before")
        with open(os.path.join(self.repo, "stock", "docs", "api", "openapi.json"), "w") as f:
            f.write('{"paths": {"/api/v1/new": {}}}\n')
        # The maintainer ran make tour after the change (the tour's own rule).
        subprocess.run([sys.executable, "scripts/tour-stamp.py", "write"], cwd=self.repo, check=True)
        self.retag(NOTES)
        r = self.release()
        self.assertEqual(r.returncode, 1, r.stderr)
        self.assertIn("the API description changed since v0.9.0", r.stderr)
        self.assertFalse(os.path.exists(os.path.join(self.repo, "dist")))
        self.retag(NOTES.replace("## HTTP API changes\n\nNone.", "## HTTP API changes\n\n- GET /api/v1/new"))
        r = self.release()
        self.assertEqual(r.returncode, 0, r.stderr)

    # A tour older than the sources it shows refuses the release before the
    # upgrade test or any build.
    def test_a_stale_tour_refuses_the_release_before_anything_is_built(self):
        with open(os.path.join(self.repo, "web", "App.tsx"), "w") as f:
            f.write("changed after make tour")
        git(self.repo, "add", "-A")
        git(self.repo, "commit", "-q", "-m", "ui change")
        git(self.repo, "tag", "-f", "-a", "v1.0.0", "-m", "one")
        r = self.release()
        self.assertNotEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("run make tour", r.stderr)
        self.assertFalse(os.path.exists(self.calls) and "upgrade-test" in open(self.calls).read())
        self.assertFalse(os.path.isdir(os.path.join(self.repo, "dist")))

    def test_a_failing_upgrade_test_fails_the_release_before_anything_is_built(self):
        self.stub("make", "#!/bin/sh\n[ \"$1\" = upgrade-test ] && exit 1\nexit 0\n")
        r = self.release()
        self.assertEqual(r.returncode, 1)
        self.assertIn("the upgrade test failed", r.stderr)
        self.assertFalse(os.path.exists(os.path.join(self.repo, "dist")))


if __name__ == "__main__":
    unittest.main()
