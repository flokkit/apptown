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
            self.assertEqual(checker.counts(root), {'web/src/a.tsx': {'styles': 2, 'numericPx': 3, 'typeProps': 2}})

    def test_type_props_count_weights_families_and_literal_inks(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / 'web/src'
            source.mkdir(parents=True)
            (source / 'a.tsx').write_text('''
<b style={{ fontWeight: 600 }} /><i style={{ fontFamily: 'var(--font-mono)' }} /><u style={{ color: '#fff' }} />
<s style={{ font: 'inherit' }} /><p style={{ fontSize: `calc(var(--fs-base) * 2)` }} />
<p style={{ color: 'var(--bad)', fontSize: 'var(--fs-xs)', background: '#fff' }} />
''')
            self.assertEqual(checker.counts(root)['web/src/a.tsx']['typeProps'], 4)

    def test_each_count_ratchets_independently_and_new_files_start_at_zero(self):
        baseline = {'a': {'styles': 3, 'numericPx': 2}}
        self.assertEqual(checker.check({'a': {'styles': 2, 'numericPx': 1}}, baseline), [])
        self.assertEqual(checker.check({}, baseline), [])
        self.assertTrue(checker.check({'a': {'styles': 4, 'numericPx': 1}}, baseline))
        self.assertTrue(checker.check({'a': {'styles': 2, 'numericPx': 3}}, baseline))
        self.assertTrue(checker.check({'new': {'styles': 1, 'numericPx': 0}}, baseline))

    def test_type_props_must_equal_the_baseline(self):
        baseline = {'a': {'styles': 3, 'numericPx': 2, 'typeProps': 2}}
        self.assertEqual(checker.check({'a': {'styles': 3, 'numericPx': 2, 'typeProps': 2}}, baseline), [])
        self.assertTrue(checker.check({'a': {'styles': 3, 'numericPx': 2, 'typeProps': 1}}, baseline))
        self.assertTrue(checker.check({'a': {'styles': 3, 'numericPx': 2, 'typeProps': 3}}, baseline))
        self.assertTrue(checker.check({}, baseline))


if __name__ == '__main__':
    unittest.main()
