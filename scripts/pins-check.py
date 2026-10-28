#!/usr/bin/env python3
"""The versions of the tools the apptown runs for projects agree with deploy/tool-pins.txt.

Fails, naming file and line, where a listed tool is pinned at another version,
where the agent Dockerfile or stock/scanners.yaml pins a tool the list does
not name, and where a listed tool is pinned nowhere. The list's own header
says what each line means.

Also fails where a Dockerfile AppTown builds itself starts FROM an image
without a digest: a tag can be moved to other contents, a digest cannot.

    python3 scripts/pins-check.py [root]
"""
import os
import re
import sys
from pathlib import Path

PINS = "deploy/tool-pins.txt"
DOCKERFILE = "deploy/apptown-agent/Dockerfile"
SCANNERS = "stock/scanners.yaml"


def load_pins(root):
    """Answers [(tool, version, [(kind, value)], line)] and the list's errors."""
    tools, errors = [], []
    for n, raw in enumerate((root / PINS).read_text().splitlines(), 1):
        line = raw.split("#", 1)[0].strip()
        if not line:
            continue
        parts = line.split(None, 2)
        if len(parts) < 3:
            errors.append(f"{PINS}:{n}: a line needs a tool, a version and where it is pinned")
            continue
        sites = []
        for site in parts[2].split(","):
            kind, _, value = site.strip().partition(" ")
            if kind not in ("image", "arg") or not value.strip():
                errors.append(f"{PINS}:{n}: {site.strip()!r} is not 'image <repository>' or 'arg <NAME>'")
                continue
            sites.append((kind, value.strip()))
        tools.append((parts[0], parts[1], sites, n))
    return tools, errors


def scanned(root):
    """The shipped files that pin these tools. Tests and testdata use
    arbitrary versions as fixtures; seeds belong to the projects made from
    them."""
    files = [root / DOCKERFILE]
    files += [p for p in sorted((root / "stock").rglob("*.yaml")) if "seeds" not in p.relative_to(root / "stock").parts[:1]]
    for top in ("internal", "cmd"):
        files += [p for p in sorted((root / top).rglob("*.go"))
                  if not p.name.endswith("_test.go") and "testdata" not in p.parts]
    return [p for p in files if p.is_file()]


def version_of(tag):
    """A tag's version, or None when the tag is not one (docker:dind)."""
    if not re.match(r"v?[0-9]", tag):
        return None
    return tag.split("-", 1)[0]


def comment(line):
    """A full-line comment gives examples and history, not pins."""
    return line.lstrip().startswith(("#", "//"))


def image_refs(text, repo):
    pattern = re.compile(r"(?<![A-Za-z0-9._/-])" + re.escape(repo) + r":([A-Za-z0-9._-]+)")
    for n, line in enumerate(text.splitlines(), 1):
        if comment(line):
            continue
        for m in pattern.finditer(line):
            v = version_of(m.group(1))
            if v is not None:
                yield n, v


def repository(ref):
    """The repository of an image reference: node:24-slim@sha256:… is node."""
    ref = ref.split("@", 1)[0]
    head, _, tail = ref.rpartition(":")
    return head if head and "/" not in tail else ref


def own_dockerfiles(root):
    """The Dockerfiles AppTown builds itself. Seeds belong to the projects made
    from them, testdata holds fixtures, and private/ is not in the public
    repository, whose CI must check the same files a maintainer does."""
    skip = {"node_modules", "testdata", ".git", "private"}
    found = []
    for d, dirs, files in os.walk(root):
        rel = Path(d).relative_to(root)
        dirs[:] = sorted(x for x in dirs if x not in skip and (rel / x).parts[:2] != ("stock", "seeds"))
        found += [Path(d) / f for f in files if "Dockerfile" in f and not f.endswith(".dockerignore")]
    return sorted(found)


def unpinned_bases(root):
    """Every FROM that names no digest. scratch and an earlier stage's name
    are not images; FROM ${NAME} is read through the file's ARG NAME= default."""
    errors = []
    for p in own_dockerfiles(root):
        rel = p.relative_to(root)
        args, stages = {}, set()
        for n, raw in enumerate(p.read_text().splitlines(), 1):
            raw = raw.strip()
            m = re.match(r"ARG\s+(\w+)=(\S+)", raw)
            if m:
                args[m.group(1)] = (m.group(2), n)
                continue
            m = re.match(r"FROM\s+(?:--\S+\s+)*(\S+)(?:\s+AS\s+(\S+))?", raw, re.IGNORECASE)
            if not m:
                continue
            ref, line = m.group(1), n
            v = re.fullmatch(r"\$\{?(\w+)\}?", ref)
            if v:
                if v.group(1) not in args:
                    errors.append(f"{rel}:{n}: FROM {ref} has no ARG default to read a digest from")
                    ref = None
                else:
                    ref, line = args[v.group(1)]
            if ref is not None and ref != "scratch" and ref not in stages and "@sha256:" not in ref:
                errors.append(f"{rel}:{line}: the base image {ref} names no digest; pin it as {ref}@sha256:<digest>")
            if m.group(2):
                stages.add(m.group(2))
    return errors


