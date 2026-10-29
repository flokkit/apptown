#!/usr/bin/env python3
"""Give every file and directory of a checkout a modification time derived from its content.

    python3 scripts/ci-content-mtimes.py <directory>

Go's test cache keys a result on the size and modification time of each file
inside the module that the test opened or stat-ed, and on the times of a
directory's entries when it read the directory. A fresh checkout gives every
file the time of the checkout, so no such result could be reused by a later
run; a fixed time for every file would let a same-size edit reuse one. Here
equal content gives an equal time, and different content a different time
(56 bits of a SHA-256: 2001-01-01 plus up to about two years, always in the
past, so Go never refuses a file as too new). A directory's time is derived
from its entries' names, kinds and content. Symbolic links get a time from
their target text and are not followed. Git's own directory is left alone.
"""
import hashlib
import os
from pathlib import Path
import stat
import sys

BASE_NS = 978307200 * 10**9  # 2001-01-01T00:00:00Z


def file_digest(path):
    h = hashlib.sha256(b'file\0')
    with open(path, 'rb') as f:
        for block in iter(lambda: f.read(1 << 20), b''):
            h.update(block)
    return h.digest()


def stamp(path, digest):
    ns = BASE_NS + int.from_bytes(digest[:7], 'big')
    os.utime(path, ns=(ns, ns), follow_symlinks=False)


def tree_digest(directory, top):
    """Stamp everything under directory; return its digest and how many directories it holds."""
    h = hashlib.sha256(b'dir\0')
    count = 1
    for entry in sorted(os.scandir(directory), key=lambda e: e.name):
        if top and entry.name == '.git':
            continue
        info = entry.stat(follow_symlinks=False)
        if stat.S_ISLNK(info.st_mode):
            kind, digest = b'link', hashlib.sha256(b'link\0' + os.fsencode(os.readlink(entry.path))).digest()
        elif stat.S_ISDIR(info.st_mode):
            (digest, n), kind = tree_digest(entry.path, False), b'dir'
            count += n
        elif stat.S_ISREG(info.st_mode):
            kind, digest = b'file', file_digest(entry.path)
        else:
            raise ValueError(f'unexpected file type: {entry.path}')
        if kind != b'dir':
            stamp(entry.path, digest)
        h.update(kind + b'\0' + os.fsencode(entry.name) + b'\0' + oct(info.st_mode & 0o7777).encode() + b'\0' + digest)
    digest = h.digest()
    stamp(directory, digest)
    return digest, count


def apply(root):
    root = Path(root)
    if root.is_symlink() or not root.is_dir():
        raise ValueError('root must be a directory, not a symlink')
    return tree_digest(str(root), True)[1]


if __name__ == '__main__':
    if len(sys.argv) != 2:
        print(__doc__.strip().splitlines()[2].strip(), file=sys.stderr)
        sys.exit(2)
    print(f'content mtimes set under {apply(sys.argv[1])} directories')
