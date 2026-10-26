#!/usr/bin/env python3
"""Upload release assets after checking every existing content identity.

The caller creates a new draft/release through its normal publication workflow.
This command never overwrites content. Only the signature bundle may be renewed
once all authenticated content has compared equal. It invokes gh with argv.
"""
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile

BUNDLE = 'SHA256SUMS.sigstore.json'


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as f:
        for chunk in iter(lambda: f.read(1 << 20), b''):
            h.update(chunk)
    return h.digest()


def upload(tag, directory):
    assets = json.loads(subprocess.check_output(['gh', 'release', 'view', tag, '--json', 'assets'], text=True))['assets']
    names = [a['name'] for a in assets]
    if len(set(names)) != len(names):
        raise ValueError('published release has duplicate asset names')
    names = set(names)
    pending = []
    with tempfile.TemporaryDirectory(prefix='release-identity.') as temp:
        for path in sorted(directory.iterdir()):
            if not path.is_file() or path.is_symlink():
                raise ValueError('release directory contains an unsupported entry')
            if path.name not in names or path.name == BUNDLE:
                pending.append(path)
                continue
            subprocess.run(['gh', 'release', 'download', tag, '--pattern', path.name, '--dir', temp], check=True)
            if digest(Path(temp) / path.name) != digest(path):
                raise ValueError('immutable release asset changed: ' + path.name)
        # Comparison completes before the first upload. Missing content uploads
        # have no --clobber: a concurrent publisher cannot be silently replaced.
        for path in pending:
            args = ['gh', 'release', 'upload', tag, str(path)]
            if path.name == BUNDLE and path.name in names:
                args.append('--clobber')
            subprocess.run(args, check=True)


if __name__ == '__main__':
    if len(sys.argv) != 3:
        raise SystemExit('usage: release-upload.py <tag> <release-directory>')
    upload(sys.argv[1], Path(sys.argv[2]))
