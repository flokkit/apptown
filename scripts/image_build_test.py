"""The container compiler loop must stop at its first failed binary."""
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


class ImageBuild(unittest.TestCase):
    def run_build(self, fail_first):
        dockerfile = (ROOT / 'deploy/Dockerfile').read_text()
        marker = 'RUN --mount=type=cache,target=/root/.cache/go-build \\\n'
        self.assertEqual(dockerfile.count(marker), 1)
        command = dockerfile.split(marker, 1)[1].split('\n\n', 1)[0].replace('\\\n', '')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            calls = root / 'calls'
            (root / 'go').write_text('#!/bin/sh\nfirst=0\n[ -e "$CALLS" ] || first=1\nprintf "go %s\\n" "$1" >> "$CALLS"\nif [ "$FAIL_FIRST" = 1 ] && [ "$first" = 1 ]; then exit 37; fi\n')
            (root / 'cp').write_text('#!/bin/sh\nprintf "cp\\n" >> "$CALLS"\n')
            for tool in ['go', 'cp']:
                (root / tool).chmod(0o755)
            env = dict(os.environ, PATH=str(root)+':'+os.environ['PATH'], CALLS=str(calls), FAIL_FIRST=str(int(fail_first)))
            result = subprocess.run(['sh', '-c', command], env=env, capture_output=True, text=True)
            return result, calls.read_text().splitlines()

    def test_first_compiler_failure_stops_later_binaries_copy_and_stock(self):
        result, calls = self.run_build(True)
        self.assertEqual(result.returncode, 37, result.stdout+result.stderr)
        self.assertEqual(calls, ['go build'])

    def test_success_builds_three_binaries_then_copy_and_stock(self):
        result, calls = self.run_build(False)
        self.assertEqual(result.returncode, 0, result.stdout+result.stderr)
        self.assertEqual(calls, ['go build', 'go build', 'go build', 'cp', 'go run'])


if __name__ == '__main__':
    unittest.main()
