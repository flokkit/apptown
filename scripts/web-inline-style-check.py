"""Inline style objects and pixel lengths may only decrease, per production file."""
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
# Numeric React styles for these properties are unitless, not pixel lengths.
UNITLESS = set('animationIterationCount aspectRatio borderImageOutset borderImageSlice borderImageWidth columnCount columns fillOpacity flex flexGrow flexShrink fontWeight gridArea gridColumn gridColumnEnd gridColumnSpan gridColumnStart gridRow gridRowEnd gridRowSpan gridRowStart lineHeight opacity order orphans scale strokeDasharray strokeDashoffset strokeMiterlimit strokeOpacity strokeWidth tabSize widows zIndex zoom'.split())


def counts(root):
    result = {}
    for path in sorted((root / 'web/src').rglob('*.tsx')):
        if '.test.' in path.name:
            continue
        text = re.sub(r'/\*.*?\*/', '', path.read_text(), flags=re.S)
        styles = re.findall(r'\bstyle\s*=\s*\{\{(.*?)\}\}', text, re.S)
        numeric = 0
        for style in styles:
            numeric += len(re.findall(r'\b\d+(?:\.\d+)?px\b', style))
            for prop, value in re.findall(r'\b([A-Za-z][A-Za-z0-9]*)\s*:\s*(-?(?:\d+(?:\.\d+)?|\.\d+))(?=\s*[,}\n]|\s*$)', style):
                if prop not in UNITLESS and float(value) != 0:
                    numeric += 1
        if styles:
            result[str(path.relative_to(root))] = {'styles': len(styles), 'numericPx': numeric}
    return result


def check(current, baseline):
    return [f'{name}: {kind} {count} exceeds baseline {baseline.get(name, {}).get(kind, 0)}'
            for name, values in current.items() for kind, count in values.items()
            if count > baseline.get(name, {}).get(kind, 0)]


if __name__ == '__main__':
    current = counts(ROOT)
    errors = check(current, json.loads((ROOT / 'scripts/web-style-baseline.json').read_text()))
    print('\n'.join(errors) if errors else f"web inline styles: {sum(v['styles'] for v in current.values())} objects, {sum(v['numericPx'] for v in current.values())} pixel lengths; within baseline")
    sys.exit(bool(errors))
