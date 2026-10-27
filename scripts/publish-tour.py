#!/usr/bin/env python3
"""Publish one complete immutable tour, then its small public history page.

Release: publish-tour.py site/tour --version v1.0.0 --source <sha>
Preview: publish-tour.py site/tour --preview --source <sha>
Legacy: publish-tour.py <saved-tour> --legacy legacy-YYYY-MM-DD --bootstrap

Run publishers serially (the release workflow has one concurrency group).
Preview/legacy publication touches only tour objects. No bucket reads/listing
are needed: existing bytes are read through the public site. --bootstrap is
only for the reviewed initial catalog, after preserving the original tour.
"""
import argparse
import hashlib
import html
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


class Publisher:
    def __init__(self, bucket, base):
        self.bucket, self.base = bucket, base.rstrip('/')

    def read(self, key, fresh=False):
        # Managed-CachingOptimized ignores query strings and has a one-second
        # minimum TTL. Our mutable catalog is published with max-age=0.
        if fresh:
            time.sleep(2)
        try:
            with urllib.request.urlopen(self.base + '/' + key, timeout=30) as r:
                if fresh and int(r.headers.get('Age', '0')) > 1:
                    raise RuntimeError('catalog is cached; refusing to overwrite history')
                return r.read()
        except urllib.error.HTTPError as e:
            if e.code in (403, 404):
                return None
            raise

    def put(self, key, data, content_type, immutable=False):
        with tempfile.NamedTemporaryFile() as f:
            f.write(data); f.flush()
            args = ['aws', 's3api', 'put-object', '--bucket', self.bucket,
                    '--key', key, '--body', f.name, '--content-type', content_type,
                    '--cache-control', 'public,max-age=31536000,immutable' if immutable
                    else 'no-cache,no-store,max-age=0,must-revalidate']
            if immutable:
                args += ['--if-none-match', '*']
            result = subprocess.run(args, capture_output=True, text=True)
            if result.returncode:
                if immutable and 'PreconditionFailed' in result.stderr:
                    if self.read(key) == data:
                        return
                    raise RuntimeError('existing snapshot differs: ' + key)
                raise RuntimeError('upload failed for ' + key + ': ' + result.stderr)


def encoded(value):
    return (json.dumps(value, sort_keys=True, indent=2) + '\n').encode()


def release_order(version):
    m = re.fullmatch(r'v?(\d+)\.(\d+)\.(\d+)(?:-rc\.(\d+))?', version)
    if not m:
        raise ValueError('expected a final or rc product version')
    return tuple(map(int, m.group(1, 2, 3))) + (int(m[4]) if m[4] else 10**12,)


def snapshot(folder, kind, identity, source):
    if kind != 'legacy':
        if not re.fullmatch('[0-9a-f]{40}', source):
            raise ValueError('source must be a full commit SHA')
        metadata = json.loads((folder / 'metadata.json').read_text())
        expected = identity if kind == 'release' else 'Unreleased preview'
        if metadata != {'version': expected, 'source': source, 'kind': kind}:
            raise ValueError('tour metadata does not match publication identity')
    page = (folder / 'index.html').read_text()
    refs = set(re.findall(r'(?:src|srcset)="([^" ]+\.png)"', page))
    if not refs:
        raise ValueError('tour has no screenshots')
    assets = {}
    for name in refs:
        if Path(name).name != name:
            raise ValueError('screenshot must be a local filename')
        data = (folder / name).read_bytes()
        if not data.startswith(b'\x89PNG\r\n\x1a\n'):
            raise ValueError('not a PNG: ' + name)
        hashed = hashlib.sha256(data).hexdigest() + '.png'
        assets[hashed] = data
        page = page.replace('"' + name + '"', '"' + hashed + '"')
    label = identity if kind == 'release' else ('Unreleased preview' if kind == 'preview' else 'Original release unknown')
    notice = '<p>AppTown tour · ' + html.escape(label)
    if source:
        notice += ' · source <code>' + html.escape(source) + '</code>'
    notice += ' · <a href="/tour/">All versions</a></p>'
    page, replaced = re.subn(r'<p[^>]*data-tour-identity[^>]*>.*?</p>', lambda _: notice, page, count=1)
    if not replaced:
        page = page.replace('<main>', '<main>\n' + notice, 1)
    return assets, page.encode()


