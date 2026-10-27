"""Tour publication preserves immutable history and does not release software."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace

spec = importlib.util.spec_from_file_location('publish_tour', Path(__file__).with_name('publish-tour.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
SHA = 'a' * 40


class Store:
    def __init__(self):
        self.objects = {}
        self.writes = []
        self.fail = None

    def read(self, key, fresh=False):
        return self.objects.get(key)

    def put(self, key, data, content_type, immutable=False):
        if self.fail == key:
            raise RuntimeError('interrupted')
        if immutable and key in self.objects and self.objects[key] != data:
            raise RuntimeError('immutable collision')
        self.objects[key] = data
        self.writes.append(key)


class PublicationTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.folder = Path(self.tmp.name)
        self.store = Store()
        self.fixture('v1.0.0')

    def fixture(self, version):
        (self.folder / 'metadata.json').write_text(json.dumps({'version': version, 'source': SHA, 'kind': 'preview' if version == 'Unreleased preview' else 'release'}))
        (self.folder / 'index.html').write_text('<main><img src="home.png"><source srcset="home-dark.png"></main>')
        for name in ['home.png', 'home-dark.png']:
            (self.folder / name).write_bytes(b'\x89PNG\r\n\x1a\n' + name.encode())

    def release(self, bootstrap=False):
        m.publish(self.store, self.folder, 'release', 'v1.0.0', SHA, bootstrap)

    def test_partial_retry_and_same_snapshot_retry_preserve_bytes(self):
        self.store.fail = 'tour/v1.0.0/index.html'
        with self.assertRaises(RuntimeError):
            self.release(True)
        self.assertNotIn('tour/versions.json', self.store.objects)
        self.store.fail = None
        self.release(True)
        expected = dict(self.store.objects)
        self.release()
        self.assertEqual(self.store.objects, expected)
        order = self.store.writes
        self.assertLess(order.index('tour/v1.0.0/index.html'), order.index('tour/versions.json'))
        (self.folder / 'home.png').write_bytes(b'\x89PNG\r\n\x1a\nchanged')
        with self.assertRaises(ValueError):
            self.release()
        self.assertEqual(self.store.objects, expected)

    def test_preview_only_writes_tour_and_preserves_released_identity(self):
        self.release(True)
        self.fixture('Unreleased preview')
        m.publish(self.store, self.folder, 'preview', 'preview-' + SHA, SHA)
        self.assertTrue(all(key.startswith('tour/') for key in self.store.writes))
        history = self.store.objects['tour/index.html'].decode()
        self.assertIn('Unreleased preview', history)
        self.assertIn('Latest released tour: <a href="/tour/v1.0.0/">v1.0.0</a>', history)
        entries = json.loads(self.store.objects['tour/versions.json'])
        self.assertEqual([e['kind'] for e in entries], ['release', 'preview'])

    def test_new_release_replaces_featured_preview_and_backfill_does_not_regress_release(self):
        self.release(True)
        self.fixture('Unreleased preview')
        m.publish(self.store, self.folder, 'preview', 'preview-' + SHA, SHA)
        self.fixture('v1.1.0')
        m.publish(self.store, self.folder, 'release', 'v1.1.0', SHA)
        self.fixture('v0.9.0')
        m.publish(self.store, self.folder, 'release', 'v0.9.0', SHA)
        history = self.store.objects['tour/index.html'].decode()
        self.assertIn('Take the tour</h2><p><a href="/tour/v1.1.0/">', history)
        self.assertIn('Latest released tour: <a href="/tour/v1.1.0/">', history)

    def test_snapshot_replaces_generated_identity_instead_of_duplicating_it(self):
        page = self.folder / 'index.html'
        page.write_text(page.read_text().replace('<main>', '<main><p class="lead" data-tour-identity>Local identity · All versions</p>'))
        self.release(True)
        published = self.store.objects['tour/v1.0.0/index.html'].decode()
        self.assertEqual(published.count('All versions'), 1)
        self.assertNotIn('Local identity', published)

    def test_missing_catalog_and_wrong_identity_fail_before_writes(self):
        with self.assertRaises(RuntimeError):
            self.release()
        with self.assertRaises(ValueError):
            m.publish(self.store, self.folder, 'preview', 'preview-' + SHA, SHA, True)
        with self.assertRaises(ValueError):
            m.publish(self.store, self.folder, 'legacy', '../escape', bootstrap=True)
        self.assertEqual(self.store.writes, [])

    def test_legacy_is_unknown_and_all_image_references_are_snapshot_local(self):
        m.publish(self.store, self.folder, 'legacy', 'legacy-2026-10-27', bootstrap=True)
        page = self.store.objects['tour/legacy-2026-10-27/index.html'].decode()
        self.assertIn('Original release unknown', page)
        self.assertNotIn('src="home.png"', page)
        self.assertNotIn('srcset="home-dark.png"', page)
        self.assertNotIn('v1.0.0', page)


class TransportTest(unittest.TestCase):
    def test_conditional_upload_accepts_only_identical_existing_bytes(self):
        client = m.Publisher('bucket', 'https://example.invalid')
        conflict = SimpleNamespace(returncode=1, stderr='PreconditionFailed')
        with patch.object(m.subprocess, 'run', return_value=conflict) as run, patch.object(client, 'read', return_value=b'old'):
            client.put('tour/v1/index.html', b'old', 'text/html', True)
            self.assertIn('--if-none-match', run.call_args.args[0])
            with self.assertRaises(RuntimeError):
                client.put('tour/v1/index.html', b'new', 'text/html', True)

    def test_stale_cached_catalog_is_never_used_for_history(self):
        class Response:
            headers = {'Age': '90'}
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def read(self): return b'[]'
        with patch.object(m.time, 'sleep') as sleep, patch.object(m.urllib.request, 'urlopen', return_value=Response()):
            with self.assertRaises(RuntimeError):
                m.Publisher('bucket', 'https://example.invalid').read('tour/versions.json', fresh=True)
            sleep.assert_called_once_with(2)


if __name__ == '__main__':
    unittest.main()
