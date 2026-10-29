#!/usr/bin/env python3
"""Join immutable release build facts; no signing, network or extraction."""
import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import tarfile

PLATFORMS = ('linux/amd64', 'linux/arm64', 'darwin/amd64', 'darwin/arm64')
IMAGES = ('apptown', 'apptown-worker', 'apptown-agent')
HEX = re.compile(r'^[0-9a-f]{64}$')
VERSION = re.compile(r'^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$')


def digest_stream(stream):
    h = hashlib.sha256()
    for chunk in iter(lambda: stream.read(1 << 20), b''):
        h.update(chunk)
    return h.hexdigest()


def artifact(path):
    with path.open('rb') as f:
        digest = digest_stream(f)
    return {'name': path.name, 'sha256': digest, 'size': path.stat().st_size}


def validate_identity(tag, commit):
    if not VERSION.fullmatch(tag) or not re.fullmatch('[0-9a-f]{40}', commit):
        raise ValueError('invalid release version or commit')
    prerelease = tag.partition('+')[0].partition('-')[2]
    if any(x.isdigit() and len(x) > 1 and x.startswith('0') for x in prerelease.split('.')):
        raise ValueError('invalid numeric prerelease identifier')


def image_facts(tag, commit, name, digest, document):
    validate_identity(tag, commit)
    if name not in IMAGES or not digest.startswith('sha256:') or not HEX.fullmatch(digest[7:]):
        raise ValueError('invalid image identity')
    platforms = {}
    for entry in document['manifests']:
        p = entry.get('platform', {})
        platform = p.get('os', '') + '/' + p.get('architecture', '')
        if platform == 'unknown/unknown' and entry.get('annotations', {}).get('vnd.docker.reference.type') == 'attestation-manifest':
            continue
        value = entry['digest']
        if platform not in PLATFORMS[:2] or platform in platforms or not value.startswith('sha256:') or not HEX.fullmatch(value[7:]):
            raise ValueError('image index has unsupported or duplicate platform')
        platforms[platform] = value
    if set(platforms) != set(PLATFORMS[:2]):
        raise ValueError('image index lacks both Linux platforms')
    return {'version': tag, 'commit': commit, 'repository': 'ghcr.io/flokkit/' + name,
            'index': digest, 'platforms': platforms}


# Member digests are computed to check the archives agree with each other;
# they are not published (nothing installs from the manifest).
def archive_facts(out, tag, platform):
    base = 'apptown-' + tag[1:] + '-' + platform.replace('/', '-')
    archive = out / (base + '.tar.gz')
    result = artifact(archive)
    members = {}
    with tarfile.open(archive, 'r:gz') as tar:
        for entry in tar:
            name = PurePosixPath(entry.name)
            if name.is_absolute() or '..' in name.parts or not name.parts or name.parts[0] != base or entry.name.rstrip('/') != name.as_posix():
                raise ValueError('archive entry escapes its release directory')
            relative = '/'.join(name.parts[1:])
            if entry.isdir():
                continue
            if not entry.isfile() or not relative or '\\' in relative or any(c in relative for c in '\x00\r\n'):
                raise ValueError('archive contains an unsupported member')
            if relative in members:
                raise ValueError('archive contains duplicate members')
            with tar.extractfile(entry) as f:
                members[relative] = digest_stream(f)
    required = {'bin/apptownd', 'bin/apptown', 'bin/apptown-worker', 'bin/apptown-linux',
                'deploy/apptown-agent/Dockerfile', 'scripts/apptown-db-backup.sh'}
    if not required <= members.keys() or not any(n.startswith('share/apptown/updates/') for n in members) or not any(n.startswith('deploy/systemd/') for n in members):
        raise ValueError('archive lacks paired release assets')
    return result, members


