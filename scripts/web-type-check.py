"""Keep typography on the shared, scalable type and space tokens."""
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def check(root):
    errors = []
    for path in sorted((root / "web/src").rglob("*")):
        if path.suffix not in (".css", ".tsx"):
            continue
        source = path.read_text()
        # Preserve line numbers, but do not check examples in comments.
        text = re.sub(r'/\*.*?\*/', lambda m: '\n' * m[0].count('\n'), source, flags=re.S)

        def fail(index, message):
            line = text.count('\n', 0, index) + 1
            errors.append(f"{path.relative_to(root)}:{line}: {message}")

        if path.suffix == ".css":
            for match in re.finditer(r'(?<![\w-])(font-size|line-height)\s*:\s*([^;}]+)', text, re.I):
                if re.search(r'[\d.]\s*px\b', match[2], re.I):
                    fail(match.start(), f"{match[1]} must not use px")
                elif match[1].lower() == 'line-height' and re.search(r'\d\s*(?:rem\b|em\b|%|pt\b|vh\b|vw\b)', match[2], re.I):
                    fail(match.start(), "line-height must be unitless")
            for match in re.finditer(r'(--(?:fs|space)-[\w-]+)\s*:\s*([^;}]+)', text):
                value = match[2].strip()
                if match[1] == '--fs-field' and re.fullmatch(r'max\(1rem,\s*16px\)', value):
                    continue
                # A rem length, or N px of the 14px Small reference: calc(Nrem / 14).
                if not re.fullmatch(r'(?:\d+(?:\.\d+)?|\.\d+)rem|calc\(\d+(?:\.\d+)?rem / 14\)', value):
                    fail(match.start(), f"{match[1]} must be defined in rem")
        else:
            # A number or arithmetic expression in a React fontSize prop becomes px.
            # Quoted strings (tokens, inherit, etc.) are CSS values, not JS numbers.
            for style in re.finditer(r'\bstyle\s*=\s*\{\{(.*?)\}\}', text, re.S):
                for match in re.finditer(r'\bfontSize\s*:\s*([^,}\n]+)', style[1]):
                    value = match[1].strip()
                    if not value.startswith(('"', "'", '`')):
                        fail(style.start(1) + match.start(), "fontSize must use a CSS token or class")
    return errors


if __name__ == "__main__":
    errors = check(Path(sys.argv[1]) if len(sys.argv) > 1 else ROOT)
    print("\n".join(errors) if errors else "web type: scalable tokens; no pixel font sizes or line heights")
    sys.exit(bool(errors))
