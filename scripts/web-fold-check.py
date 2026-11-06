"""Page facts are inline or on a linked page; only menus disclose content."""
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MENUS = {'MoreMenu.tsx', 'SplitButton.tsx', 'Popover.tsx', 'TopBar.tsx'}


def counts(root):
    found = {}
    for path in sorted((root / 'web/src').rglob('*.tsx')):
        if '.test.' in path.name or path.name in MENUS:
            continue
        text = re.sub(r'/\*.*?\*/|//[^\n]*', '', path.read_text(), flags=re.S)
        n = len(re.findall(r'<(?:details|Disclosure)\b|createElement\(\s*[\'\"]details[\'\"]|(?:const|let) [A-Z][a-z]\w* = [^\n;]*[\'\"]details[\'\"]', text))
        if n:
            found[str(path.relative_to(root))] = n
    return found


if __name__ == '__main__':
    found = counts(ROOT)
    for path, count in found.items():
        print(f'{path}: {count} page disclosures (allowed: 0)')
    print(f'page disclosures: {sum(found.values())} (allowed: 0)')
    sys.exit(bool(found))
