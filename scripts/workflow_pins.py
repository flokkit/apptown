"""Read workflow action pins for the offline and network checks."""
import re


RELEASE = r"v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?"
ACTION = re.compile(r"([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)(?:/[A-Za-z0-9_./-]+)?@([0-9a-fA-F]{40})")


def workflow_pins(root):
    """Return (file, line, repository, commit, tag) pins and format errors."""
    pins, errors = [], []
    for path in sorted((root / '.github/workflows').glob('*.yml')):
        rel = path.relative_to(root)
        for line, raw in enumerate(path.read_text().splitlines(), 1):
            match = re.match(r'\s*(?:-\s+)?uses:\s*(.*)', raw)
            if not match:
                continue
            value, _, annotation = match.group(1).partition('#')
            value = value.strip()
            if len(value) >= 2 and value[0] in "\"'" and value[-1] == value[0]:
                value = value[1:-1]
            if value.startswith('./'):
                continue
            if re.fullmatch(r'docker://\S+@sha256:[0-9a-fA-F]{64}', value):
                continue
            action = ACTION.fullmatch(value)
            tag = annotation.strip()
            if not action or not re.fullmatch(RELEASE, tag):
                errors.append(f'{rel}:{line}: uses: needs owner/repo[/path]@<40-hex commit> # vX.Y.Z '
                              '(local ./ actions and docker:// images with a sha256 digest are allowed)')
                continue
            repo, commit = action.groups()
            pins.append((rel, line, repo, commit.lower(), tag))
    return pins, errors
