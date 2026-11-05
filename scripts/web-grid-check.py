"""Fixed pixel grid tracks may only be removed, including in new files."""
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def counts(root):
    result = {}
    for path in sorted((root / 'web/src').rglob('*')):
        if path.suffix not in ('.css', '.tsx') or '.test.' in path.name:
            continue
        text = re.sub(r'/\*.*?\*/|//[^\n]*', '', path.read_text(), flags=re.S)
        values = re.findall(r'grid-template-columns\s*:\s*([^;}]+)', text)
        values += re.findall(r'gridTemplateColumns\s*:\s*[\'"`]([^\'"`]+)', text)
        count = sum(len(re.findall(r'\b\d+(?:\.\d+)?px\b', value)) for value in values)
        if count:
            result[str(path.relative_to(root))] = count
    return result


def check(current, baseline):
    return [f'{name}: {count} px grid tracks exceeds baseline {baseline.get(name, 0)}'
            for name, count in current.items() if count > baseline.get(name, 0)]


if __name__ == '__main__':
    current = counts(ROOT)
    errors = check(current, json.loads((ROOT / 'scripts/web-grid-baseline.json').read_text()))
    print('\n'.join(errors) if errors else f'web grids: {sum(current.values())} px tracks, within baseline')
    sys.exit(bool(errors))
