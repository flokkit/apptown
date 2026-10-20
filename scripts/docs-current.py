#!/usr/bin/env python3
"""Current guides state the current rule, not its history (CLAUDE.md).

Under design/, outside plans/ and reviews/ (dated working papers), in
stock/docs/ (the guides AppTown ships) and in CLAUDE.md, this fails, naming
the file and line, on:

  - a file or folder name with a date in it (2026-10-19, 2026-10, 20261019);
  - a Markdown link into design/plans/ or design/reviews/;
  - a line starting "Status:" or "> Superseded".

In stock/docs/ and in the guidance AppTown gives agents (stock/guidance/, the
body and its checks) it also fails on the old product name in prose: "the forge",
"this forge", "a forge", "forge's" or "Forge". The product is AppTown; the
command `apptown`, `.apptown/`, `FORGE_*` and the like are code and are written
in backticks, which this skips, as it skips fenced and indented code.

The reading of a guide for history in its sentences stays a reviewer's job;
this holds only the parts a machine can see.

    python3 scripts/docs-current.py [root]
"""
import re
import sys
from pathlib import Path

DATED = re.compile(r"(?<!\d)(20\d\d-\d\d(-\d\d)?|20\d{6})(?!\d)")
# An inline link, with or without a title, and a reference definition.
LINK = re.compile(r"\]\(\s*<?([^)\s>]+)>?(?:\s+[\"'(][^)]*)?\)")
REFERENCE = re.compile(r"^\s{0,3}\[[^\]]+\]:\s*<?([^\s>]+)>?")
STATUS = re.compile(r"^(Status:|> Superseded)")
WORKING_PAPERS = ("plans", "reviews")


# The old product name as a word in prose, across a line break too.
# A hyphen, or a dot or colon joined to a word (forge.flokkit.io, apptown:mcp),
# makes it a code name; sentence punctuation does not.
OLD_NAME = re.compile(r"\b(?:[Tt]he|[Tt]his|[Aa])\s+forge\b(?!-|[.:]\w)|\b[Ff]orge's\b|\bForge\b(?!-|[.:]\w)")
# What is not prose: fenced blocks, indented code, code spans, live-link
# placeholders such as {apptown:mcp}, link targets and bare addresses.
CODE = re.compile(r"```.*?```|^    [^\n]*$|`[^`\n]*`|\{apptown:[a-z]+\}|\]\([^)]*\)|https?://\S+", re.S | re.M)


def current_guides(root):
    design = root / "design"
    for path in sorted(design.rglob("*")):
        rel = path.relative_to(design)
        if rel.parts and rel.parts[0] in WORKING_PAPERS:
            continue
        yield path
    shipped = root / "stock" / "docs"
    if shipped.exists():
        yield from sorted(shipped.rglob("*"))
    claude = root / "CLAUDE.md"
    if claude.exists():
        yield claude


def link_target(source, target, root):
    target = target.split("#", 1)[0]
    if not target or "://" in target or target.startswith("mailto:"):
        return None
    try:
        return (source.parent / target).resolve().relative_to(root.resolve()).as_posix()
    except ValueError:
        return None


def old_names(text):
    """Line numbers of the old product name in prose."""
    prose = CODE.sub(lambda m: re.sub(r"[^\n]", " ", m.group(0)), text)
    return [prose.count("\n", 0, m.start()) + 1 for m in OLD_NAME.finditer(prose)]


def problems(root):
    out = []
    for path in current_guides(root):
        rel = path.relative_to(root).as_posix()
        if DATED.search(path.name):
            out.append(f"{rel}: a current guide's name has a date in it; git history dates it")
        if path.is_dir() or path.suffix != ".md":
            continue
        if rel.startswith("stock/docs/"):
            for n in old_names(path.read_text()):
                out.append(f"{rel}:{n}: says forge for the product; write AppTown, or put the command in backticks")
        for n, line in enumerate(path.read_text().splitlines(), 1):
            if STATUS.match(line):
                out.append(f"{rel}:{n}: a status line; state the current rule instead")
            for target in LINK.findall(line) + REFERENCE.findall(line):
                to = link_target(path, target, root)
                if to and to.startswith(("design/plans/", "design/reviews/")):
                    out.append(f"{rel}:{n}: links a plan or review ({to}); write the reason into the guide")
    guidance = root / "stock" / "guidance"
    if guidance.exists():
        for path in sorted(guidance.rglob("*.md")):
            rel = path.relative_to(root).as_posix()
            for n in old_names(path.read_text()):
                out.append(f"{rel}:{n}: says forge for the product; write AppTown, or put the command in backticks")
    return out


def main():
    root = Path(sys.argv[1] if len(sys.argv) > 1 else ".")
    found = problems(root)
    for p in found:
        print(p)
    if found:
        print(f"docs-current: {len(found)} problem(s); see CLAUDE.md, \"Writing and maintaining guidance\"")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
