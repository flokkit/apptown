import importlib.util
import tempfile
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location('folds', Path(__file__).with_name('web-fold-check.py'))
folds = importlib.util.module_from_spec(spec)
spec.loader.exec_module(folds)


class PageDisclosures(unittest.TestCase):
    def test_counts_page_disclosures_including_a_dynamic_tag_but_allows_menus(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            src = root / 'web/src'
            src.mkdir(parents=True)
            (src / 'Page.tsx').write_text("<details><summary>Facts</summary></details><Disclosure />\nconst Panel = inline ? 'section' : 'details'")
            (src / 'MoreMenu.tsx').write_text('<Disclosure />')
            (src / 'Page.test.tsx').write_text('<details />')
            self.assertEqual(folds.counts(root), {'web/src/Page.tsx': 3})

    def test_tab_names_are_not_disclosures(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            src = root / 'web/src'
            src.mkdir(parents=True)
            (src / 'Page.tsx').write_text("const tab = 'details'; const x = <section>Facts</section>")
            self.assertEqual(folds.counts(root), {})


if __name__ == '__main__':
    unittest.main()
