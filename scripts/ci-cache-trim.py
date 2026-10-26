#!/usr/bin/env python3
"""Bound a disposable Go compilation archive, retaining most recently used files."""
import argparse
import os
from pathlib import Path
import stat


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
            if stat.S_ISREG(info.st_mode) and (include is None or include(path)):
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
    print(f'Go compilation cache retained {trim(args.path, args.max_mib * 1024**2)} bytes')
