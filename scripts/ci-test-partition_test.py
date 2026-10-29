"""CI's partitions run every package of a tier once, from Go's test cache,
except the listed tests, which run once and uncached."""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('ci-test-partition.sh').resolve()
REPO = SCRIPT.parent.parent
UNCACHED = Path(__file__).with_name('ci-uncached-tests.txt')
M = 'github.com/flokkit/apptown'
ROOT = M + '/internal/apptown'
PACKAGES = [ROOT, ROOT + '/dependency', ROOT + '/dependency/new',
            ROOT + '/instancebackup', ROOT + '/dataprotection/new',
            ROOT + '/workflow', ROOT + '/workflowexec/new', ROOT + '/workflowextra',
            ROOT + '/verification', ROOT + '/instanceupdate/supervisor',
            M + '/internal/worker', M + '/internal/worker/new', M + '/internal/workers',
            M + '/internal/future', M + '/cmd/apptownd']
GROUPS = {'unit': ['root-a', 'root-b', 'subsystems', 'rest'],
          'race': ['root', 'backup', 'workflow', 'rest'],
          'integration': ['root-a', 'root-b', 'root-c', 'dependency', 'worker', 'rest']}
SPLIT = {'unit': ['root-a', 'root-b'], 'race': [], 'integration': ['root-a', 'root-b', 'root-c']}


def uncached_lines():
    lines = []
    for line in UNCACHED.read_text().splitlines():
        line = line.split('#')[0].split()
        if line:
            lines.append(tuple(line))
    return lines


def flag(call, name):
    return call[call.index(name) + 1] if name in call else None


