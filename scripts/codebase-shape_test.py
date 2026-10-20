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


if __name__ == "__main__":
    unittest.main()
