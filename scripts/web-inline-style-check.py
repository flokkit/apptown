"""Inline style objects and pixel lengths may only decrease, per production file.

Inline type (a weight, a family, a font, or an ink or size not taken from a
token) is counted too, and its count must equal the baseline: text takes its
type from a role class, so a removal lowers the baseline in the same change."""
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
        type_props = 0
        for style in styles:
            type_props += len(re.findall(r'\b(?:fontWeight|fontFamily|font)\s*:', style))
            for value in re.findall(r'\b(?:color|fontSize)\s*:\s*([^,}\n]+)', style):
                value = value.strip()
                if value[:1] in '"\'`' and 'var(--' not in value:
                    type_props += 1
            numeric += len(re.findall(r'\b\d+(?:\.\d+)?px\b', style))
            for prop, value in re.findall(r'\b([A-Za-z][A-Za-z0-9]*)\s*:\s*(-?(?:\d+(?:\.\d+)?|\.\d+))(?=\s*[,}\n]|\s*$)', style):
                if prop not in UNITLESS and float(value) != 0:
                    numeric += 1
        if styles:
            result[str(path.relative_to(root))] = {'styles': len(styles), 'numericPx': numeric, 'typeProps': type_props}
    return result


def check(current, baseline):
    errors = [f'{name}: {kind} {count} exceeds baseline {baseline.get(name, {}).get(kind, 0)}'
              for name, values in current.items() for kind, count in values.items()
              if count > baseline.get(name, {}).get(kind, 0)]
    for name in sorted(set(current) | set(baseline)):
        count = current.get(name, {}).get('typeProps', 0)
        limit = baseline.get(name, {}).get('typeProps', 0)
        if count < limit:
            errors.append(f'{name}: typeProps {count} is below baseline {limit}; lower the baseline')
    return errors


if __name__ == '__main__':
    current = counts(ROOT)
    errors = check(current, json.loads((ROOT / 'scripts/web-style-baseline.json').read_text()))
    print('\n'.join(errors) if errors else f"web inline styles: {sum(v['styles'] for v in current.values())} objects, {sum(v['numericPx'] for v in current.values())} pixel lengths; within baseline")
    sys.exit(bool(errors))