def go_regex(pattern):
    # Go's RE2 syntax as these patterns use it is Python's too.
    return re.compile(pattern)


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
if '-count=1' in sys.argv: sys.exit(int(os.environ.get('UNCACHED_STATUS', '0')))
sys.exit(int(os.environ.get('TEST_STATUS', '0')))
''')
        stub.chmod(0o755)
        self.env = dict(os.environ, PATH=str(self.root) + os.pathsep + os.environ['PATH'],
                        CALLS=str(self.root / 'calls'), PACKAGES='\n'.join(PACKAGES))

    def run_partition(self, tier, group, **env):
        calls = self.root / 'calls'
        calls.write_text('')
        r = subprocess.run(['sh', str(SCRIPT), tier, group], env={**self.env, **env}, capture_output=True, text=True, cwd=REPO)
        return r, [json.loads(line) for line in calls.read_text().splitlines()]

    def go_tests(self, calls):
        return [c for c in calls if c[0] == 'test']

    def packages(self, call):
        return [a for a in call if a.startswith(M)]

    def test_every_package_runs_once_per_tier_and_the_root_once_per_split_group(self):
        for tier, groups in GROUPS.items():
            seen = []
            for group in groups:
                r, calls = self.run_partition(tier, group)
                self.assertEqual(r.returncode, 0, r.stderr)
                cached = self.go_tests(calls)[0]
                selected = self.packages(cached)
                self.assertTrue(selected, (tier, group))
                if group in SPLIT[tier] or (tier == 'race' and group == 'root'):
                    self.assertEqual(selected, [ROOT], (tier, group))
                seen.extend(p for p in selected if p != ROOT)
            self.assertCountEqual(seen, [p for p in PACKAGES if p != ROOT], tier)

    def test_results_are_reused_except_for_the_listed_tests(self):
        for tier, groups in GROUPS.items():
            for group in groups:
                _, calls = self.run_partition(tier, group)
                tests = self.go_tests(calls)
                self.assertNotIn('-count=1', tests[0], (tier, group))
                for call in tests[1:]:
                    self.assertIn('-count=1', call)
                if tier == 'race':
                    self.assertEqual(tests[0][:4], ['test', '-race', '-timeout', '30m'])
                if tier == 'integration':
                    self.assertEqual(tests[0][:5], ['test', '-tags=integration', '-timeout', '60m', '-v'])

    def test_every_listed_test_is_skipped_by_the_cached_run_and_runs_once_uncached(self):
        listed = uncached_lines()
        self.assertTrue(listed)
        ran = []
        for tier, groups in GROUPS.items():
            for group in groups:
                _, calls = self.run_partition(tier, group)
                tests = self.go_tests(calls)
                skip = flag(tests[0], '-skip')
                for line_tier, package, name in listed:
                    if line_tier == tier and package in self.packages(tests[0]):
                        self.assertTrue(skip and go_regex(skip).search(name), (tier, group, name))
                for call in tests[1:]:
                    run = go_regex(flag(call, '-run'))
                    for line_tier, package, name in listed:
                        if line_tier == tier and package in self.packages(call) and run.search(name):
                            ran.append((tier, package, name))
                    self.assertEqual(call[:2], tests[0][:2])
        in_partition = [l for l in listed if l[1] in PACKAGES]
        self.assertTrue(in_partition)
        self.assertCountEqual(ran, in_partition)

    def test_the_listed_tests_exist(self):
        for tier, package, name in uncached_lines():
            self.assertIn(tier, GROUPS)
            directory = REPO / package.removeprefix(M + '/')
            sources = ''.join(p.read_text() for p in directory.glob('*_test.go'))
            self.assertIn(f'func {name}(t *testing.T)', sources, f'{package} {name}')

    def test_the_root_package_splits_select_each_test_once(self):
        # The real names, so a renamed test cannot fall out of every group.
        names = set()
        for path in (REPO / 'internal/apptown').glob('*_test.go'):
            names.update(re.findall(r'^func (Test\w+)\(t \*testing\.T\)', path.read_text(), re.M))
        integration = {n for n in names if n.startswith('TestIntegration')}
        self.assertGreater(len(integration), 20)
        for tier, pool in [('unit', names), ('integration', integration)]:
            chosen = {n: [] for n in pool}
            for group in SPLIT[tier]:
                _, calls = self.run_partition(tier, group)
                cached = self.go_tests(calls)[0]
                run, skip = flag(cached, '-run'), flag(cached, '-skip')
                for n in pool:
                    if (run is None or go_regex(run).search(n)) and (skip is None or not go_regex(skip).search(n)):
                        chosen[n].append(group)
            for n, groups in chosen.items():
                self.assertEqual(len(groups), 1, f'{tier} {n}: {groups}')
            # Each named group selects something real.
            for group in SPLIT[tier]:
                self.assertTrue(any(group in g for g in chosen.values()), (tier, group))

    def test_the_unit_split_is_the_makefiles(self):
        makefile = (REPO / 'Makefile').read_text()
        root_tests_a = re.search(r'^ROOT_TESTS_A = (.*)$', makefile, re.M).group(1)
        _, calls = self.run_partition('unit', 'root-a')
        self.assertEqual(flag(self.go_tests(calls)[0], '-run'), root_tests_a)
        _, calls = self.run_partition('unit', 'root-b')
        self.assertEqual(flag(self.go_tests(calls)[0], '-skip'), root_tests_a)

    def test_list_and_test_failures_propagate_and_both_runs_happen(self):
        r, calls = self.run_partition('race', 'root', LIST_STATUS='17')
        self.assertEqual(r.returncode, 17)
        self.assertEqual(len(calls), 1)
        r, _ = self.run_partition('race', 'root', TEST_STATUS='19')
        self.assertEqual(r.returncode, 19)
        r, calls = self.run_partition('integration', 'rest', TEST_STATUS='19')
        self.assertEqual(r.returncode, 19)
        self.assertEqual(len(self.go_tests(calls)), 2)
        r, calls = self.run_partition('integration', 'rest', UNCACHED_STATUS='23')
        self.assertEqual(r.returncode, 23)

    def test_list_only_never_tests(self):
        calls_file = self.root / 'calls'
        calls_file.write_text('')
        r = subprocess.run(['sh', str(SCRIPT), 'race', 'root', 'list'], env=self.env, capture_output=True, text=True, cwd=REPO)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout, ROOT + '\n')
        self.assertFalse(any(json.loads(l)[0] == 'test' for l in calls_file.read_text().splitlines()))

    def test_unknown_or_empty_partition_never_tests(self):
        for tier, group, env in [('race', 'typo', {}), ('typo', 'rest', {}), ('integration', 'root', {}),
                                 ('race', 'root', {'PACKAGES': ''})]:
            r, calls = self.run_partition(tier, group, **env)
            self.assertNotEqual(r.returncode, 0)
            self.assertFalse(any(c[0] == 'test' for c in calls))


if __name__ == '__main__':
    unittest.main()
