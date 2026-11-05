import importlib.util
import tempfile
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location('grid_check', Path(__file__).with_name('web-grid-check.py'))
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


class GridCheckTest(unittest.TestCase):
    def test_counts_fixed_tracks_including_minmax_and_inline_styles(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / 'web/src'
            source.mkdir(parents=True)
            (source / 'a.css').write_text('/* grid-template-columns: 99px; */ .a { grid-template-columns: minmax(0,1fr) minmax(12px, 24px) auto; }')
            (source / 'b.tsx').write_text('const style = { gridTemplateColumns: "1fr 80px" }')
            self.assertEqual(checker.counts(root), {'web/src/a.css': 2, 'web/src/b.tsx': 1})

    def test_growth_and_new_files_fail_removal_passes(self):
        self.assertEqual(checker.check({'a': 2}, {'a': 3}), [])
        self.assertEqual(checker.check({}, {'a': 3}), [])
        self.assertTrue(checker.check({'a': 4}, {'a': 3}))
        self.assertTrue(checker.check({'new': 1}, {}))


if __name__ == '__main__':
    unittest.main()
