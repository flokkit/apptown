"""Cache warming compiles without executing tests; admission is not execution."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent


class Compilation(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory(); self.addCleanup(tmp.cleanup)
        self.dir = Path(tmp.name)
        stub = self.dir / 'go'
        stub.write_text('''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
with open(os.environ['CALLS'], 'a') as f: f.write(json.dumps(sys.argv[1:]) + '\\n')
if sys.argv[1] == 'list':
    print('github.com/flokkit/apptown/internal/apptown')
    print('github.com/flokkit/apptown/internal/worker')
    sys.exit(int(os.environ.get('LIST_STATUS', '0')))
if '-o' in sys.argv: Path(sys.argv[sys.argv.index('-o') + 1]).write_text('compiled')
sys.exit(int(os.environ.get('COMPILE_STATUS', '0')))
'''); stub.chmod(0o755)
        self.env = dict(os.environ, PATH=str(self.dir) + os.pathsep + os.environ['PATH'], CALLS=str(self.dir / 'calls'))

    def invoke(self, *args, **env):
        (self.dir / 'calls').write_text('')
        r = subprocess.run(['sh', str(HERE / 'ci-compile.sh'), *args], env={**self.env, **env}, capture_output=True, text=True)
        return r, [json.loads(line) for line in (self.dir / 'calls').read_text().splitlines()]

    def test_every_selected_package_compiles_with_actual_tags_and_output_is_removed(self):
        for args, flag in [(('race', 'root'), '-race'), (('integration', 'worker'), '-tags=integration'),
                           (('journeys',), '-tags=journey'), (('opt-in-tiers',), '-tags=browser,pi,inference_docker')]:
            r, calls = self.invoke(*args)
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertIn('no test binaries or TestMain execute', r.stdout)
            builds = [call for call in calls if call[0] == 'test']
            self.assertTrue(builds)
            for call in builds:
                self.assertIn('-c', call)
                self.assertIn(flag, call)
                self.assertFalse(Path(call[call.index('-o') + 1]).parent.exists())
            self.assertIn(flag, calls[0])

    def test_failures_propagate_and_temp_output_is_cleaned(self):
        r, calls = self.invoke('journeys', LIST_STATUS='7')
        self.assertEqual(r.returncode, 7)
        self.assertFalse(any(call[0] == 'test' for call in calls))
        r, calls = self.invoke('journeys', COMPILE_STATUS='9')
        self.assertEqual(r.returncode, 9)
        build = next(call for call in calls if call[0] == 'test')
        self.assertFalse(Path(build[build.index('-o') + 1]).parent.exists())

    def test_list_only_partition_does_not_execute_tests(self):
        r = subprocess.run(['sh', str(HERE / 'ci-test-partition.sh'), 'race', 'root', 'list'], env=self.env, capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout, 'github.com/flokkit/apptown/internal/apptown\n')
        calls = [json.loads(line) for line in (self.dir / 'calls').read_text().splitlines()]
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0][0], 'list')

    def test_real_testmain_is_compiled_but_never_executed(self):
        fixture = self.dir / 'fixture'; fixture.mkdir()
        (fixture / 'go.mod').write_text('module example.com/warm\n\ngo 1.23\n')
        package = fixture / 'internal/apptown'; package.mkdir(parents=True)
        (package / 'compile_test.go').write_text('''package apptown
import ("os"; "testing")
func TestMain(m *testing.M) { os.WriteFile(os.Getenv("SENTINEL"), []byte("executed"), 0600); os.Exit(m.Run()) }
func TestCompiled(t *testing.T) {}
''')
        sentinel = self.dir / 'executed'
        env = dict(os.environ, GOWORK='off', GOTOOLCHAIN='local', SENTINEL=str(sentinel))
        r = subprocess.run(['sh', str(HERE / 'ci-compile.sh'), 'journeys'], cwd=fixture, env=env, capture_output=True, text=True, timeout=120)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertFalse(sentinel.exists())


class EventBoundary(unittest.TestCase):
    def result(self, event, results, report='success'):
        return subprocess.run(['sh', str(HERE / 'heavy-result.sh')], env=dict(os.environ, GITHUB_EVENT_NAME=event, RESULTS=results, REPORT_RESULT=report), capture_output=True, text=True)

    def test_pr_only_admits_and_queue_manual_require_actual_success(self):
        r = self.result('pull_request', 'skipped skipped skipped')
        self.assertEqual(r.returncode, 0); self.assertIn('admission only', r.stdout)
        self.assertNotEqual(self.result('pull_request', 'success success success').returncode, 0)
        for event in ['merge_group', 'workflow_dispatch']:
            self.assertEqual(self.result(event, 'success success success').returncode, 0)
            for bad in ['skipped', 'failure', 'cancelled', '']:
                self.assertNotEqual(self.result(event, 'success success ' + bad).returncode, 0)
        self.assertNotEqual(self.result('push', 'success success success').returncode, 0)
        self.assertNotEqual(self.result('merge_group', 'success success success', 'failure').returncode, 0)

    def test_warm_and_execution_workflows_keep_cache_and_partition_agreement(self):
        heavy = (ROOT / '.github/workflows/heavy.yml').read_text()
        warm = (ROOT / '.github/workflows/heavy-cache.yml').read_text()
        keys = lambda text: sorted(line.strip() for line in text.splitlines() if 'key: go-build-' in line or 'restore-keys: go-build-' in line)
        self.assertEqual(keys(heavy), keys(warm))
        for group in ['root', 'dependency', 'worker', 'rest']:
            self.assertIn('args: test-partition integration ' + group, heavy)
            self.assertIn('args: compile integration ' + group, warm)
        for text in [heavy, warm]: self.assertIn('group: [root, backup, workflow, rest]', text)
        self.assertNotIn('  heavy-ok:', warm)
        self.assertNotIn('args: test ', warm)
        self.assertNotIn('args: test-partition', warm)
        self.assertNotIn('sh scripts/ci-heavy.sh pull', warm)
        self.assertIn('sh scripts/ci-compile.sh race', warm)
        self.assertIn('  merge_group:', heavy)
        self.assertNotIn('  push:', heavy)
        self.assertIn('  push:', warm)
        self.assertNotIn('  pull_request:', warm)


if __name__ == '__main__': unittest.main()