def catalog_page(entries):
    releases = sorted((e for e in entries if e['kind'] == 'release'), key=lambda e: release_order(e['id']), reverse=True)
    latest = entries[-1] if entries else None
    if latest and latest['kind'] != 'preview' and releases:
        latest = releases[0]
    def link(e):
        label = e['id'] if e['kind'] == 'release' else ('Unreleased preview — ' + e['source'][:12] if e['kind'] == 'preview' else 'Original tour — release unknown')
        return '<a href="/tour/' + e['id'] + '/">' + html.escape(label) + '</a>'
    body = '<h1>AppTown tours</h1><p>See what changed, release by release.</p>'
    if latest:
        body += '<h2>Take the tour</h2><p>' + link(latest) + '</p>'
    body += '<p>Latest released tour: ' + (link(releases[0]) if releases else 'No release identity has been verified yet.') + '</p>'
    body += '<h2>Earlier tours</h2><ul>' + ''.join('<li>' + link(e) + '</li>' for e in reversed(entries)) + '</ul>'
    return ('<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>AppTown tours</title><style>body{font:18px/1.6 system-ui;max-width:55rem;margin:4rem auto;padding:0 1rem;background:#fbfaf7;color:#242822}a{color:#285d4b}li{margin:.6rem 0}</style><main>' + body + '<p><a href="/">Back to AppTown</a></p></main></html>\n').encode()


def publish(client, folder, kind, identity, source='', bootstrap=False):
    if not re.fullmatch(r'[a-zA-Z0-9.-]+', identity):
        raise ValueError('unsafe snapshot identity')
    if kind == 'release':
        release_order(identity)
    assets, page = snapshot(folder, kind, identity, source)
    old = client.read('tour/versions.json', fresh=True)
    if old is None and not bootstrap:
        raise RuntimeError('no readable catalog; explicit reviewed --bootstrap required')
    entries = json.loads(old) if old else []
    if not isinstance(entries, list) or any(set(e) != {'id', 'kind', 'source', 'digest'} for e in entries):
        raise ValueError('invalid catalog; refusing to replace it')
    entry = {'id': identity, 'kind': kind, 'source': source, 'digest': hashlib.sha256(page).hexdigest()}
    prior = next((e for e in entries if e['id'] == identity), None)
    if prior and prior != entry:
        raise ValueError('snapshot identity already belongs to different content')
    for name, data in sorted(assets.items()):
        client.put('tour/' + identity + '/' + name, data, 'image/png', True)
    client.put('tour/' + identity + '/index.html', page, 'text/html; charset=utf-8', True)
    if not prior:
        entries.append(entry)
    client.put('tour/versions.json', encoded(entries), 'application/json')
    client.put('tour/index.html', catalog_page(entries), 'text/html; charset=utf-8')


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('folder', type=Path)
    group = p.add_mutually_exclusive_group(required=True)
    group.add_argument('--version'); group.add_argument('--preview', action='store_true'); group.add_argument('--legacy')
    p.add_argument('--source', default=''); p.add_argument('--bootstrap', action='store_true')
    args = p.parse_args()
    kind = 'preview' if args.preview else ('legacy' if args.legacy else 'release')
    identity = 'preview-' + args.source if args.preview else (args.legacy or args.version)
    client = Publisher(os.environ.get('SITE_BUCKET', 'apptown-dev-site-078142186677'), os.environ.get('SITE_URL', 'https://apptown.dev'))
    publish(client, args.folder, kind, identity, args.source, args.bootstrap)
    subprocess.run(['aws', 'cloudfront', 'create-invalidation', '--distribution-id', os.environ.get('SITE_DISTRIBUTION', 'E1XMP2SIAVI7FR'), '--paths', '/tour/', '/tour/index.html', '/tour/versions.json'], check=True, stdout=subprocess.DEVNULL)
    print('Published /tour/' + identity + '/')


if __name__ == '__main__':
    main()
