"""Equal content gets an equal modification time, different content a different one."""
import importlib.util
import os
from pathlib import Path
import tempfile
import time
import unittest

spec = importlib.util.spec_from_file_location('mtimes', Path(__file__).with_name('ci-content-mtimes.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def times(root):
    """Every path's (mode, size, mtime_ns), as Go's test cache sees them."""
    out = {}
    for directory, dirs, names in os.walk(root):
        dirs[:] = [d for d in dirs if d != '.git']
        for name in dirs + names + ['']:
            path = Path(directory) / name
            info = path.lstat()
            out[str(path.relative_to(root))] = (info.st_mode, info.st_size if path.is_file() else 0, info.st_mtime_ns)
    return out


class ContentTimes(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.tmp = Path(tmp.name)

    def checkout(self, name):
        root = self.tmp / name
        (root / 'pkg' / 'testdata').mkdir(parents=True)
        (root / 'pkg' / 'a.txt').write_text('alpha')
        (root / 'pkg' / 'testdata' / 'b.json').write_text('{"b": 1}')
        (root / 'go.mod').write_text('module example\n')
        (root / 'link').symlink_to('pkg/a.txt')
        (root / '.git').mkdir()
        (root / '.git' / 'HEAD').write_text('ref: refs/heads/main\n')
        return root

    def test_two_checkouts_of_the_same_content_look_identical(self):
        first, second = self.checkout('one'), self.checkout('two')
        os.utime(second / 'pkg' / 'a.txt', (5, 5))
        module.apply(first)
        module.apply(second)
        self.assertEqual(times(first), times(second))

    def test_times_are_in_the_past(self):
        root = self.checkout('one')
        module.apply(root)
        now = time.time_ns()
        for path, (_, _, mtime) in times(root).items():
            self.assertLess(mtime, now - 10**9 * 86400, path)
            self.assertGreaterEqual(mtime, module.BASE_NS, path)

    def test_a_same_size_edit_changes_the_file_and_its_directories(self):
        first, second = self.checkout('one'), self.checkout('two')
        (second / 'pkg' / 'a.txt').write_text('alphA')
        module.apply(first)
        module.apply(second)
        a, b = times(first), times(second)
        for path in ('pkg/a.txt', 'pkg', '.'):
            self.assertNotEqual(a[path][2], b[path][2], path)
        self.assertEqual(a['pkg/testdata/b.json'], b['pkg/testdata/b.json'])
        self.assertEqual(a['go.mod'], b['go.mod'])

    def test_an_added_file_changes_its_directory(self):
        first, second = self.checkout('one'), self.checkout('two')
        (second / 'pkg' / 'testdata' / 'c').write_text('')
        module.apply(first)
        module.apply(second)
        a, b = times(first), times(second)
        self.assertNotEqual(a['pkg/testdata'][2], b['pkg/testdata'][2])
        self.assertEqual(a['pkg/a.txt'], b['pkg/a.txt'])

    def test_a_changed_mode_changes_the_directory(self):
        first, second = self.checkout('one'), self.checkout('two')
        (second / 'pkg' / 'a.txt').chmod(0o755)
        module.apply(first)
        module.apply(second)
        self.assertNotEqual(times(first)['pkg'][2], times(second)['pkg'][2])

    def test_git_metadata_is_left_alone_and_links_are_not_followed(self):
        root = self.checkout('one')
        os.utime(root / '.git' / 'HEAD', (7, 7))
        target = (root / 'pkg' / 'a.txt').stat().st_mtime_ns
        module.apply(root)
        self.assertEqual((root / '.git' / 'HEAD').stat().st_mtime_ns, 7 * 10**9)
        self.assertNotEqual((root / 'link').lstat().st_mtime_ns, (root / 'pkg' / 'a.txt').stat().st_mtime_ns)
        self.assertNotEqual(target, (root / 'pkg' / 'a.txt').stat().st_mtime_ns)

    def test_a_link_root_is_refused(self):
        root = self.checkout('one')
        link = self.tmp / 'root-link'
        link.symlink_to(root, target_is_directory=True)
        with self.assertRaises(ValueError):
            module.apply(link)


if __name__ == '__main__':
    unittest.main()
