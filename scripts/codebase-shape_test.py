"""The private reorganisation preserves the product/tooling boundary."""
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("codebase_shape", Path(__file__).with_name("codebase-shape.py"))
shape = importlib.util.module_from_spec(spec)
spec.loader.exec_module(shape)


class PrivateTooling(unittest.TestCase):
    def test_moved_tooling_stays_out_of_the_default_product_measurement(self):
        for path in ("scripts/nightly-tiers.sh", "eval/resurrection/parity.sh"):
            with self.subTest(path=path):
                self.assertIsNone(shape.kind_of(path))
                self.assertIsNone(shape.kind_of("private/" + path))


class ModuleSeries(unittest.TestCase):
    # A subsystem keeps one series across both moves: internal/<x> before
    # 2026-09-13, internal/forge/<x> until 2026-10-22, internal/apptown/<x>
    # since; the root's own files are one module under every name.
    def test_a_subsystem_is_one_module_under_each_of_its_paths(self):
        for path in ("internal/workflow/run.go", "internal/forge/workflow/run.go", "internal/apptown/workflow/run.go"):
            with self.subTest(path=path):
                self.assertEqual(shape.module_of(path), "internal/apptown/workflow")
        for path in ("internal/forge/app.go", "internal/apptown/app.go"):
            with self.subTest(path=path):
                self.assertEqual(shape.module_of(path), "internal/apptown")


if __name__ == "__main__":
    unittest.main()
