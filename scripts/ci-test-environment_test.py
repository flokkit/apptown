"""Stored test results stand only under the environment they were made in:
another one, none recorded, or a fresh run expires them all.
docker and go are stubs; docker answers image IDs from the STUB_IMAGES file."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent
SCRIPT = HERE / 'ci-test-environment.sh'

DOCKER = r'''#!/bin/sh
case "$1" in
version) case "$*" in *Client*) echo 29.0.0 ;; *) echo "${STUB_DAEMON:-28.5.1}" ;; esac; exit 0 ;;
image)
	shift 2
	[ "$1" != --format ] || shift 2
	status=0
	for ref; do
		id=$(awk -v r="$ref" '$1 == r { print $2 }' "$STUB_IMAGES")
		if [ -n "$id" ]; then echo "$id"; else echo "Error: No such image: $ref" >&2; status=1; fi
	done
	exit $status ;;
esac
exit 97
'''

GO = r'''#!/bin/sh
case "$1 $2" in
"env GOCACHE") echo "$STUB_GOCACHE" ;;
"clean -testcache") echo clean >>"$STUB_CLEANS" ;;
*) exit 97 ;;
esac
'''


def listed():
    refs = []
    for line in (ROOT / 'deploy/ci-heavy/test-images.txt').read_text().splitlines():
        line = line.split('#')[0].strip()
        if line:
            refs.append(line)
    return refs


class Environment(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.dir = Path(tmp.name)
        bindir = self.dir / 'bin'
        bindir.mkdir()
        for name, body in [('docker', DOCKER), ('go', GO)]:
            (bindir / name).write_text(body)
            (bindir / name).chmod(0o755)
        self.cache = self.dir / 'gocache'
        self.cleans = self.dir / 'cleans'
        self.images = {ref: 'sha256:' + format(i, '064x') for i, ref in enumerate(listed())}
        self.env = dict(os.environ, PATH=f'{bindir}{os.pathsep}{os.environ["PATH"]}', STUB_GOCACHE=str(self.cache),
                        STUB_CLEANS=str(self.cleans), ImageOS='ubuntu24', ImageVersion='20261020.1')

    def run_mode(self, mode, images=None, **env):
        self.cleans.write_text('')
        table = self.dir / 'images'
        table.write_text(''.join(f'{ref} {id}\n' for ref, id in (self.images if images is None else images).items()))
        r = subprocess.run(['sh', str(SCRIPT), mode], cwd=ROOT, capture_output=True, text=True,
                           env={**self.env, 'STUB_IMAGES': str(table), **env})
        return r, self.cleans.read_text().count('clean')

    def test_results_stand_only_under_the_same_environment(self):
        r, cleans = self.run_mode('reuse')
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(cleans, 1, 'nothing was recorded, so nothing stored may stand')
        document = (self.cache / 'apptown-test-environment').read_text()
        for ref, id in self.images.items():
            self.assertIn(f'image {ref} {id}\n', document)
        self.assertIn('runner ubuntu24 20261020.1\n', document)
        self.assertIn('built apptown-agent absent\n', document)
        r, cleans = self.run_mode('reuse')
        self.assertEqual((r.returncode, cleans), (0, 0), r.stdout)
        self.assertEqual((self.cache / 'apptown-test-environment').read_text(), document)

    def test_any_difference_expires_every_result(self):
        self.run_mode('reuse')
        moved = dict(self.images, **{listed()[0]: 'sha256:' + 'f' * 64})
        built = dict(self.images, **{'apptown-agent': 'sha256:' + 'a' * 64})
        for change in [dict(images=moved), dict(images=built), dict(ImageVersion='20261027.1'), dict(STUB_DAEMON='29.0.0')]:
            with self.subTest(change=list(change)):
                r, cleans = self.run_mode('reuse', **change)
                self.assertEqual((r.returncode, cleans), (0, 1), r.stdout + r.stderr)
                self.assertIn('they expire', r.stdout)
                self.run_mode('reuse')  # back to the first environment, expiring again

    def test_fresh_always_expires(self):
        self.run_mode('reuse')
        for _ in range(2):
            r, cleans = self.run_mode('fresh')
            self.assertEqual((r.returncode, cleans), (0, 1))

    def test_an_image_that_is_not_there_fails_and_changes_nothing(self):
        self.run_mode('reuse')
        document = (self.cache / 'apptown-test-environment').read_text()
        missing = dict(self.images)
        del missing[listed()[-1]]
        r, cleans = self.run_mode('reuse', images=missing)
        self.assertNotEqual(r.returncode, 0)
        self.assertEqual(cleans, 0)
        self.assertIn('is not pulled', r.stderr)
        self.assertIn('No such image: ' + listed()[-1], r.stderr)
        self.assertEqual((self.cache / 'apptown-test-environment').read_text(), document)

    def test_an_unknown_mode_does_nothing(self):
        r, cleans = self.run_mode('cached')
        self.assertEqual((r.returncode, cleans), (2, 0))
        self.assertFalse(self.cache.exists())


if __name__ == '__main__':
    unittest.main()
