"""Release comments identify the pinned commit; tests never contact GitHub."""
import importlib.util
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('action_pins_check', Path(__file__).with_name('action-pins-check.py'))
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)

COMMIT = 'a' * 40
OTHER = 'b' * 40


class ActionPinsTest(unittest.TestCase):
    def setUp(self):
        tree = tempfile.TemporaryDirectory()
        self.addCleanup(tree.cleanup)
        self.root = Path(tree.name)
        self.workflows = self.root / '.github/workflows'
        self.workflows.mkdir(parents=True)
        self.write('a.yml', f'- uses: owner/repo/path@{COMMIT} # v1.2.3\n')

    def write(self, name, text):
        (self.workflows / name).write_text(text)

    def test_each_distinct_repository_and_tag_is_resolved_once(self):
        self.write('b.yml', f'uses: owner/repo@{COMMIT} # v1.2.3\n'
                   f'uses: owner/repo@{OTHER} # v2.0.0\n'
                   f'uses: another/repo@{COMMIT} # v1.2.3\n'
                   'uses: ./local\nuses: docker://alpine@sha256:' + 'c' * 64 + '\n')
        calls = []

        def resolve(repo, tag):
            calls.append((repo, tag))
            return OTHER if tag == 'v2.0.0' else COMMIT

        self.assertEqual(checker.check(self.root, resolve), [])
        self.assertEqual(calls, [('owner/repo', 'v1.2.3'), ('owner/repo', 'v2.0.0'), ('another/repo', 'v1.2.3')])

    def test_mismatch_is_reported_at_every_use_even_when_resolution_is_cached(self):
        self.write('b.yml', f'\nuses: owner/repo@{OTHER} # v1.2.3\n')
        errors = checker.check(self.root, lambda repo, tag: COMMIT)
        self.assertEqual(len(errors), 1)
        self.assertIn(f'.github/workflows/b.yml:2: owner/repo@v1.2.3 points at {COMMIT}, pinned commit is {OTHER}', errors[0])

    def test_missing_tag_and_resolution_errors_fail_with_location(self):
        for result in [None, OSError('unavailable'), subprocess.TimeoutExpired('git', 60),
                       subprocess.CalledProcessError(128, 'git')]:
            with self.subTest(result=result):
                def resolve(repo, tag):
                    if isinstance(result, Exception):
                        raise result
                    return result
                errors = checker.check(self.root, resolve)
                self.assertEqual(len(errors), 1)
                self.assertIn('.github/workflows/a.yml:1:', errors[0])
                self.assertIn('has no tag' if result is None else 'cannot resolve', errors[0])

    def test_bad_format_fails_without_resolution(self):
        self.write('a.yml', 'uses: owner/repo@main\n')
        def unexpected(repo, tag):
            self.fail('invalid pin reached the resolver')
        self.assertIn('.github/workflows/a.yml:1:', checker.check(self.root, unexpected)[0])

    def test_git_resolver_uses_exact_tag_and_prefers_peeled_commit_in_either_order(self):
        ref = 'refs/tags/v1.2.3'
        for output, expected in [('', None), (f'{COMMIT}\t{ref}\n', COMMIT),
                                 (f'{OTHER}\t{ref}\n{COMMIT}\t{ref}^{{}}\n', COMMIT),
                                 (f'{COMMIT}\t{ref}^{{}}\n{OTHER}\t{ref}\n', COMMIT)]:
            with self.subTest(output=output), patch.object(checker.subprocess, 'run') as run:
                run.return_value.stdout = output
                self.assertEqual(checker.resolve_tag('owner/repo', 'v1.2.3'), expected)
                run.assert_called_once_with(
                    ['git', 'ls-remote', '--tags', 'https://github.com/owner/repo', ref, ref + '^{}'],
                    capture_output=True, text=True, check=True, timeout=60)


if __name__ == '__main__':
    unittest.main()
