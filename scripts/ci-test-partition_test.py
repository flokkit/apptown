"""Hosted partitions execute the full original package set exactly once."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('ci-test-partition.sh').resolve()
ROOT = 'github.com/flokkit/apptown/internal/apptown'
PACKAGES = [ROOT, ROOT + '/dependency', ROOT + '/dependency/new',
            ROOT + '/instancebackup', ROOT + '/dataprotection/new',
            ROOT + '/workflow', ROOT + '/workflowexec/new', ROOT + '/workflowextra',
            'github.com/flokkit/apptown/internal/worker',
            'github.com/flokkit/apptown/internal/worker/new',
            'github.com/flokkit/apptown/internal/workers',
            'github.com/flokkit/apptown/internal/future',
            'github.com/flokkit/apptown/cmd/apptownd']


class Partition(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        stub = self.root / 'go'
        stub.write_text('''#!/usr/bin/env python3
import json, os, sys
with open(os.environ['CALLS'], 'a') as f: f.write(json.dumps(sys.argv[1:]) + '\\n')
if sys.argv[1] == 'list':
    print(os.environ['PACKAGES'])
    sys.exit(int(os.environ.get('LIST_STATUS', '0')))
sys.exit(int(os.environ.get('TEST_STATUS', '0')))
''')
        stub.chmod(0o755)
        self.env = dict(os.environ, PATH=str(self.root) + os.pathsep + os.environ['PATH'],
                        CALLS=str(self.root / 'calls'), PACKAGES='\n'.join(PACKAGES))

    def run_partition(self, tier, group, **env):
        calls = self.root / 'calls'
        calls.write_text('')
        r = subprocess.run(['sh', str(SCRIPT), tier, group], env={**self.env, **env}, capture_output=True, text=True)
        return r, [json.loads(line) for line in calls.read_text().splitlines()]

    def test_partitions_cover_every_package_once_with_original_flags(self):
        for tier, groups in [('race', ['root', 'backup', 'workflow', 'rest']),
                             ('integration', ['root', 'dependency', 'worker', 'rest'])]:
            seen = []
            for group in groups:
                r, calls = self.run_partition(tier, group)
                self.assertEqual(r.returncode, 0, r.stderr)
                self.assertEqual(len(calls), 2)
                if tier == 'race':
                    self.assertEqual(calls[0], ['list', '-race', './internal/...', './cmd/...'])
                    prefix = ['test', '-race', '-count=1', '-timeout', '30m']
                else:
                    self.assertEqual(calls[0], ['list', '-tags=integration', './internal/...'])
                    prefix = ['test', '-tags=integration', '-run', '^TestIntegration', '-count=1', '-timeout', '60m', '-v']
                self.assertEqual(calls[1][:len(prefix)], prefix)
                selected = calls[1][len(prefix):]
                self.assertTrue(selected)
                if group == 'root': self.assertEqual(selected, [ROOT])
                if group == 'rest':
                    self.assertIn(ROOT + '/workflowextra', selected)
                    self.assertIn('github.com/flokkit/apptown/internal/future', selected)
                    self.assertIn('github.com/flokkit/apptown/internal/workers', selected)
                seen.extend(selected)
            self.assertCountEqual(seen, PACKAGES)

    def test_list_and_test_failures_propagate(self):
        r, calls = self.run_partition('race', 'root', LIST_STATUS='17')
        self.assertEqual(r.returncode, 17)
        self.assertEqual(len(calls), 1)
        r, _ = self.run_partition('race', 'root', TEST_STATUS='19')
        self.assertEqual(r.returncode, 19)

    def test_unknown_or_empty_partition_never_tests(self):
        for tier, group, env in [('race', 'typo', {}), ('typo', 'rest', {}),
                                 ('race', 'root', {'PACKAGES': ''})]:
            r, calls = self.run_partition(tier, group, **env)
            self.assertNotEqual(r.returncode, 0)
            self.assertFalse(any(c[0] == 'test' for c in calls))


if __name__ == '__main__':
    unittest.main()
