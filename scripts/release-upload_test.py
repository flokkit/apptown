#!/usr/bin/env python3
"""Publication identity checks without remote release mutations."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('release_upload', Path(__file__).with_name('release-upload.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class UploadTests(unittest.TestCase):
    def exercise(self, existing, local, expected_error=None):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, data in local.items():
                (root / name).write_bytes(data)
            effects = []
            def run(args, check):
                self.assertTrue(check)
                if args[2] == 'download':
                    name = args[args.index('--pattern') + 1]
                    Path(args[args.index('--dir') + 1], name).write_bytes(existing[name])
                else:
                    effects.append(args)
            response = json.dumps({'assets': [{'name': n} for n in existing]})
            with patch.object(module.subprocess, 'check_output', return_value=response), patch.object(module.subprocess, 'run', side_effect=run):
                if expected_error:
                    with self.assertRaisesRegex(ValueError, expected_error):
                        module.upload('v1.0.0', root)
                else:
                    module.upload('v1.0.0', root)
            return effects

    def test_identical_content_is_not_reuploaded(self):
        self.assertEqual([], self.exercise({'archive.tar.gz': b'old'}, {'archive.tar.gz': b'old'}))

    def test_all_comparisons_precede_any_upload(self):
        effects = self.exercise({'z-archive.tar.gz': b'old'}, {'a-new': b'new', 'z-archive.tar.gz': b'changed'}, 'immutable release asset changed')
        self.assertEqual([], effects)

    def test_missing_assets_never_clobber_concurrent_publication(self):
        effects = self.exercise({}, {'archive.tar.gz': b'new', module.BUNDLE: b'signed'})
        self.assertEqual(2, len(effects))
        self.assertTrue(all('--clobber' not in args for args in effects))

    def test_bundle_renewal_requires_unchanged_content(self):
        effects = self.exercise({'archive.tar.gz': b'old', module.BUNDLE: b'old signature'}, {'archive.tar.gz': b'old', module.BUNDLE: b'new signature'})
        self.assertEqual(1, len(effects))
        self.assertEqual('--clobber', effects[0][-1])
        effects = self.exercise({'archive.tar.gz': b'old', module.BUNDLE: b'old signature'}, {'archive.tar.gz': b'changed', module.BUNDLE: b'new signature'}, 'immutable release asset changed')
        self.assertEqual([], effects)

    def test_duplicate_remote_names_refuse_before_effect(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(module.subprocess, 'check_output', return_value='{"assets":[{"name":"a"},{"name":"a"}]}'), patch.object(module.subprocess, 'run') as run:
            with self.assertRaisesRegex(ValueError, 'duplicate'):
                module.upload('v1.0.0', Path(directory))
            run.assert_not_called()


if __name__ == '__main__':
    unittest.main()
