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
            (root / 'ab').mkdir()
            for index in range(4):
                p = root / 'ab' / str(index)
                p.write_bytes(b'1234')
                os.utime(p, (index + 1, index + 1))
            self.assertEqual(module.trim(root, 9), 8)
            self.assertEqual({p.name for p in (root / 'ab').iterdir()}, {'2', '3'})
            self.assertEqual(witness.read_bytes(), b'important')
            self.assertTrue((root / 'link').is_symlink())

    def test_empty_missing_and_oversize_cache(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / 'cache'
            self.assertEqual(module.trim(root, 5), 0)
            root.mkdir()
            self.assertEqual(module.trim(root, 5), 0)
            (root / '0f').mkdir()
            (root / '0f' / 'large').write_bytes(b'123456')
            self.assertEqual(module.trim(root, 5), 0)
            self.assertFalse((root / '0f' / 'large').exists())

    def test_the_expiry_mark_and_environment_are_never_trimmed(self):
        # Trimming testexpire.txt would make results go clean -testcache
        # expired valid again.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / 'cache'
            (root / '00').mkdir(parents=True)
            (root / '00' / 'old-d').write_bytes(b'x' * 10)
            os.utime(root / '00' / 'old-d', (1, 1))
            for name in ('testexpire.txt', 'apptown-test-environment', 'trim.txt', 'README'):
                (root / name).write_bytes(b'kept')
                os.utime(root / name, (0, 0))
            (root / 'notes').mkdir()
            (root / 'notes' / 'x').write_bytes(b'kept')
            self.assertEqual(module.trim(root, 0), 0)
            self.assertFalse((root / '00' / 'old-d').exists())
            for name in ('testexpire.txt', 'apptown-test-environment', 'trim.txt', 'README', 'notes/x'):
                self.assertEqual((root / name).read_bytes(), b'kept', name)

    def test_a_caller_can_narrow_the_entries_further(self):
        # private/scripts/cache-budget.py trims only compilation entries.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / 'cache'
            (root / 'ab').mkdir(parents=True)
            for name in ('keep', 'drop'):
                (root / 'ab' / name).write_bytes(b'x')
            module.trim(root, 0, include=lambda p: p.name == 'drop')
            self.assertEqual({p.name for p in (root / 'ab').iterdir()}, {'keep'})

    def test_link_root_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            link = Path(directory) / 'link'
            link.symlink_to(directory, target_is_directory=True)
            with self.assertRaises(ValueError):
                module.trim(link, 0)


if __name__ == '__main__':
    unittest.main()
