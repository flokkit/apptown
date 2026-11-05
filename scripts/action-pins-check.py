#!/usr/bin/env python3
"""Check workflow release comments against GitHub tags (network, heavy CI).

    python3 scripts/action-pins-check.py [root]
"""
import subprocess
import sys
from pathlib import Path

from workflow_pins import workflow_pins


def resolve_tag(repo, tag):
    """An annotated tag's peeled commit takes precedence over its tag object."""
    ref = f'refs/tags/{tag}'
    result = subprocess.run(
        ['git', 'ls-remote', '--tags', f'https://github.com/{repo}', ref, ref + '^{}'],
        capture_output=True, text=True, check=True, timeout=60)
    refs = dict((name, commit.lower()) for commit, name in
                (line.split() for line in result.stdout.splitlines()))
    return refs.get(ref + '^{}', refs.get(ref))


def check(root, resolver=resolve_tag):
    pins, errors = workflow_pins(root)
    resolved = {}
    for path, line, repo, commit, tag in pins:
        key = repo, tag
        if key not in resolved:
            try:
                resolved[key] = (resolver(repo, tag), None)
            except (OSError, subprocess.SubprocessError) as exc:
                resolved[key] = (None, str(exc))
        actual, failure = resolved[key]
        if failure is not None:
            errors.append(f'{path}:{line}: cannot resolve {repo}@{tag}: {failure}')
        elif actual is None:
            errors.append(f'{path}:{line}: {repo} has no tag {tag}')
        elif actual.lower() != commit:
            errors.append(f'{path}:{line}: {repo}@{tag} points at {actual}, pinned commit is {commit}')
    return errors


def main(argv):
    errors = check(Path(argv[1]) if len(argv) > 1 else Path('.'))
    for error in errors:
        print(error)
    return 1 if errors else 0


if __name__ == '__main__':
    sys.exit(main(sys.argv))
