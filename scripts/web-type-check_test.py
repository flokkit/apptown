"""Fixtures protect the typography gate, including new source files."""
import importlib.util
import tempfile
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("type_check", Path(__file__).with_name("web-type-check.py"))
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


class TypeCheckTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.source = self.root / "web/src/new"
        self.source.mkdir(parents=True)

    def check(self, content, suffix="css"):
        (self.source / f"fixture.{suffix}").write_text(content)
        return checker.check(self.root)

    def test_rem_tokens_and_field_floor_pass(self):
        self.assertEqual(self.check('''@layer tokens {
          :root { --fs-sm: .928571rem; --fs-xs: calc(12rem / 14); --space-1: 0.285714rem;
            --fs-field: max(1rem, 16px); }
          @media (width < 800px) { input { font-size: var(--fs-field); line-height: 1.5; } }
          /* font-size: 12px; --fs-old: 12px; */
        }'''), [])

    def test_pixel_type_and_line_heights_fail(self):
        for declaration in ['font-size: 14px', 'font-size: max(1rem, 16px)',
                            'line-height: 24px', 'line-height: 1.5rem', 'line-height: 150%']:
            with self.subTest(declaration=declaration):
                self.assertTrue(self.check(f'.a {{ {declaration}; }}'))

    def test_every_token_definition_must_be_rem(self):
        for declaration in ['--fs-sm: 13px', '--space-1: 4px', '--fs-base: 1em',
                            '--fs-sm: var(--other)', '--fs-other: max(1rem, 16px)',
                            '--fs-field: 16px', '--fs-xs: calc(12px / 14)', '--fs-field: max(1rem, 14px)']:
            with self.subTest(declaration=declaration):
                self.assertTrue(self.check(f':root {{ {declaration}; }}'))

    def test_numeric_tsx_sizes_fail(self):
        for value in ['14', '14.5', 'size * 0.7', 'size']:
            with self.subTest(value=value):
                self.assertTrue(self.check(f'<span style={{{{ fontSize: {value} }}}} />', 'tsx'))

    def test_numeric_test_data_and_types_are_not_style_props(self):
        self.assertEqual(self.check('const options: { fontSize: number } = { fontSize: 13 }', 'tsx'), [])

    def test_tsx_token_and_comments_pass(self):
        self.assertEqual(self.check('''// fontSize: 14
          <span style={{ fontSize: 'var(--fs-sm)' }} />
          {/* fontSize: 12 */}''', 'tsx'), [])


if __name__ == "__main__":
    unittest.main()
