#!/usr/bin/env python3
"""codebase-shape: how big the codebase is, over time and by module.

Standalone prototype of an AppTown feature. Reads git only, writes one HTML file
with two figures: code lines over time stacked by module, and a treemap of
the tree at HEAD. No dependencies beyond git and Python 3.

    scripts/codebase-shape.py [--repo .] [--out codebase-shape.html] [--since 2026-01-01]

The evaluation harness (eval/) and the repository's own scripts are harness,
not product, and are not measured unless --with-tooling says so.
"""
import argparse, collections, colorsys, datetime as dt, fnmatch, html, json, os, re, subprocess, sys

CODE = {".go", ".ts", ".tsx", ".js", ".css", ".py", ".sh", ".sql"}
DOCS = {".md"}
DATA = {".yaml", ".yml", ".toml", ".json"}
# Directories whose name anywhere in the path means "not ours".
SKIP_SEGMENTS = {"node_modules", "dist", "vendor", ".git", "__pycache__"}
# Trees skipped by their place in the repository, not their name.
SKIP_PREFIXES = ("design/experiments/",)
# Harness, not product: the evaluation corpora and their runners, and the
# repository's own scripts. Measured with --with-tooling.
TOOLING_PREFIXES = ("eval/", "scripts/")
SKIP_FILES = ("package-lock.json", "go.sum")
# Fixed categorical order (dataviz reference palette); a module keeps its slot.
LIGHT = ["#2a78d6", "#eb6834", "#1baf7a", "#eda100", "#e87ba4", "#008300", "#4a3aa7", "#e34948"]
DARK = ["#3987e5", "#d95926", "#199e70", "#c98500", "#d55181", "#008300", "#9085e9", "#e66767"]
MAX_SERIES = 7  # in the area chart only: the rest fold into Other
# The treemap shows every module, so it needs more colours than the eight bands
# the area chart can carry. The eight are extended to sixteen hues (the gaps
# between them, filled), then the sixteen are shaded once away from the page's
# ground -- darker on the light page, lighter on the dark one -- for thirty-two
# in all. The first eight slots stay the palette's own, so the modules that are
# bands in the area chart are the same colour in both figures.
SHADE_LIGHT = (-0.16, 1.0)  # (lightness shift, saturation factor)
SHADE_DARK = (0.16, 0.85)
# Generated code is not counted as code: the Go convention's header (a
# declaration the file makes) and linguist-generated in .gitattributes (a
# declaration the project makes), the GitHub convention.
GENERATED_HEADER = re.compile(rb"^// Code generated .* DO NOT EDIT\.$", re.M)


def git(repo, *args, input=None):
    return subprocess.run(["git", "-C", repo, *args], input=input, capture_output=True, check=True).stdout


# Libraries sit at internal/<x>; forge's subsystems moved from there to
# internal/forge/<x> on 2026-09-13. History is read under today's names so a
# subsystem's series does not restart at zero when its directory moved.
LIBRARIES = {"console", "database", "dbtest", "deploy", "execution", "progress", "stamp"}
def module_of(path):
    parts = path.split("/")
    if parts[0] == "internal" and len(parts) > 2 and parts[1] not in LIBRARIES and parts[1] != "forge":
        parts = ["internal", "forge", *parts[1:]]
    if parts[:2] == ["internal", "forge"] and len(parts) > 3:
        return "/".join(parts[:3])  # forge's subsystems, apart from the App's own files
    if parts[0] in ("internal", "cmd", "web") and len(parts) > 2:
        return "/".join(parts[:2]) if parts[0] != "web" else "web/src"
    return parts[0] if len(parts) > 1 else "(root)"


def shade(hexcolor, dl, ds):
    r, g, b = (int(hexcolor[i : i + 2], 16) / 255 for i in (1, 3, 5))
    h, l, s = colorsys.rgb_to_hls(r, g, b)
    l = min(0.94, max(0.08, l + dl))
    s = min(1.0, s * ds)
    return "#%02x%02x%02x" % tuple(round(c * 255) for c in colorsys.hls_to_rgb(h, l, s))


def hue_gap(a, b):
    d = abs(a - b) % 1.0
    return min(d, 1.0 - d)


