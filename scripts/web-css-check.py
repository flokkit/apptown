"""Keep source CSS inside the declared cascade and small enough to own."""
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
LAYERS = "tokens, base, components, pages, utilities"


def check(root):
    source = root / "web/src"
    errors = []
    for path in sorted(source.rglob("*.css")):
        name = path.relative_to(root)
        text = path.read_text()
        if len(text.splitlines()) > 400:
            errors.append(f"{name}: exceeds 400 lines")
        # Ignore comments and quoted content, including braces in strings.
        plain = re.sub(r'/\*.*?\*/|"(?:\\.|[^"\\])*"|\'(?:\\.|[^\'\\])*\'',
                       lambda m: " " if m[0].startswith("/*") else '""', text, flags=re.S)
        if path == source / "layers.css":
            if plain.strip() != f"@layer {LAYERS};":
                errors.append(f"{name}: must declare @layer {LAYERS};")
            continue
        match = re.match(r"\s*@layer\s+(tokens|base|components|pages|utilities)\s*\{", plain)
        depth = 1
        if match:
            for index in range(match.end(), len(plain)):
                char = plain[index]
                depth += (char == "{") - (char == "}")
                if depth == 0:
                    if plain[index + 1:].strip():
                        match = None
                    break
        if not match or depth != 0:
            errors.append(f"{name}: wrap the whole file in one declared @layer")
    if not (source / "layers.css").exists():
        errors.append("web/src/layers.css: missing layer declaration")
    return errors


if __name__ == "__main__":
    errors = check(Path(sys.argv[1]) if len(sys.argv) > 1 else ROOT)
    print("\n".join(errors) if errors else "web CSS: declared layers; every file at most 400 lines")
    sys.exit(bool(errors))
