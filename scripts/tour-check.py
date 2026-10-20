#!/usr/bin/env python3
"""The tour's shape (site/tour), cheap enough for every gate: every stop in
tour.yaml has an id, a title, a caption and a path; both its shots exist
and are exactly 2880x1800 (1440x900 at device scale 2); no shot belongs to
no stop; and index.html shows exactly the stops, in order. Whether the shots
are current is the release script's question (scripts/tour-stamp.py)."""
import os
import re
import struct
import sys

WIDTH, HEIGHT = 2880, 1800


KEYS = {"id", "title", "caption", "path", "wait"}
# A plain value YAML reads as the same string: no ": " or " #" inside, and
# nothing YAML gives a meaning at the start. Anything else is double-quoted.
PLAIN = re.compile(r"""^(?![-?:,\[\]{}#&*!|>'"%@`\s])(?!.*: )(?!.* #).*\S$""")
QUOTED = re.compile(r'^"([^"\\]*)"$')


def stops(text):
    """The stops of tour.yaml, and the lines outside the shape this file
    keeps: a top-level stops: list of flat key: value maps whose values are
    plain or double-quoted strings. The gate needs no YAML library, so it
    accepts only text that YAML (the camera's parser) reads the same way."""
    out, cur, bad = [], None, []
    for n, line in enumerate(text.splitlines(), 1):
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if line == "stops:":
            continue
        m = re.match(r"^  - (\w+): (.*)$", line) or re.match(r"^    (\w+): (.*)$", line)
        value = m and value_of(m.group(2))
        if not m or m.group(1) not in KEYS or value is None:
            bad.append(f"tour.yaml line {n}: not a key: value line of this file's shape (quote a value with a colon in it)")
            continue
        if line.startswith("  - "):
            cur = {}
            out.append(cur)
        if cur is None:
            bad.append(f"tour.yaml line {n}: a key outside a stop")
            continue
        if m.group(1) in cur:
            bad.append(f"tour.yaml line {n}: {m.group(1)} given twice in one stop")
            continue
        cur[m.group(1)] = value
    return out, bad


def value_of(v):
    q = QUOTED.match(v)
    if q:
        return q.group(1)
    return v if PLAIN.match(v) else None


def png_size(path):
    with open(path, "rb") as f:
        head = f.read(24)
    if head[:8] != b"\x89PNG\r\n\x1a\n":
        return None
    return struct.unpack(">II", head[16:24])


def problems(tour):
    out = []
    try:
        listed, bad = stops(open(os.path.join(tour, "tour.yaml")).read())
    except OSError as e:
        return [f"tour.yaml: {e}"]
    out.extend(bad)
    if not listed:
        out.append("tour.yaml lists no stops")
    ids = []
    for i, s in enumerate(listed, 1):
        for key in ("id", "title", "caption", "path"):
            if not s.get(key):
                out.append(f"stop {i}: no {key}")
        sid = s.get("id", "")
        if sid in ids:
            out.append(f"stop {sid}: listed twice")
        ids.append(sid)
        for theme in ("light", "dark"):
            png = os.path.join(tour, f"{sid}-{theme}.png")
            if not os.path.exists(png):
                out.append(f"stop {sid}: no {sid}-{theme}.png (run make tour)")
            elif png_size(png) != (WIDTH, HEIGHT):
                out.append(f"{sid}-{theme}.png is {png_size(png)}, not {WIDTH}x{HEIGHT}")
    wanted = {f"{i}-{t}.png" for i in ids for t in ("light", "dark")}
    for name in sorted(os.listdir(tour)):
        if name.endswith(".png") and name not in wanted:
            out.append(f"{name} belongs to no stop")
    try:
        page = open(os.path.join(tour, "index.html")).read()
        shown = re.findall(r'<section id="([^"]+)">', page)
        if shown != ids:
            out.append(f"index.html shows {shown}, tour.yaml lists {ids} (run make tour)")
    except OSError:
        out.append("no index.html (run make tour)")
    return out


def main():
    root = os.environ.get("TOUR_ROOT") or os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    found = problems(os.path.join(root, "site", "tour"))
    for p in found:
        print("tour: " + p, file=sys.stderr)
    return 1 if found else 0


if __name__ == "__main__":
    sys.exit(main())