def between(base):
    """The eight hues that sit in the gaps between the palette's eight.

    Ordered so that neighbouring slots are far apart on the wheel: the treemap
    lays modules out by size, so consecutive slots end up side by side.
    """
    hsl = sorted(colorsys.rgb_to_hls(*(int(c[i : i + 2], 16) / 255 for i in (1, 3, 5))) for c in base)
    mid = []
    for i, (h, l, s) in enumerate(hsl):
        nh, nl, ns = hsl[(i + 1) % len(hsl)]
        if nh < h:
            nh += 1.0
        mid.append((((h + nh) / 2) % 1.0, (l + nl) / 2, (s + ns) / 2))
    last = colorsys.rgb_to_hls(*(int(base[-1][i : i + 2], 16) / 255 for i in (1, 3, 5)))[0]
    start = max(range(len(mid)), key=lambda i: hue_gap(mid[i][0], last))  # away from the slot before
    out = []
    for i in range(len(mid)):
        h, l, s = mid[(start + 3 * i) % len(mid)]  # 3 and 8 are coprime: every hue, in big steps
        out.append("#%02x%02x%02x" % tuple(round(c * 255) for c in colorsys.hls_to_rgb(h, l, s)))
    return out


def ramp(base, shift):
    """Thirty-two: the palette, the hues between, then both a shade away."""
    hues = base + between(base)
    return hues + [shade(c, *shift) for c in hues]


SKIP_TREES = SKIP_PREFIXES + TOOLING_PREFIXES  # rebound by main() under --with-tooling


def kind_of(path):
    ext = os.path.splitext(path)[1]
    parts = path.split("/")
    if any(part in SKIP_SEGMENTS for part in parts[:-1]) or path.startswith(SKIP_TREES):
        return None
    if os.path.basename(path) in SKIP_FILES:
        return None
    if ext in CODE:
        return "test" if path.endswith("_test.go") or ".test." in path else "code"
    if ext in DOCS:
        return "docs"
    if ext in DATA:
        return "data"
    return None


def sample_commits(repo, since):
    out = git(repo, "log", "--first-parent", "--format=%H %ct", f"--since={since}").decode()
    by_day = {}
    for line in out.splitlines():
        sha, ts = line.split()
        day = dt.datetime.fromtimestamp(int(ts), dt.timezone.utc).date()
        by_day.setdefault(day, sha)  # log is newest first: first seen is the last of the day
    return sorted(by_day.items())


def tree(repo, sha):
    files = []
    for line in git(repo, "ls-tree", "-r", "-z", sha).decode().split("\0"):
        if not line:
            continue
        meta, path = line.split("\t", 1)
        _, typ, blob = meta.split()
        if typ == "blob":
            files.append((path, blob))
    return files


def count_lines(repo, blobs, cache):
    need = [b for b in blobs if b not in cache]
    if not need:
        return
    out = git(repo, "cat-file", "--batch", input=("\n".join(need) + "\n").encode())
    i = 0
    for b in need:
        nl = out.index(b"\n", i)
        head = out[i:nl].split()
        size = int(head[2])
        body = out[nl + 1 : nl + 1 + size]
        lines = body.count(b"\n") + (1 if body and not body.endswith(b"\n") else 0)
        cache[b] = (lines, bool(GENERATED_HEADER.search(body[:2048])))
        i = nl + 1 + size + 1


def generated_patterns(repo, files, cache):
    """linguist-generated patterns from the revision's .gitattributes, if any."""
    for p, b in files:
        if p == ".gitattributes":
            count_lines(repo, [b], cache)
            body = git(repo, "cat-file", "-p", b).decode(errors="replace")
            pats = []
            for line in body.splitlines():
                parts = line.split()
                if len(parts) > 1 and any(a in ("linguist-generated", "linguist-generated=true", "linguist-vendored", "linguist-vendored=true") for a in parts[1:]):
                    pats.append(parts[0].lstrip("/"))
            return pats
    return []


def classify(repo, files, cache):
    """Yield (path, kind, lines) with generated code as its own kind."""
    pats = generated_patterns(repo, files, cache)
    count_lines(repo, [b for _, b in files], cache)
    for p, b in files:
        k = kind_of(p)
        lines, header = cache[b]
        if k == "code" and (header or any(fnmatch.fnmatch(p, pat) or fnmatch.fnmatch(p, pat + "/*") or p.startswith(pat.rstrip("*")) for pat in pats)):
            k = "generated"
        yield p, k, lines