def checksum_versions(root):
    """A checksum's Renovate release tag must name the downloaded version.

    Renovate updates architecture digests separately. Keeping the tag beside
    each digest lets it replace both without overlapping the shared ARG.
    """
    errors = []
    for rel in (DOCKERFILE, ".github/workflows/ci.yml"):
        path = root / rel
        if not path.is_file():
            continue
        text = path.read_text()
        versions = dict(re.findall(r"(?:ARG |^[ \t]*)(\w+)_VERSION(?:=|: )([^\s]+)", text, re.M))
        for match in re.finditer(
                r"# renovate: checksum=\S+ version=(\S+)\n"
                r"(?:ARG |[ \t]*)(\w+)_SHA256(?:_\w+)?(?:=|: )([a-f0-9]{64})", text):
            tag, tool, _ = match.groups()
            version = versions.get(tool)
            if version is None or tag.removeprefix("v") != version.removeprefix("v"):
                line = text.count("\n", 0, match.start()) + 1
                errors.append(f"{rel}:{line}: {tool} checksum release {tag} differs from version {version}")
    return errors


def check(root):
    tools, errors = load_pins(root)
    texts = {p: p.read_text() for p in scanned(root)}
    images = {value: tool for tool, _, sites, _ in tools for kind, value in sites if kind == "image"}
    args = {value: tool for tool, _, sites, _ in tools for kind, value in sites if kind == "arg"}
    dockerfile = root / DOCKERFILE
    found = set()

    for tool, version, sites, n in tools:
        for kind, value in sites:
            if kind == "image":
                for path, text in texts.items():
                    for line, v in image_refs(text, value):
                        found.add((tool, kind, value))
                        if v != version:
                            errors.append(f"{path.relative_to(root)}:{line}: {tool} is pinned at {v} ({value}); {PINS} says {version}")
            else:
                text = texts.get(dockerfile, "")
                for line, raw in enumerate(text.splitlines(), 1):
                    m = re.match(r"ARG\s+" + re.escape(value) + r"=(\S+)", raw.strip())
                    if m:
                        found.add((tool, kind, value))
                        if m.group(1) != version:
                            errors.append(f"{DOCKERFILE}:{line}: {tool} is pinned at {m.group(1)} ({value}); {PINS} says {version}")
            if (tool, kind, value) not in found:
                errors.append(f"{PINS}:{n}: {tool} is not pinned as {kind} {value} anywhere the check reads")

    # The two places that define what the apptown runs pin nothing the list
    # leaves out.
    for line, raw in enumerate(texts.get(dockerfile, "").splitlines(), 1):
        raw = raw.strip()
        m = re.match(r"ARG\s+(\w+_VERSION)=", raw)
        if m and m.group(1) not in args:
            errors.append(f"{DOCKERFILE}:{line}: {m.group(1)} is not in {PINS}")
        m = re.match(r"FROM\s+(?:--\S+\s+)*(\S+)", raw)
        if m and repository(m.group(1)) not in images:
            errors.append(f"{DOCKERFILE}:{line}: the image {repository(m.group(1))} is not in {PINS}")
    scanners = root / SCANNERS
    for line, raw in enumerate(texts.get(scanners, "").splitlines(), 1):
        m = re.match(r"\s*image:\s*[\"']?([^\s\"'#]+)", raw)
        if m and repository(m.group(1)) not in images:
            errors.append(f"{SCANNERS}:{line}: the image {repository(m.group(1))} is not in {PINS}")
    errors += unpinned_bases(root)
    errors += checksum_versions(root)
    return errors


def main(argv):
    root = Path(argv[1]) if len(argv) > 1 else Path(".")
    errors = check(root)
    for e in errors:
        print(e)
    if errors:
        print(f"{len(errors)} problem(s): tool versions that disagree with {PINS}, or base images without a digest")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
