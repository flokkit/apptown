#!/usr/bin/env python3
"""Links in the shipped guides and the release notes work where they are read.

The guides AppTown ships (stock/docs/guides/, stock/docs/integrations/) are
read in two places: on GitHub, as files in the repository, and on an
AppTown's Docs page, which serves each at its own route
(/docs/guides/<slug>, /docs/<capability>/<provider>). A link has to work in
both, so a shipped doc links to another shipped doc by its file, relatively
(`split-install.md`, `../integrations/registry-registry.admin.md`): GitHub
follows that, and the Docs page maps it to its route when it renders
(web/src/docRoutes.ts). This fails, naming the file and line, on:

  - a root path (`/docs/guides/x`, `/projects`): a route of the app, which
    GitHub resolves inside the repository, where it is a 404. Name a page of
    the app in words;
  - a relative link to anything but an existing shipped .md doc
    (`../../../design/x.md`, `missing.md`): the Docs page serves only the
    shipped docs. Link another repository file by its full address,
    https://github.com/flokkit/apptown/blob/main/<path>;
  - an anchor or query on a relative link: the Docs page's headings have no
    ids;
  - a live address ({apptown:mcp}) as a link target: GitHub shows it as a
    dead link. Write it in prose or code, where the Docs page fills it in.

The release notes (releases/*.md) are read on GitHub as files and as the body
of the GitHub Release, where a relative link does not resolve; there every
link into the repository is a full https://github.com/flokkit/apptown/blob/main/
address, and a relative or root link fails.

A shipped doc's own name must be one the Docs page routes (guides/<slug>.md,
integrations/<capability>-<provider>.(admin|developer).md), so that the
check and the page agree on what a shipped doc is.

Links are the ones the renderer makes: scripts/docs-links-extract.mjs reads
each file with the Docs page's own Markdown library (marked, from web/), so
it needs the web package's dependencies installed.

In both, a https://github.com/flokkit/apptown/blob/main/<path> (or tree/main/)
address fails when <path> is not in the tree. Other web addresses are not
fetched.

    python3 scripts/docs-links.py [root]
"""
import json
import posixpath
import re
import subprocess
import sys
from urllib.parse import unquote
from pathlib import Path

# The renderer's links, read by the renderer (marked) itself.
EXTRACT = Path(__file__).with_name("docs-links-extract.mjs")
SCHEME = re.compile(r"^[a-zA-Z][a-zA-Z0-9+.-]*:")
REPO = re.compile(r"^https://github\.com/flokkit/apptown/(?:blob|tree)/main/([^?#]*)")

# The shipped docs the Docs page serves, by their path in the repository;
# web/src/docRoutes.ts maps the same names to routes.
SHIPPED = re.compile(r"^stock/docs/(guides/[a-z0-9-]+|integrations/[a-z0-9]+-[a-z0-9-]+\.(admin|developer))\.md$")


def links(paths):
    """{path: [(line, target)]} for every link the renderer makes in paths."""
    out = {p: [] for p in paths}
    if not paths:
        return out
    r = subprocess.run(["node", str(EXTRACT), *map(str, paths)], capture_output=True, text=True)
    if r.returncode != 0:
        sys.exit(r.stderr.strip() or f"docs-links: {EXTRACT.name} failed ({r.returncode})")
    for row in r.stdout.splitlines():
        link = json.loads(row)
        # marked percent-encodes a target ({apptown:mcp} becomes %7B...).
        out[Path(link["file"])].append((link["line"], unquote(link["href"])))
    return out


def shipped_doc(rel):
    return bool(SHIPPED.match(rel))


def problem(root, rel, target, release_notes):
    """What is wrong with one link in the file rel, or None."""
    if "{apptown:" in target:
        return "a live address as a link target is a dead link on GitHub; write it in prose, where the Docs page fills it in"
    repo = REPO.match(target)
    if repo:
        path = repo.group(1).rstrip("/")
        if not (root / path).exists():
            return f"names {path}, which is not in the repository"
        return None
    if SCHEME.match(target) or target.startswith("//"):
        return None
    if release_notes:
        return "release notes are also a GitHub Release's text, where this does not resolve; link https://github.com/flokkit/apptown/blob/main/<path>"
    if target.startswith("/"):
        return "a root path is a route of the app, a 404 on GitHub; link the doc's file relatively (split-install.md), or name an app page in words"
    if not target or "#" in target or "?" in target:
        return "an anchor or query: the Docs page's headings have no ids; link the doc's file alone"
    to = posixpath.normpath(posixpath.join(posixpath.dirname(rel), target))
    if not shipped_doc(to):
        return f"resolves to {to}, which the Docs page does not serve; link a shipped doc, or the file's https://github.com/flokkit/apptown/blob/main/ address"
    if not (root / to).is_file():
        return f"resolves to {to}, which does not exist"
    return None


def checked(root):
    for d in ("stock/docs/guides", "stock/docs/integrations"):
        yield from sorted((root / d).glob("*.md"))
    yield from sorted((root / "releases").glob("*.md"))


def problems(root):
    out = []
    paths = list(checked(root))
    found = links(paths)
    for path in paths:
        rel = path.relative_to(root).as_posix()
        if rel.startswith("stock/") and not shipped_doc(rel):
            out.append(f"{rel}: a name the Docs page has no route for; a guide is <slug>.md, an integration document <capability>-<provider>.admin.md or .developer.md")
        for n, target in found[path]:
            why = problem(root, rel, target, rel.startswith("releases/"))
            if why:
                out.append(f"{rel}:{n}: ({target}) {why}")
    return out


def main():
    root = Path(sys.argv[1] if len(sys.argv) > 1 else ".")
    found = problems(root)
    for p in found:
        print(p)
    if found:
        print(f"docs-links: {len(found)} link(s) that would not work on GitHub or the Docs page; see scripts/docs-links.py")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