def measure(repo, since):
    cache = {}
    series = []  # (day, {module: lines}, totals by kind)
    for day, sha in sample_commits(repo, since):
        files = [(p, b) for p, b in tree(repo, sha) if kind_of(p) or p == ".gitattributes"]
        mods, kinds = collections.Counter(), collections.Counter()
        for p, k, n in classify(repo, files, cache):
            if k is None:
                continue
            kinds[k] += n
            if k == "code":
                mods[module_of(p)] += n
        series.append((day, mods, kinds))
    head = [(p, b) for p, b in tree(repo, "HEAD") if kind_of(p) == "code" or p == ".gitattributes"]
    return series, [(p, n) for p, k, n in classify(repo, head, cache) if k == "code"]


# --- treemap (squarified) -------------------------------------------------------------------

def squarify(items, x, y, w, h):
    """items: [(label, size, payload)] sorted desc. Returns [(label, size, payload, x, y, w, h)]."""
    out = []
    items = [i for i in items if i[1] > 0]
    total = sum(i[1] for i in items)
    if not items or total <= 0 or w <= 0 or h <= 0:
        return out
    scale = w * h / total
    row, rest = [], list(items)
    while rest:
        item = rest[0]
        side = min(w, h)
        cand = row + [item]
        if not row or worst(cand, side, scale) <= worst(row, side, scale):
            row.append(item); rest.pop(0)
            continue
        x, y, w, h = layout_row(row, x, y, w, h, scale, out)
        row = []
    if row:
        layout_row(row, x, y, w, h, scale, out)
    return out


def worst(row, side, scale):
    areas = [r[1] * scale for r in row]
    s = sum(areas)
    if s == 0 or side == 0:
        return float("inf")
    mx, mn = max(areas), min(areas)
    return max(side * side * mx / (s * s), s * s / (side * side * mn))


def layout_row(row, x, y, w, h, scale, out):
    areas = [r[1] * scale for r in row]
    s = sum(areas)
    if w >= h:  # lay the row vertically along the left
        rw = s / h
        cy = y
        for r, a in zip(row, areas):
            rh = a / rw
            out.append((*r, x, cy, rw, rh)); cy += rh
        return x + rw, y, w - rw, h
    rh = s / w
    cx = x
    for r, a in zip(row, areas):
        rw = a / rh
        out.append((*r, cx, y, rw, rh)); cx += rw
    return x, y + rh, w, h - rh


# --- html -------------------------------------------------------------------------------------

