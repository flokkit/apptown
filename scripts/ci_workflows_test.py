"""CI reuses main's unit results before merge; heavy runs fresh after merge.

The heavy matrix covers every release execution job, and the checks jobs run
every checker stage once.
"""
from pathlib import Path
import re
import subprocess
import unittest

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent
WORKFLOWS = ROOT / '.github/workflows'


def text(name):
    return (WORKFLOWS / name).read_text()


def job(workflow, name):
    """The text of one top-level job of a workflow."""
    match = re.search(rf'^  {re.escape(name)}:\n(.*?)(?=^  [\w-]+:\n|\Z)', workflow, re.M | re.S)
    assert match, name
    return match.group(1)


def save_guards(section):
    """The if: of every step that saves the Go compilation and test cache."""
    guards = []
    for step in re.split(r'\n      - ', section):
        if 'ci-cache-trim.py' in step or ('cache/save@' in step and 'path: ~/.cache/go-build' in step):
            guards.append(re.search(r'if: (.*)', step).group(1))
    return guards


class TestCache(unittest.TestCase):
    def test_heavy_runs_only_after_merge_or_by_hand(self):
        self.assertFalse((WORKFLOWS / 'heavy.yml').exists())
        main = text('heavy-main.yml')
        self.assertIn('  push:\n    branches: [main]\n  workflow_dispatch:\n', main)
        self.assertNotIn('merge_group', main)
        self.assertNotIn('pull_request', main)
        self.assertIn('cancel-in-progress: false', main)
        self.assertNotIn('qualification', main)
        self.assertIn('  merge_group:\n    types: [checks_requested]\n', text('ci.yml'))

    def test_heavy_matrix_covers_every_release_execution_job(self):
        main = text('heavy-main.yml')
        matrix = re.findall(r'^          - name: (\S+)\n            args: (.*)$', main, re.M)
        names = [n for n, _ in matrix]
        for group in ['root-a', 'root-b', 'root-c', 'dependency', 'worker', 'rest']:
            self.assertIn(('integration-' + group, 'test-partition integration ' + group), matrix)
        self.assertEqual(len(names), len(set(names)))
        commands = dict(matrix)
        self.assertIn('-tags=journey', commands['journeys'])
        self.assertIn('-run TestJourney', commands['journeys'])
        self.assertIn('-tags=browser,pi,inference_docker', commands['opt-in-tiers'])
        for name in ['TestBrowserSecurityChromium', 'TestInferencePinnedPi', 'TestInferenceConsoleMountBoundary']:
            self.assertIn(name, commands['opt-in-tiers'])
        self.assertIn('require: TestBrowserSecurityChromium TestInferencePinnedPi TestInferenceConsoleMountBoundary', main)
        self.assertIn('group: [root, backup, workflow, rest]', main)
        from heavy_execution import EXECUTION_JOBS
        self.assertEqual(EXECUTION_JOBS, set(names) | {'web', 'heavy-ok'} | {'race-' + g for g in ['root', 'backup', 'workflow', 'rest']})
        self.assertEqual(set(re.findall(r'^  ([\w-]+):\n', main.split('jobs:\n', 1)[1], re.M)),
                         {'web', 'tier', 'race', 'heavy-ok'})
        for name in ['web', 'tier', 'race']:
            self.assertNotRegex(job(main, name), r'(?m)^    if:', name)
        aggregate = job(main, 'heavy-ok')
        self.assertIn('if: always()', aggregate)
        self.assertIn('needs: [web, tier, race]', aggregate)
        # Execute the workflow's own aggregate: cancelled or skipped execution
        # must fail release evidence just as a failed execution does.
        script = aggregate.split('run: |\n', 1)[1]
        for results in ['success success success', 'success skipped success',
                        'success failure success', 'success cancelled success']:
            r = subprocess.run(['sh', '-c', script], env={'RESULTS': results}, capture_output=True)
            self.assertEqual(r.returncode == 0, results == 'success success success', results)

    def test_main_executes_fresh_and_alone_saves(self):
        main, ci = text('heavy-main.yml'), text('ci.yml')
        # heavy-main: on main and by hand only; every test job fresh.
        self.assertIn('  push:\n    branches: [main]\n  workflow_dispatch:\n', main)
        self.assertNotIn('merge_group', main)
        self.assertNotIn('pull_request', main)
        self.assertIn('CI_TEST_RESULTS: fresh', job(main, 'tier'))
        self.assertIn('ci-test-environment.sh fresh', job(main, 'race'))
        self.assertNotRegex(main, r'CI_TEST_RESULTS: (?!fresh)|ci-test-environment.sh (?!fresh)')
        for name in ['tier', 'race']:
            guards = save_guards(job(main, name))
            self.assertEqual(guards, ["github.ref == 'refs/heads/main'"] * 2, name)
        # ci.yml: a push (to main: its only push) runs fresh; everything else
        # reuses; only a push saves, so a dispatch on main never does.
        test = job(ci, 'test')
        self.assertIn("ci-test-environment.sh ${{ github.event_name == 'push' && 'fresh' || 'reuse' }}", test)
        self.assertIn('  push:\n    branches: [main]\n', ci)
        for name in ['test', 'checks']:
            guards = save_guards(job(ci, name))
            self.assertTrue(guards, name)
            for guard in guards:
                self.assertTrue(guard.startswith("github.event_name == 'push' && github.ref == 'refs/heads/main'"), guard)

    def test_test_jobs_normalise_times_and_name_their_environment_before_testing(self):
        for workflow, jobs in [('ci.yml', ['test']), ('heavy-main.yml', ['tier', 'race'])]:
            for name in jobs:
                section = job(text(workflow), name)
                mtimes = section.index('python3 scripts/ci-content-mtimes.py .')
                self.assertLess(section.index('name: web-dist'), mtimes, (workflow, name))
                self.assertLess(section.index('sh scripts/ci-heavy.sh pull'), section.index('go test') if 'go test' in section else len(section))
                self.assertLess(mtimes, section.index('sh scripts/ci-heavy.sh pull'), (workflow, name))
                if name == 'tier':
                    self.assertIn("CI_HEAVY_ID: ${{ matrix.name }}", section)
                else:
                    self.assertLess(section.index('sh scripts/ci-heavy.sh pull'), section.index('ci-test-environment.sh'), (workflow, name))
                    self.assertLess(section.index('ci-test-environment.sh'), section.index('ci-test-partition.sh'), (workflow, name))

    def test_archive_budgets_fit_the_repository_cache(self):
        # GitHub keeps 10 GB of caches per repository. One main commit's Go
        # archives, compressed at about 4:1 as observed, must leave room for
        # the next commit's beside them.
        total = 0
        for workflow in ['ci.yml', 'heavy-main.yml']:
            t = text(workflow)
            for name in re.findall(r'^  ([\w-]+):\n', t, re.M):
                section = job(t, name)
                budgets = [int(m) for m in re.findall(r'ci-cache-trim.py ~/.cache/go-build --max-mib (\d+)', section)]
                if not budgets:
                    continue
                entries = len(re.findall(r'^          - name: ', section, re.M)) or len(re.findall(r'^          - group: ', section, re.M))
                if 'group: [root, backup, workflow, rest]' in section:
                    entries = 4
                if 'part: [root-a, root-b, subsystems, rest]' in section:
                    entries = 4
                total += max(budgets) * max(entries, 1)
        self.assertGreater(total, 0)
        self.assertLessEqual(total / 4 / 1024, 5, f'{total} MiB of archives per main commit')
        self.assertIn('--max-mib 1024', job(text('heavy-main.yml'), 'tier'))
        self.assertIn('--max-mib 1024', job(text('heavy-main.yml'), 'race'))

    def test_no_workflow_disables_the_cache_by_hand(self):
        for workflow in ['ci.yml', 'heavy-main.yml']:
            self.assertNotIn('-count=1', text(workflow), workflow)

    def test_the_checks_jobs_run_every_checker_stage_once(self):
        ci = text('ci.yml')
        groups = re.findall(r'^            stages: (.*)$', job(ci, 'checks'), re.M)
        stages = [s for g in groups for s in g.split()]
        makefile = (ROOT / 'Makefile').read_text()
        gate = re.search(r'^GATE_GO_STAGES = (.*?)(?<!\\)\n', makefile, re.M | re.S).group(1).replace('\\\n', ' ').split()
        # The tests are the test jobs'; the docs and public checks have jobs of
        # their own (web, public); govulncheck is CI's addition.
        expected = set(gate) - {'check-test', 'check-test-root-a', 'check-test-root-b', 'check-docs', 'check-public'} | {'check-vuln'}
        self.assertEqual(sorted(stages), sorted(expected))
        self.assertEqual(len(stages), len(set(stages)))
        self.assertIn('make check-docs', job(ci, 'web'))
        self.assertIn('make check-public', job(ci, 'public'))
        self.assertEqual(job(ci, 'checks').count('make check-gate'), 1)


if __name__ == '__main__': unittest.main()
