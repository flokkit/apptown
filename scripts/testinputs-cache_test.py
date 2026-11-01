"""A test that builds and runs a binary in a child process re-runs, rather
than replaying a cached pass, when that binary's sources change: an edited
file and an added file. Real go test runs, in a temporary module that uses
internal/execution/executiontest, with its own Go cache; no Docker or database."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent


class ChildBuildInputs(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # One compilation cache for both tests; each module's test results
        # are its own, since its path differs.
        cache = tempfile.TemporaryDirectory(prefix='testinputs-gocache-')
        cls.addClassCleanup(cache.cleanup)
        cls.cache = cache.name

    def setUp(self):
        tmp = tempfile.TemporaryDirectory(prefix='testinputs-cache-')
        self.addCleanup(tmp.cleanup)
        base = Path(tmp.name)
        self.module = base / 'module'
        (self.module / 'cmd/fixture').mkdir(parents=True)
        (self.module / 'go.mod').write_text(
            'module github.com/flokkit/apptown/probe\n\ngo 1.27.3\n\n'
            'require github.com/flokkit/apptown v0.0.0\n'
            f'replace github.com/flokkit/apptown => {ROOT}\n')
        (self.module / 'go.sum').write_text('')
        (self.module / 'cmd/fixture/main.go').write_text('package main\n\nfunc main() {}\n')
        (self.module / 'probe_test.go').write_text('''package probe

import (
	"os/exec"
	"testing"

	"github.com/flokkit/apptown/internal/execution/executiontest"
)

func TestBuildAndRun(t *testing.T) {
	executiontest.GoBuild(t, ".", nil, "./cmd/fixture")
	binary := t.TempDir() + "/fixture"
	if out, err := exec.Command("go", "build", "-o", binary, "./cmd/fixture").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	if out, err := exec.Command(binary).CombinedOutput(); err != nil {
		t.Fatalf("run: %v: %s", err, out)
	}
}
''')
        self.env = dict(os.environ, GOMAXPROCS='4', GOFLAGS='-mod=mod', GOCACHE=self.cache)
        r = self.go('mod', 'tidy')
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.stamp()
        r = self.go('test', '-p', '2', '.')
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        r = self.go('test', '-p', '2', '.')
        self.assertIn('(cached)', r.stdout)

    def go(self, *args):
        return subprocess.run(['go', *args], cwd=self.module, env=self.env, capture_output=True, text=True)

    def stamp(self):
        r = subprocess.run(['python3', str(ROOT / 'scripts/ci-content-mtimes.py'), str(self.module)], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)

    def assert_reruns_and_fails(self):
        self.stamp()
        r = self.go('test', '-p', '2', '.')
        self.assertNotIn('(cached)', r.stdout, 'a changed child build replayed a cached pass')
        self.assertNotEqual(r.returncode, 0, r.stdout)
        self.assertIn('child source executed', r.stdout)

    def test_an_added_source_file_reruns_the_test(self):
        (self.module / 'cmd/fixture/added.go').write_text('package main\n\nfunc init() { panic("child source executed") }\n')
        self.assert_reruns_and_fails()

    def test_an_edited_source_file_reruns_the_test(self):
        (self.module / 'cmd/fixture/main.go').write_text('package main\n\nfunc main() { panic("child source executed") }\n')
        self.assert_reruns_and_fails()


if __name__ == '__main__':
    unittest.main()
