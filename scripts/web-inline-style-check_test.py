import importlib.util
import tempfile
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location('inline_check', Path(__file__).with_name('web-inline-style-check.py'))
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


class InlineStyleCheckTest(unittest.TestCase):
    def test_counts_objects_and_pixel_lengths_not_unitless_or_tests(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / 'web/src'
            source.mkdir(parents=True)
            (source / 'a.tsx').write_text('''
/* <div style={{ padding: 99 }} /> */
<div style={{ marginTop: 6, opacity: .5, flex: 1, zIndex: 2, fontWeight: 600,
  lineHeight: 1.5, fontWeight: emphasized ? 600 : 400, padding: '4px 8px', width: pct + '%', height: 0 }} />
<div style={dynamic} /><div style={{ gap: 'var(--space-1)' }} />
''')
            (source / 'a.test.tsx').write_text('<div style={{ padding: 100 }} />')
            self.assertEqual(checker.counts(root), {'web/src/a.tsx': {'styles': 2, 'numericPx': 3}})

    def test_each_count_ratchets_independently_and_new_files_start_at_zero(self):
        baseline = {'a': {'styles': 3, 'numericPx': 2}}
        self.assertEqual(checker.check({'a': {'styles': 2, 'numericPx': 1}}, baseline), [])
        self.assertEqual(checker.check({}, baseline), [])
        self.assertTrue(checker.check({'a': {'styles': 4, 'numericPx': 1}}, baseline))
        self.assertTrue(checker.check({'a': {'styles': 2, 'numericPx': 3}}, baseline))
        self.assertTrue(checker.check({'new': {'styles': 1, 'numericPx': 0}}, baseline))


if __name__ == '__main__':
    unittest.main()
