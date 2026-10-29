#!/usr/bin/env python3
"""Bound a disposable Go cache archive, retaining most recently used entries.

Only Go's entries are trimmed: the files in its two-hex-digit directories.
The files beside them stay: testexpire.txt is the mark before which stored
test results are void (go clean -testcache), so trimming it would revive
results it expired, and apptown-test-environment says what the stored results
were made under (ci-test-environment.sh).
"""
import argparse
import os
from pathlib import Path
import stat


def entry(root, path):
    """A Go cache entry: a file directly in one of its two-hex-digit directories."""
    parent = path.parent
    return parent.parent == root and len(parent.name) == 2 and all(c in '0123456789abcdef' for c in parent.name)


def trim(root, limit, include=None):
    root = Path(root)
    if root.is_symlink():
        raise ValueError('cache root must not be a symlink')
    if not root.exists():
        return 0
    files = []
    for directory, dirs, names in os.walk(root, followlinks=False):
        dirs[:] = [d for d in dirs if not (Path(directory) / d).is_symlink()]
        for name in names:
            path = Path(directory) / name
            try:
                info = path.lstat()
            except FileNotFoundError:
                continue
            if stat.S_ISREG(info.st_mode) and entry(root, path) and (include is None or include(path)):
                files.append((info.st_mtime_ns, str(path), info.st_size))
    retained = 0
    for _, name, size in sorted(files, reverse=True):
        if retained + size <= limit:
            retained += size
        else:
            try:
                Path(name).unlink()
            except FileNotFoundError:
                pass
    return retained


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('path')
    parser.add_argument('--max-mib', type=int, required=True)
    args = parser.parse_args()
    if args.max_mib < 1:
        parser.error('--max-mib must be positive')
    print(f'Go cache retained {trim(args.path, args.max_mib * 1024**2)} bytes')
