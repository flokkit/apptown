"""The CSS gate rejects unlayered rules and oversized files, including new files."""
import importlib.util
import tempfile
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("css_check", Path(__file__).with_name("web-css-check.py"))
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


class CSSCheckTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.source = self.root / "web/src"
        self.source.mkdir(parents=True)
        (self.source / "layers.css").write_text(f"@layer {checker.LAYERS};\n")

    def check(self, css):
        (self.source / "new.css").write_text(css)
        return checker.check(self.root)

    def test_nested_rules_and_quoted_braces_pass(self):
        self.assertEqual(self.check('/* why */ @layer components { @media (width < 800px) { .a::after { content: "}"; } } }'), [])

    def test_the_type_layer_is_declared_between_base_and_components(self):
        self.assertEqual(self.check('@layer type { h2 { font-size: var(--fs-lg); } }'), [])
        self.assertIn("base, type, components", checker.LAYERS)

    def test_unlayered_rules_fail(self):
        for css in ['.a {}', '@layer components { .a {} } .b {}', '.a {} @layer pages {}', '@layer pages {} @layer base {}', '@layer other {}', '@layer pages {']:
            with self.subTest(css=css):
                self.assertTrue(self.check(css))

    def test_line_limit(self):
        self.assertEqual(self.check('@layer pages {\n' + '\n' * 398 + '}\n'), [])
        self.assertTrue(self.check('@layer pages {\n' + '\n' * 399 + '}\n'))

    def test_layer_order_is_required(self):
        (self.source / "layers.css").write_text('@layer pages, components;')
        self.assertTrue(checker.check(self.root))
        (self.source / "layers.css").unlink()
        self.assertTrue(checker.check(self.root))


if __name__ == "__main__":
    unittest.main()