def render(series, head_files, repo_name, excluded):
    days = [d.isoformat() for d, _, _ in series]
    final = series[-1][1]
    order = [m for m, _ in final.most_common()]
    top, other = order[:MAX_SERIES], order[MAX_SERIES:]
    names = top + (["Other"] if other else [])
    rows = []
    for d, mods, kinds in series:
        vals = [mods.get(m, 0) for m in top]
        if other:
            vals.append(sum(mods.get(m, 0) for m in other))
        rows.append({"day": d.isoformat(), "values": vals, "kinds": dict(kinds)})
    # treemap: module -> directory -> file
    by_dir = collections.defaultdict(lambda: collections.defaultdict(int))
    for p, n in head_files:
        by_dir[module_of(p)][os.path.dirname(p) or "."] += 0  # ensure
        by_dir[module_of(p)][p] += n
    mods = sorted(by_dir.items(), key=lambda kv: -sum(kv[1].values()))
    # Every module gets its own colour here, largest first, so nothing but the
    # slivers past the ramp's end shares one. The top modules keep the slots
    # they have in the area chart, so the two figures agree on their colours.
    slots = {m: i for i, (m, _) in enumerate(mods)}
    W, H = 1100, 520
    laid = squarify([(m, sum(fs.values()), None) for m, fs in mods], 0, 0, W, H)
    cells = []
    for m, size, _, x, y, w, h in laid:
        files = sorted(((p, n, None) for p, n in by_dir[m].items() if n > 0), key=lambda t: -t[1])
        inner = squarify(files, x + 1, y + 1, max(w - 2, 0), max(h - 2, 0))
        cells.append({"module": m, "lines": size, "slot": slots[m], "x": x, "y": y, "w": w, "h": h,
                      "files": [{"path": p, "lines": n, "x": fx, "y": fy, "w": fw, "h": fh} for p, n, _, fx, fy, fw, fh in inner]})
    data = {"repo": repo_name, "days": days, "names": names, "rows": rows, "cells": cells,
            "light": LIGHT, "dark": DARK, "W": W, "H": H, "excluded": excluded,
            "treeLight": ramp(LIGHT, SHADE_LIGHT), "treeDark": ramp(DARK, SHADE_DARK),
            "modules": [{"name": m, "lines": sum(fs.values()), "files": sum(1 for n in fs.values() if n > 0)} for m, fs in mods],
            "generated": dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%d %H:%M UTC")}
    return TEMPLATE.replace("__DATA__", json.dumps(data)).replace("__REPO__", html.escape(repo_name))


TEMPLATE = r"""<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Codebase shape: __REPO__</title>
<style>
:root{--bg:#fcfcfb;--ink:#1b1b1a;--ink2:#5d5c58;--muted:#8a8984;--grid:#e6e5e1;--panel:#ffffff;--border:#e2e1dc}
@media(prefers-color-scheme:dark){:root{--bg:#1a1a19;--ink:#ececea;--ink2:#b3b2ad;--muted:#7d7c77;--grid:#2c2c2a;--panel:#212120;--border:#333331}}
body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.45 system-ui,-apple-system,Segoe UI,sans-serif}
main{max-width:1180px;margin:0 auto;padding:28px 24px 48px}
h1{font-size:22px;margin:0 0 2px;letter-spacing:-.01em}h2{font-size:15px;margin:28px 0 8px}
.sub{color:var(--ink2);margin:0 0 18px}
.tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:12px;margin:14px 0 6px}
.tile{background:var(--panel);border:1px solid var(--border);border-radius:8px;padding:12px 14px}
.tile b{display:block;font-size:24px;font-variant-numeric:tabular-nums;letter-spacing:-.02em}.tile span{color:var(--ink2);font-size:12px;text-transform:uppercase;letter-spacing:.06em}
.tile small{display:block;color:var(--muted);margin-top:2px}
svg{display:block;width:100%;height:auto;background:var(--panel);border:1px solid var(--border);border-radius:8px}
.legend{display:flex;flex-wrap:wrap;gap:6px 16px;margin:8px 0 0;font-size:12px;color:var(--ink2)}
.legend i{display:inline-block;width:10px;height:10px;border-radius:2px;margin-right:6px;vertical-align:-1px}
.mlegend{gap:4px 14px;font-size:11px}.mlegend b{color:var(--muted);font-weight:400;font-variant-numeric:tabular-nums}
.tip{position:fixed;pointer-events:none;background:var(--panel);border:1px solid var(--border);border-radius:6px;padding:8px 10px;font-size:12px;box-shadow:0 4px 16px rgba(0,0,0,.12);display:none;max-width:360px}
.tip b{font-variant-numeric:tabular-nums}
table{border-collapse:collapse;font-size:12px;font-variant-numeric:tabular-nums}td,th{padding:3px 10px 3px 0;text-align:left}th{color:var(--ink2);font-weight:500}
details{margin-top:10px}summary{cursor:pointer;color:var(--ink2)}
text{fill:var(--ink)}.ax{fill:var(--ink2);font-size:11px}.gl{stroke:var(--grid)}
</style></head><body><main>
<h1>Codebase shape: __REPO__</h1>
<p class="sub" id="sub"></p>
<div class="tiles" id="tiles"></div>
<h2>Code lines over time, by module</h2>
<div id="area"></div><div class="legend" id="legend"></div>
<h2>The tree at HEAD (code lines, by module then file)</h2>
<div id="tree"></div><div class="legend mlegend" id="treelegend"></div>
<details><summary>Table</summary><div id="table"></div></details>
<div class="tip" id="tip"></div>
<script>
const D = __DATA__;
const dark = matchMedia('(prefers-color-scheme: dark)').matches;
const pal = dark ? D.dark : D.light;
const col = i => pal[i % pal.length];
const tpal = dark ? D.treeDark : D.treeLight;   // one per module in the treemap
const tcol = i => tpal[i % tpal.length];
const fmt = n => n.toLocaleString('en');
const $ = s => document.querySelector(s);
const tip = $('#tip');
const showTip = (e, html) => { tip.innerHTML = html; tip.style.display = 'block'; const w = tip.offsetWidth, h = tip.offsetHeight; tip.style.left = Math.min(e.clientX + 14, innerWidth - w - 8) + 'px'; tip.style.top = Math.min(e.clientY + 14, innerHeight - h - 8) + 'px'; };
const hideTip = () => tip.style.display = 'none';

// tiles
const last = D.rows[D.rows.length - 1], first = D.rows[0];
const total = r => r.values.reduce((a, b) => a + b, 0);
const kinds = last.kinds;
$('#sub').textContent = `${D.days[0]} to ${D.days[D.days.length - 1]}, one sample per day on the first-parent line.`
  + (D.excluded.length ? ` Not measured: ${D.excluded.join(', ')}.` : '') + ` Generated ${D.generated}.`;
$('#tiles').innerHTML = [
  ['code lines', total(last), `${total(last) - total(first) >= 0 ? '+' : ''}${fmt(total(last) - total(first))} over the period`],
  ['test lines', kinds.test || 0, `${Math.round(100 * (kinds.test || 0) / Math.max(1, total(last)))}% of code`],
  ['design & docs lines', kinds.docs || 0, ''],
  ['generated lines', kinds.generated || 0, 'not counted as code'],
  ['yaml, toml, json lines', kinds.data || 0, ''],
  ['modules', D.modules.length, D.names.includes('Other') ? `${D.names.length - 1} as bands below, the rest as Other` : ''],
].map(([l, v, s]) => `<div class="tile"><span>${l}</span><b>${fmt(v)}</b><small>${s}</small></div>`).join('');

// stacked area
(() => {
  const W = 1100, H = 380, L = 64, R = 150, T = 14, B = 34;
  const n = D.rows.length, m = D.names.length;
  const stacks = D.rows.map(r => { let acc = 0; return r.values.map(v => [acc, acc += v]); });
  const max = Math.max(...stacks.map(s => s[m - 1][1]));
  const x = i => L + (n === 1 ? 0 : i * (W - L - R) / (n - 1));
  const y = v => T + (H - T - B) * (1 - v / max);
  let svg = `<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="code lines over time by module">`;
  const ticks = 5;
  for (let t = 0; t <= ticks; t++) { const v = max * t / ticks; svg += `<line class="gl" x1="${L}" x2="${W - R}" y1="${y(v)}" y2="${y(v)}"/><text class="ax" x="${L - 8}" y="${y(v) + 4}" text-anchor="end">${fmt(Math.round(v / 1000))}k</text>`; }
  const step = Math.max(1, Math.floor(n / 8));
  for (let i = 0; i < n; i += step) svg += `<text class="ax" x="${x(i)}" y="${H - 12}" text-anchor="middle">${D.days[i].slice(5)}</text>`;
  for (let s = m - 1; s >= 0; s--) {
    const up = stacks.map((st, i) => `${x(i)},${y(st[s][1])}`).join(' ');
    const down = stacks.map((st, i) => `${x(i)},${y(st[s][0])}`).reverse().join(' ');
    svg += `<polygon points="${up} ${down}" fill="${col(s)}" fill-opacity="${dark ? .8 : .85}" stroke="var(--panel)" stroke-width="1.5"/>`;
  }
  // direct labels at the right edge for the largest four
  const lastStack = stacks[n - 1];
  // direct labels only where the band is tall enough to carry one; the legend names the rest
  D.names.slice(0, 4).forEach((name, s) => { const top = y(lastStack[s][1]), bottom = y(lastStack[s][0]); if (bottom - top < 14) return; const mid = (top + bottom) / 2; svg += `<text x="${W - R + 8}" y="${mid + 4}" font-size="12">${name} <tspan fill="var(--ink2)">${fmt(last.values[s])}</tspan></text>`; });
  svg += `<line id="xh" class="gl" y1="${T}" y2="${H - B}" x1="0" x2="0" style="display:none;stroke:var(--ink2)"/><rect x="${L}" y="${T}" width="${W - L - R}" height="${H - T - B}" fill="transparent" id="hit"/></svg>`;
  $('#area').innerHTML = svg;
  $('#legend').innerHTML = D.names.map((nm, i) => `<span><i style="background:${col(i)}"></i>${nm}</span>`).join('');
  const hit = $('#hit'), xh = $('#xh');
  hit.addEventListener('mousemove', e => {
    const r = hit.getBoundingClientRect(); const i = Math.round((e.clientX - r.left) / r.width * (n - 1));
    xh.setAttribute('x1', x(i)); xh.setAttribute('x2', x(i)); xh.style.display = '';
    const row = D.rows[i];
    showTip(e, `<b>${D.days[i]}</b> · ${fmt(total(row))} code lines<table>${D.names.map((nm, s) => `<tr><td><i class="sw" style="display:inline-block;width:8px;height:8px;background:${col(s)};border-radius:2px;margin-right:6px"></i>${nm}</td><td><b>${fmt(row.values[s])}</b></td></tr>`).join('')}</table>`);
  });
  hit.addEventListener('mouseleave', () => { xh.style.display = 'none'; hideTip(); });
})();

// treemap
(() => {
  let svg = `<svg viewBox="0 0 ${D.W} ${D.H}" role="img" aria-label="code lines at HEAD by module and file">`;
  for (const c of D.cells) {
    svg += `<rect x="${c.x}" y="${c.y}" width="${c.w}" height="${c.h}" fill="${tcol(c.slot)}" fill-opacity=".18" stroke="var(--panel)" stroke-width="2"/>`;
    for (const f of c.files) svg += `<rect data-p="${f.path}" data-n="${f.lines}" data-m="${c.module}" x="${f.x}" y="${f.y}" width="${Math.max(f.w - 1, 0)}" height="${Math.max(f.h - 1, 0)}" fill="${tcol(c.slot)}" fill-opacity="${dark ? .75 : .8}" stroke="var(--panel)" stroke-width="1"/>`;
    if (c.w > 70 && c.h > 22) svg += `<text x="${c.x + 6}" y="${c.y + 15}" font-size="12" font-weight="600" style="paint-order:stroke;stroke:var(--panel);stroke-width:3px">${c.module} <tspan font-weight="400" fill="var(--ink2)">${fmt(c.lines)}</tspan></text>`;
    for (const f of c.files) if (f.w > 90 && f.h > 30) svg += `<text x="${f.x + 5}" y="${f.y + f.h - 6}" font-size="10" fill="var(--ink2)" style="pointer-events:none;paint-order:stroke;stroke:var(--panel);stroke-width:2px">${f.path.split('/').pop()}</text>`;
  }
  svg += '</svg>';
  $('#tree').innerHTML = svg;
  $('#treelegend').innerHTML = D.modules.map((m, i) => `<span><i style="background:${tcol(i)}"></i>${m.name} <b>${fmt(m.lines)}</b></span>`).join('');
  $('#tree').addEventListener('mousemove', e => { const t = e.target; if (t.dataset && t.dataset.p) showTip(e, `<b>${t.dataset.p}</b><br>${fmt(+t.dataset.n)} lines · ${t.dataset.m}`); else hideTip(); });
  $('#tree').addEventListener('mouseleave', hideTip);
})();

// table
$('#table').innerHTML = `<table><tr><th>module</th>${D.days.filter((_, i) => i % Math.max(1, Math.floor(D.days.length / 6)) === 0 || i === D.days.length - 1).map(d => `<th>${d}</th>`).join('')}</tr>` +
  D.names.map((nm, s) => `<tr><td>${nm}</td>${D.rows.filter((_, i) => i % Math.max(1, Math.floor(D.days.length / 6)) === 0 || i === D.rows.length - 1).map(r => `<td>${fmt(r.values[s])}</td>`).join('')}</tr>`).join('') + '</table>';
</script></main></body></html>
"""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--repo", default=".")
    ap.add_argument("--out", default="codebase-shape.html")
    ap.add_argument("--since", default="2026-01-01")
    ap.add_argument("--open", action="store_true", help="open the result in the browser")
    ap.add_argument("--with-tooling", action="store_true", help="count eval/ and scripts/ too")
    a = ap.parse_args()
    global SKIP_TREES
    SKIP_TREES = SKIP_PREFIXES if a.with_tooling else SKIP_PREFIXES + TOOLING_PREFIXES
    excluded = [t.rstrip("/") for t in SKIP_TREES]
    series, head_files = measure(a.repo, a.since)
    if not series:
        sys.exit("no commits in range")
    name = os.path.basename(os.path.abspath(a.repo))
    with open(a.out, "w") as f:
        f.write(render(series, head_files, name, excluded))
    last = series[-1]
    print(f"{a.out}: {len(series)} days, {sum(last[1].values()):,} code lines at HEAD, {len(head_files)} files")
    if a.open:
        subprocess.run(["open", a.out])


if __name__ == "__main__":
    main()
