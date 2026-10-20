#!/usr/bin/env python3
"""The names people and agents see say AppTown, not forge
(design/plans/name-rename-2026-10-20.md).

Counts each user-visible "forge" surface across the repository's tracked
files and holds every count to scripts/names-baseline.json: a count above its
baseline fails (a new old name); a count below it is reported, and
`python3 scripts/names-check.py --tighten` records it in the same commit.
The rename lowers the baselines push by push until the user-visible surfaces
reach zero.

Kept names (the Go module, database and Docker resource names, history) are
not surfaces here; a kept name a surface pattern would still match is listed
in ALLOWED with its reason, so keeping it is a decision, not an omission.

Usage: names-check.py [--root DIR] [--tighten] [--show SURFACE] [--init]
"""
import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

# Each surface is one kind of name a person or an agent meets.
SURFACES = {
    # A command name is never followed by "-": forge-console-<id> and
    # forge-check-<key> are kept container names, not commands.
    "command": r"\bforged\b|\bforge-(?:worker|linux|check|console|deploy|gate)\b(?!-)|\bcmd/forge(?:/|$)",
    "cli-invocation": r"(?<![\w./-])forge (?=[a-z][a-z-]+\b)",
    "env": r"\bFORGE_[A-Z0-9_]+|__FORGE_ENV__",
    "repo-folder": r"\.forge/",
    "api-version": r"forge\.flokkit\.io",
    "static-path": r"/_forge/",
    "header": r"\bX-Forge-[A-Za-z-]+",
    "trailer-or-header": r"(?<![\w-])Forge-[A-Z][A-Za-z-]+",
    "refs": r"\brefs/forge/",
    "branch": r"\bforge/(?:change|run)-",
    # The daemon's and worker's install paths; maintainer-only folders on lab
    # and demo hosts (/opt/forge-restore, /etc/forge-registry) are not these.
    "install-path": r"/(?:opt|etc|var/lib)/forge(?:-worker)?(?![-\w])|forged\.service|forge-worker\.service|\.config/forge\b",
    "token-or-cookie": r"\bforge_(?:session|invite_|mcp_|rt_|git_|pull_)|__Host-forge|[\"']forge_[\"']",
    "metric": r"\bforge_(?:http|inference|step|unit)_[a-z_]+",
    "scope": r"\bforge:[a-z*][a-z:*/-]*",
    "storage-key": r"\bforge\.(?:user|chunk-reload)\b|\bforge-theme\b",
    "export-and-ticket": r"\.forge-export\b|\bx-forge-[a-z-]+",
    "quoted-name": r"[\"']forge[\"']",
    "commit-subject": r"(?<![\w.:-])forge: ",
    "mount-and-builder": r"/forge/(?:in|state)\b|/forge-cache\b|\bforge-local\b|\bforge-system\b",
    "public-field": r"\bforgeFault\b|\bforge-disk\b|\btrusts-forge-front-door\b|\bcapacity\.forge\b",
    "gate": r"\bGATE_FORGE\b",
    "skill": r"\.claude/skills/forge\b",
    "agent-recipe": r"\bdeploy/forge-agent\b",
}

# Files that are history or this check itself: never counted. The upgrade
# fixtures are earlier releases' frozen state (internal/forge/boot_test.go):
# they keep the names those releases wrote.
SKIP = re.compile(
    r"^(design/plans/|design/reviews/|web/dist/|.*node_modules/|releases/v1\.0\.0-rc\.[1-5]\.md$"
    r"|internal/forge/testdata/upgrade/"
    r"|scripts/names-check\.py$|scripts/names-check_test\.py$|scripts/names-baseline\.json$)"
)

