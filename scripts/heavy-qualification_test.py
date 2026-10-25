"""A report cannot suggest reuse for changed inputs or incomplete evidence."""
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('qualification', Path(__file__).with_name('heavy-qualification.py'))
q = importlib.util.module_from_spec(spec); spec.loader.exec_module(q)


def blob(value=b'one', mode=b'100644'):
    return mode, b'blob', value


def run(**changes):
    result = dict(head_sha='base', event='push', head_branch='main', created_at='now', id=1,
                  status='completed', conclusion='success', run_attempt=1, html_url='https://example.invalid/1')
    result.update(changes)
    return result


class Qualification(unittest.TestCase):
    def test_only_nonexecutable_design_and_release_markdown_are_excluded(self):
        baseline = {b'internal/a.go': blob(), b'design/rule.md': blob()}
        for path in [b'design/rule.md', b'releases/rc.md']:
            changed = {**baseline, path: blob(b'two')}
            self.assertTrue(q.equivalent(baseline, changed))
        for path in [b'internal/a.go', b'stock/docs/guides/a.md', b'.github/workflows/heavy.yml',
                     b'deploy/systemd/apptownd.service', b'unknown.md', b'design/config.yaml', b'go.sum']:
            self.assertFalse(q.equivalent(baseline, {**baseline, path: blob(b'two')}), path)
        self.assertFalse(q.equivalent(baseline, baseline))

    def test_modes_symlinks_deletions_and_unknown_files_are_not_hidden(self):
        baseline = {b'design/a.md': blob(), b'stock/docs/a.md': blob()}
        for mode in [b'100755', b'120000', b'160000']:
            self.assertFalse(q.equivalent(baseline, {**baseline, b'design/a.md': blob(mode=mode)}))
            self.assertFalse(q.equivalent({**baseline, b'design/a.md': blob(mode=mode)}, baseline))
        self.assertFalse(q.equivalent(baseline, {b'design/a.md': blob()}))
        self.assertTrue(q.equivalent(baseline, {b'stock/docs/a.md': blob()}))
        self.assertFalse(q.equivalent(baseline, {**baseline, b'new.unknown': blob()}))

    def test_digest_tracks_paths_modes_types_and_object_content(self):
        original = {b'source': blob()}
        for other in [{b'other': blob()}, {b'source': blob(b'two')},
                      {b'source': blob(mode=b'100755')}, {b'source': (b'100644', b'tree', b'one')}]:
            self.assertNotEqual(q.digest(original), q.digest(other))

    def test_latest_first_attempt_complete_source_only(self):
        self.assertEqual(q.source_run([run()], 'base')['id'], 1)
        for changes in [{'status': 'in_progress'}, {'conclusion': 'failure'}, {'run_attempt': 2}]:
            self.assertIsNone(q.source_run([run(), run(id=2, **changes)], 'base'))
        self.assertIsNone(q.source_run([run(event='pull_request')], 'base'))
        self.assertIsNone(q.source_run([run(event='workflow_dispatch')], 'base'))
        with self.assertRaises(ValueError): q.source_run([run(head_sha='wrong')], 'base')

    def test_missing_skipped_failed_or_partial_job_set_is_not_execution(self):
        jobs = [{'name': name, 'status': 'completed', 'conclusion': 'success'} for name in q.EXECUTION_JOBS]
        self.assertTrue(q.complete(jobs))
        self.assertFalse(q.complete(jobs[:-1]))
        for result in ['skipped', 'failure', 'cancelled']:
            changed = [dict(job) for job in jobs]; changed[0]['conclusion'] = result
            self.assertFalse(q.complete(changed))

    def test_report_names_exact_source_but_never_changes_execution(self):
        jobs = [{'name': name, 'status': 'completed', 'conclusion': 'success'} for name in q.EXECUTION_JOBS]
        base = {b'design/a.md': blob()}; candidate = {b'design/a.md': blob(b'two')}
        with patch.object(q, 'tree', side_effect=[base, candidate]), patch.object(q, 'api', side_effect=[{'workflow_runs': [run()]}, {'total_count': len(jobs), 'jobs': jobs}]):
            result = q.report('owner/repo', 'base', 'candidate')
        self.assertEqual(result['decision'], 'would-reuse')
        self.assertEqual(result['mode'], 'report-only')
        self.assertEqual(result['source_sha'], 'base')
        self.assertEqual(result['candidate_sha'], 'candidate')
        self.assertEqual(result['source_run_id'], 1)
        with patch.object(q, 'tree', side_effect=RuntimeError('missing object')):
            self.assertEqual(q.report('owner/repo', 'base', 'candidate')['decision'], 'execute')
        with patch.object(q, 'tree', side_effect=[base, candidate]), patch.object(q, 'api', side_effect=RuntimeError('unreadable')):
            self.assertEqual(q.report('owner/repo', 'base', 'candidate')['decision'], 'execute')
        self.assertEqual(q.report('owner/repo', '', 'candidate')['decision'], 'execute')


if __name__ == '__main__': unittest.main()
