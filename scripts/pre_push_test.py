"""Tests for scripts/git-hooks/pre-push: a push of release tags alone checks
their commit's evidence instead of rerunning the full gate; anything else,
a deletion or a mix included, runs the full gate, and evidence that is
missing refuses the push. make and the evidence scripts are stubs that
record how they were called."""
import os
import shutil
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
URL = "https://github.com/flokkit/apptown.git"
ZERO = "0" * 40
STUB = """#!/bin/sh
echo "{name} $*" >> "$CALLS"
exit ${{FAIL:-0}}
"""


def git(cwd, *args):
    return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True,
                          env={**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t",
                               "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"}).stdout.strip()


class PrePush(unittest.TestCase):
    def setUp(self):
        self.repo = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.repo)
        os.makedirs(os.path.join(self.repo, "scripts/git-hooks"))
        os.makedirs(os.path.join(self.repo, "bin"))
        shutil.copy(os.path.join(HERE, "git-hooks/pre-push"), os.path.join(self.repo, "scripts/git-hooks"))
        for name, path in (("heavy", "scripts/heavy-evidence.py"), ("candidate", "scripts/candidate-evidence.py")):
            with open(os.path.join(self.repo, path), "w") as f:
                f.write(f"import os, sys\nopen(os.environ['CALLS'], 'a').write('{name} ' + ' '.join(sys.argv[1:]) + '\\n')\nsys.exit(int(os.environ.get('FAIL', '0')))\n")
        with open(os.path.join(self.repo, "bin/make"), "w") as f:
            f.write(STUB.format(name="make"))
        os.chmod(os.path.join(self.repo, "bin/make"), 0o755)
        git(self.repo, "init", "-q")
        git(self.repo, "add", "-A")
        git(self.repo, "commit", "-q", "-m", "seed")
        self.commit = git(self.repo, "rev-parse", "HEAD")
        git(self.repo, "tag", "-a", "v1.0.1", "-m", "release")
        self.tag_object = git(self.repo, "rev-parse", "v1.0.1")
        self.calls = os.path.join(self.repo, "calls")

    def push(self, *lines, fail=False):
        env = {**os.environ, "PATH": os.path.join(self.repo, "bin") + os.pathsep + os.environ["PATH"], "CALLS": self.calls, "FAIL": "1" if fail else "0"}
        r = subprocess.run(["sh", "scripts/git-hooks/pre-push", "origin", URL], cwd=self.repo, input="".join(l + "\n" for l in lines),
                           capture_output=True, text=True, env=env)
        calls = open(self.calls).read().splitlines() if os.path.exists(self.calls) else []
        if os.path.exists(self.calls):
            os.remove(self.calls)
        return r.returncode, calls

    def test_a_candidate_tag_checks_heavy_evidence_on_its_commit(self):
        ref = f"refs/tags/candidate/v1.0.1/{self.commit[:12]}"
        self.assertEqual(self.push(f"{ref} {self.commit} {ref} {ZERO}"), (0, [f"heavy flokkit/apptown {self.commit}"]))

    def test_a_release_tag_checks_its_candidate_run(self):
        self.assertEqual(self.push(f"refs/tags/v1.0.1 {self.tag_object} refs/tags/v1.0.1 {ZERO}"),
                         (0, [f"candidate flokkit/apptown {self.commit} v1.0.1"]))

    def test_missing_evidence_refuses_the_push(self):
        code, _ = self.push(f"refs/tags/v1.0.1 {self.tag_object} refs/tags/v1.0.1 {ZERO}", fail=True)
        self.assertEqual(code, 1)

    def test_branches_mixes_and_deletions_run_the_full_gate(self):
        tag = f"refs/tags/v1.0.1 {self.tag_object} refs/tags/v1.0.1 {ZERO}"
        for lines in ([f"refs/heads/main {self.commit} refs/heads/main {ZERO}"],
                      [tag, f"refs/heads/main {self.commit} refs/heads/main {ZERO}"],
                      [f"(delete) {ZERO} refs/tags/v1.0.1 {self.commit}"],
                      [f"refs/tags/other {self.commit} refs/tags/other {ZERO}"]):
            self.assertEqual(self.push(*lines), (0, ["make check"]), lines)


if __name__ == "__main__":
    unittest.main()