# Kept names a surface pattern still matches: (surface, path regex, line
# regex, reason). Each entry is narrow: the same name elsewhere still counts.
ALLOWED = [
    ("quoted-name", r"^internal/forge/dbboot\.go$", r"\bbuiltinDBName\s*=\s*\"forge\"",
     "the built-in database's name: storage, kept (an existing install addresses it)"),
    ("quoted-name", r"^scripts/(shape-check|shape-report|codebase-shape)\.py$|^scripts/shape-request-boundaries\.go$",
     r".", "the shape tools name the Go package internal/forge, which is kept"),
    ("quoted-name", r"^scripts/shape-check_test\.py$", r"flokkit\.io/forge/internal/forge",
     "asserts the shape tool's name for the kept Go package internal/forge"),
    # The demo's installs retire the units installed before the rename, so
    # they name them, once, by their old names.
    ("command", r"^scripts/do-demo/install-(forge|worker)\.sh$", r"before the AppTown rename|systemctl disable --now forge|/var/lib/forge-worker|rm -rf /opt/forge",
     "retires the daemon and worker installed before the rename"),
    ("install-path", r"^scripts/do-demo/install-(forge|worker)\.sh$", r"systemctl disable --now forge|/var/lib/forge-worker|rm -rf /opt/forge",
     "retires the daemon and worker installed before the rename"),
    ("command", r"^\.gitignore$", r"^/forged$", "a stray build of the old daemon is never committed again"),
    ("env", r"^scripts/deploy-ct\.sh$", r"\^FORGE_", "refuses a CT whose settings still use the old prefix"),
    ("env", r"^scripts/deploy_ct_names_test\.py$", r"FORGE_(DB_URL|URL)=",
     "the CT check's test plants an old daemon setting and a kept CLI variable"),
    ("cli-invocation", r"^web/src/(pages/console/Bubbles\.tsx|brand\.test\.ts)$", r"apptown\\?\|forge\\?\) check",
     "chats from before the rename keep their [forge check] messages as written"),
    ("cli-invocation", r"^web/src/brand\.test\.ts$", r"strayNames\(", "the web name test plants old names to show it catches them"),
    # History readers (push 3): records written before the rename keep their
    # names, so these readers know the old spelling. Nothing writes it.
    ("trailer-or-header", r"^internal/trailer/trailer\.go$", r"OldOperation = \"Forge-Operation\"",
     "scm finds a landing's merge from before the rename by its old Operation trailer"),
    ("trailer-or-header", r"^internal/trailer/trailer_test\.go$", r"Forge-Opened-by|\"Forge-Key: \"",
     "the trailer test's comments name the old spelling it guards against"),
    ("mount-and-builder", r"^internal/forge/identity/enabled\.go$", r"forge-system",
     "the system actor in records from before the rename is still a system actor, never a person"),
    ("public-field", r"^internal/forge/dbschema/schema\.go$", r"WHERE store = 'forge-disk'",
     "migration 154 renames the stored value"),
    ("public-field", r"^internal/forge/workflow/check_evidence\.go$", r"forgeFault",
     "check evidence recorded before the rename keeps its platform fault"),
    ("trailer-or-header", r"^internal/forge/scm/git_driver_test\.go$", r"Forge-Operation",
     "the merge-proof test plants a merge written before the rename"),
    ("token-or-cookie", r"^internal/forge/identity/refresh\.go$", r"forge_rt_",
     "a refresh token from before the rename is refused; the comment names its prefix"),
    ("token-or-cookie", r"^internal/forge/identity/apptown_rename_test\.go$", r"forge_rt_",
     "the test plants a refresh token from before the rename"),
    ("scope", r"^internal/forge/identity/apptown_rename_test\.go$", r"forge:change:create",
     "the test plants a delegation's stored scope from before the rename"),
    ("mount-and-builder", r"^internal/forge/identity/directory_test\.go$", r"\"forge-system\": true",
     "the system actor in records from before the rename stays a system actor"),
    ("public-field", r"^internal/forge/(dbschema/apptown_rename_migration_test|workflow/apptown_rename_test)\.go$", r"forge-disk|forgeFault",
     "migration and reader tests plant the stored values from before the rename"),
]


def tracked(root):
    out = subprocess.run(["git", "-C", str(root), "ls-files"], capture_output=True, text=True, check=True).stdout
    return [f for f in out.split("\n") if f and not SKIP.match(f)]


def scan(root):
    """Hits per surface: {surface: [(path, line number, text)]}."""
    patterns = {s: re.compile(p) for s, p in SURFACES.items()}
    allowed = [(s, re.compile(p), re.compile(l)) for s, p, l, _ in ALLOWED]
    hits = {s: [] for s in SURFACES}
    for f in tracked(root):
        try:
            text = (root / f).read_text(encoding="utf-8")
        except (UnicodeDecodeError, OSError):
            text = ""
        # The path itself, as line 0: a moved file must not keep its old name.
        for n, line in enumerate([f] + text.split("\n")):
            for s, rx in patterns.items():
                for _ in rx.finditer(line):
                    if any(a == s and ap.search(f) and al.search(line) for a, ap, al in allowed):
                        continue
                    hits[s].append((f, n, line.strip()))
    return hits


def main(argv):
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", default=str(Path(__file__).resolve().parents[1]))
    ap.add_argument("--tighten", action="store_true", help="lower the baseline to counts that fell")
    ap.add_argument("--show", help="list every hit of one surface")
    ap.add_argument("--init", action="store_true", help="write the baseline from today's counts (once, when the guard starts)")
    args = ap.parse_args(argv)
    root = Path(args.root)
    base_file = root / "scripts" / "names-baseline.json"
    baseline = json.loads(base_file.read_text()) if base_file.exists() else {}
    hits = scan(root)
    if args.init:
        base_file.write_text(json.dumps({s: len(hits[s]) for s in SURFACES}, indent=1) + "\n")
        print("names: baseline written")
        return 0
    ok, fell = True, False
    for s in SURFACES:
        n, held = len(hits[s]), baseline.get(s, 0)
        if n > held:
            print(f"names {s:18} {n:5}  baseline {held:5}  WORSE: say AppTown (design/plans/name-rename-2026-10-20.md)")
            for f, ln, text in hits[s][:20]:
                print(f"    {f}:{ln}: {text[:140]}")
            ok = False
        elif n < held:
            print(f"names {s:18} {n:5}  baseline {held:5}  lower")
            fell = True
            baseline[s] = n
        else:
            print(f"names {s:18} {n:5}  baseline {held:5}  ok")
        if args.show == s:
            for f, ln, text in hits[s]:
                print(f"    {f}:{ln}: {text[:160]}")
    if fell and args.tighten:
        base_file.write_text(json.dumps({s: baseline.get(s, 0) for s in SURFACES}, indent=1) + "\n")
        print("names: baseline lowered")
    elif fell:
        print("names: counts fell; record them with `python3 scripts/names-check.py --tighten` in this commit")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
