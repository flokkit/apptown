"""The archive budget drops only disposable files within its cache root."""
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('trim', Path(__file__).with_name('ci-cache-trim.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class CacheBudget(unittest.TestCase):
    def test_newest_files_fit_budget_without_following_links(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / 'cache'
            root.mkdir()
            outside = Path(directory) / 'outside'
            outside.mkdir()
            witness = outside / 'keep'
            witness.write_bytes(b'important')
            (root / 'link').symlink_to(outside, target_is_directory=True)
            (root / 'file-link').symlink_to(witness)
            for index in range(4):
                p = root / str(index)
                p.write_bytes(b'1234')
                os.utime(p, (index + 1, index + 1))
            self.assertEqual(module.trim(root, 9), 8)
            self.assertEqual({p.name for p in root.iterdir() if not p.is_symlink()}, {'2', '3'})
            self.assertEqual(witness.read_bytes(), b'important')
            self.assertTrue((root / 'link').is_symlink())

    def test_empty_missing_and_oversize_cache(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / 'cache'
            self.assertEqual(module.trim(root, 5), 0)
            root.mkdir()
            self.assertEqual(module.trim(root, 5), 0)
            (root / 'large').write_bytes(b'123456')
            self.assertEqual(module.trim(root, 5), 0)
            self.assertFalse((root / 'large').exists())

    def test_link_root_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            link = Path(directory) / 'link'
            link.symlink_to(directory, target_is_directory=True)
            with self.assertRaises(ValueError):
                module.trim(link, 0)


if __name__ == '__main__':
    unittest.main()