def validate_compatibility(facts):
    keys = {'sourceSchema', 'sourceLayout', 'schemaResult', 'layoutResult', 'cliProtocol',
            'daemonWorkerProtocol', 'workerProtocol'}
    if set(facts) != keys:
        raise ValueError('compatibility declaration is incomplete')
    for key in ('sourceSchema', 'sourceLayout', 'workerProtocol'):
        row = facts[key]
        if set(row) != {'min', 'max'} or any(type(n) is not int for n in row.values()) or row['min'] < 1 or row['max'] < row['min']:
            raise ValueError('invalid compatibility range')
    for key in ('schemaResult', 'layoutResult', 'cliProtocol', 'daemonWorkerProtocol'):
        if type(facts[key]) is not int or facts[key] < 1:
            raise ValueError('invalid compatibility build fact')
    if facts['schemaResult'] < facts['sourceSchema']['max'] or facts['layoutResult'] < facts['sourceLayout']['max'] or not facts['workerProtocol']['min'] <= facts['daemonWorkerProtocol'] <= facts['workerProtocol']['max']:
        raise ValueError('contradictory compatibility declaration')


def generate(tag, commit, out, image_dir, facts):
    validate_identity(tag, commit)
    images = {}
    for name in IMAGES:
        row = json.loads((image_dir / (name + '.json')).read_text())
        if row.pop('version') != tag or row.pop('commit') != commit or row['repository'] != 'ghcr.io/flokkit/' + name:
            raise ValueError('image facts belong to another release')
        # Revalidate facts rather than trusting a producer's JSON shape.
        rebuilt = image_facts(tag, commit, name, row['index'], {'manifests': [
            {'platform': dict(zip(('os', 'architecture'), platform.split('/'))), 'digest': value}
            for platform, value in row['platforms'].items()]})
        rebuilt.pop('version'); rebuilt.pop('commit')
        if row != rebuilt:
            raise ValueError('image facts contain unexpected fields')
        images[name] = row
    validate_compatibility(facts)
    facts_by_platform = {p: archive_facts(out, tag, p) for p in PLATFORMS}
    archives = {p: a for p, (a, _) in facts_by_platform.items()}
    members = {p: m for p, (_, m) in facts_by_platform.items()}
    # Darwin ships the very same Linux CLI its architecture's Linux archive uses.
    for arch in ('amd64', 'arm64'):
        if members['darwin/' + arch]['bin/apptown-linux'] != members['linux/' + arch]['bin/apptown-linux'] or members['linux/' + arch]['bin/apptown'] != members['linux/' + arch]['bin/apptown-linux']:
            raise ValueError('paired Linux CLI differs across archives')
    stock = lambda m: {k: v for k, v in m.items() if k.startswith(('share/apptown/updates/', 'deploy/apptown-agent/', 'deploy/systemd/'))}
    first = stock(members[PLATFORMS[0]])
    if any(stock(m) != first for m in members.values()):
        raise ValueError('paired stock/agent/service content differs across archives')
    notes = (out / 'RELEASE-NOTES.md').read_bytes()
    if not notes.strip() or len(notes) > 256 << 10:
        raise ValueError('release notes are empty or exceed the published limit')
    manifest = {'format': 2, 'version': tag, 'commit': commit,
                'notes': artifact(out / 'RELEASE-NOTES.md'), 'archives': archives,
                'images': images, 'compatibility': facts}
    path = out / 'UPDATE-MANIFEST.json'
    path.write_text(json.dumps(manifest, sort_keys=True, indent=2) + '\n')
    paths = [out / a['name'] for a in archives.values()] + [out / 'RELEASE-NOTES.md', path]
    (out / 'SHA256SUMS').write_text(''.join(artifact(p)['sha256'] + '  ' + p.name + '\n' for p in sorted(paths)))
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    image = sub.add_parser('image')
    for argument in ('tag', 'commit', 'name', 'digest', 'index', 'output'):
        image.add_argument(argument)
    build = sub.add_parser('build')
    for argument in ('tag', 'commit', 'output', 'images', 'facts'):
        build.add_argument(argument)
    args = parser.parse_args()
    if args.command == 'image':
        result = image_facts(args.tag, args.commit, args.name, args.digest, json.loads(Path(args.index).read_text()))
        Path(args.output).write_text(json.dumps(result, sort_keys=True) + '\n')
    else:
        generate(args.tag, args.commit, Path(args.output), Path(args.images), json.loads(Path(args.facts).read_text()))


if __name__ == '__main__':
    main()
