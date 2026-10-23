#!/usr/bin/env python3
"""The names people and agents see say AppTown, not forge
(private/design/plans/name-rename-2026-10-20.md).

Counts each user-visible "forge" surface across the repository's tracked
files and holds every count to scripts/names-baseline.json: a count above its
baseline fails (a new old name); a count below it is reported, and
`python3 scripts/names-check.py --tighten` records it in the same commit.
The rename lowers the baselines push by push until the user-visible surfaces
reach zero.

Kept names (database and Docker resource names, history) are not surfaces
here; a kept name a surface pattern would still match is listed
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
    # What AppTown names on a host or a provider (containers, volumes,
    # networks, stacks, clusters, buckets, keys, caches): apptown- from 1.0.
    # A deployment made before keeps its forge- names by its record
    # (deployments.host_prefix), so the readers that know them are listed
    # below; the Docker label keys forge.* stay, and are not this surface.
    "host-name": r"(?<![\w./-])forge-(?:env|svc|net|init|data|need|preview|cache|step|tests|review|scan|conform|dind|task|console|check|maker|db|rehearsal|registry|private|shots|exec|eval|what-if|fleet|cleanbuild)\b",
}

# Files that are history or this check itself: never counted. The upgrade
# fixtures and the rc5 catalogue copies are earlier releases' frozen state
# (internal/apptown/boot_test.go, the dependency upgrade tests): they keep the
# names those releases wrote. RC6's notes record the rename itself, so they
# name every old name beside its new one.
SKIP = re.compile(
    r"^(web/dist/|.*node_modules/|releases/v1\.0\.0-rc\.[1-6]\.md$"
    r"|internal/apptown/testdata/upgrade/|internal/apptown/dependency/testdata/rc5/"
    r"|scripts/names-check\.py$|scripts/names-check_test\.py$|scripts/names-baseline\.json$)"
)

# Kept names a surface pattern still matches: (surface, path regex, line
# regex, reason). Each entry is narrow: the same name elsewhere still counts.
ALLOWED = [
    ("cli-invocation", r"^internal/gitexec/folder(_test)?\.go$", r"\"forge helper\"",
     "the Git name of a change folder cloned before the rename: history in that folder's config, accepted, not rewritten"),
    ("command", r"^\.gitignore$", r"^/forged$", "a stray build of the old daemon is never committed again"),
    # AppTown's own acts recorded before the rename (actor "forge-system",
    # instrument and usage kind "forge") are kept identifiers; these readers
    # show them as AppTown (private/design/plans/rename-leftovers-2026-10-20.md).
    ("mount-and-builder", r"^internal/apptown/identity/(principals\.go|apptown_rename_test\.go)$|^web/src/systemActors(\.test)?\.ts$",
     r"forge-system", "shows system actors recorded before the rename as AppTown"),
    ("cli-invocation", r"^web/src/brand\.test\.ts$", r"strayNames\(", "the web name test plants old names to show it catches them"),
    # History readers and the upgrade (push 4): revisions recorded before the
    # rename keep .forge/, refs/forge/ and forge/change-N; these read them or
    # move from them. Nothing writes the old names.
    ("repo-folder", r"^internal/deploy/docker/contract_(bytes|history_test)\.go$|^internal/apptown/changeadds/(changeadds|apptown_rename_test)\.go$"
     r"|^internal/apptown/repositoryedit/(scope|apptown_rename_test)\.go$|^internal/apptown/scm/(repository|staging_test)\.go$"
     r"|^internal/apptown/workflow/workspace_git_test\.go$",
     r".", "reads or moves the contract folder as revisions before the rename hold it"),
    # A db.env still using the keys from before the rename is refused as
    # incomplete; the test plants one.
    ("env", r"^internal/apptown/db_test\.go$", r"FORGE_DB_", "the test plants a db.env with the old keys to show it is refused"),
    # An install made before 1.0 keeps its database forge and a trial volume its
    # data folder forge; these tests plant them.
    ("quoted-name", r"^internal/apptown/(database_dump|db)_test\.go$", r'Database: "forge"|"--no-owner", "forge"',
     "the test plants the database an install made before 1.0 keeps"),
    ("quoted-name", r"^scripts/apptown_start_test\.py$", r'"forge"', "the test plants the data folder a trial volume made before 1.0 keeps"),
    ("quoted-name", r"^scripts/apptown_recovery_rehearsal_test\.py$", r"\('forge-db', 'forge'\)", "the test plants the database an install made before 1.0 keeps"),
    ("quoted-name", r"^scripts/codebase-shape\.py$", r'\["internal", "forge"\]', "the shape report reads history under internal/forge, the root's folder before 2026-10-22"),
    # History readers (push 3): records written before the rename keep their
    # names, so these readers know the old spelling. Nothing writes it.
    ("trailer-or-header", r"^internal/trailer/trailer_test\.go$", r"Forge-Opened-by|\"Forge-Key: \"",
     "the trailer test's comments name the old spelling it guards against"),
    ("mount-and-builder", r"^internal/apptown/identity/enabled\.go$", r"forge-system",
     "the system actor in records from before the rename is still a system actor, never a person"),
    ("public-field", r"^internal/apptown/dbschema/schema\.go$", r"WHERE store = 'forge-disk'",
     "migration 154 renames the stored value"),
    ("public-field", r"^internal/apptown/workflow/check_evidence\.go$", r"forgeFault",
     "check evidence recorded before the rename keeps its platform fault"),
    ("mount-and-builder", r"^internal/apptown/identity/directory_test\.go$", r"\"forge-system\": true",
     "the system actor in records from before the rename stays a system actor"),
    ("public-field", r"^internal/apptown/(dbschema/apptown_rename_migration_test|workflow/apptown_rename_test)\.go$", r"forge-disk|forgeFault",
     "migration and reader tests plant the stored values from before the rename"),
    # Push 5 (prose): what stays because a stored value or a kept resource
    # depends on the exact text.
    ("cli-invocation", r"^internal/apptown/secret/store\.go$", r"derivedPurpose = \"forge need credentials v1\"",
     "the key-derivation purpose of every generated need credential; changing it changes every deployment's passwords"),
    ("cli-invocation", r"^scripts/docs-current(_test)?\.py$", r"says forge for the product",
     "the docs check names the word it refuses"),
    ("cli-invocation", r"^web/src/brand\.test\.ts$", r"the forge command and its", "the web name test lists the old names it catches"),
    ("commit-subject", r"^web/src/brand\.test\.ts$", r"forge: PRODUCT|From another forge:", "the web name test plants old names to show it catches them"),
    ("export-and-ticket", r"^internal/apptown/project/contract_snapshots_test\.go$", r"x-forge-app", "plants a compose file from before the app contract to show it is not read as one"),
    ("api-version", r"^internal/dockerlabel/dockerlabel_test\.go$|^internal/apptown/schema/schema_test\.go$|^scripts/docs-current\.py$", r"forge\.flokkit\.io",
     "tests and the docs check name the old apiVersion host to show it is refused or not a product word"),
    ("env", r"^cmd/apptownd/flags_test\.go$", r"FORGE_DB_URL", "the test plants a setting spelled the old way to show it is not read"),
    ("cli-invocation", r"^internal/apptown/scm/main_record_test\.go$", r"forge then forge",
     "the walk's pairs name the stored main-record kind \"forge\" (a quoted-name surface, renamed with the stored data)"),
    ("cli-invocation", r"^scripts/docs-current_test\.py$", r".", "the docs check's test plants the old product name to show it is refused"),
    ("cli-invocation", r"^web/src/pages/console/Bubbles\.tsx$", r"\[forge check\]", "chats from before the rename keep their [forge check] messages as written"),
    # Host names (external-names plan, 21 Oct 2026): what a deployment or an
    # install made before 1.0 is called keeps its forge- names for good;
    # these read or name them, and tests plant them.
    ("host-name", r"_test\.(go|py)$|\.test\.tsx?$", r".", "tests plant names a deployment or an install made before 1.0 keeps"),
    ("host-name", r"^internal/deploy/digitalocean/(inventory|storage)\.go$", r"forge-maker", "a maker key an older release left is still found and deleted"),
    ("host-name", r"^internal/apptown/maintenance/images\.go$", r"forge-conform:", "an older release's conformance images are still swept"),
    ("host-name", r"^internal/deploy/portainer/portainer\.go$", r"forge-env applied", "the platform's own Portainer environment name, not one AppTown makes"),
    ("host-name", r"^scripts/apptown-recovery-rehearsal\.sh$", r"forge-db", "live's built-in database, made before 1.0"),
    ("host-name", r"^stock/docs/guides/(apptown-backups|upgrading|server-setup)\.md$|^design/operations/README\.md$", r"forge-db", "says an install made before 1.0 keeps its database's name"),
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
            print(f"names {s:18} {n:5}  baseline {held:5}  WORSE: say AppTown")
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
