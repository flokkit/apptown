---
name: repo/no-build-artifacts
tags: repo,hygiene,platform
tier: platform
checks: [{"name": "no-ignored-files-tracked", "expects": "nothing the repository's own .gitignore covers is committed — the project declared what is not source. Decide what each named file is before fixing: build output or a forge runtime file gets untracked (git rm --cached); a source file or a test fixture means the ignore rule is wrong, so fix the rule (anchor it, narrow it, or add an explicit !path). Never move, rename or delete a file to make the check stop seeing it", "severity": "medium", "evaluator": "sh", "sh": "t=$(mktemp -d) && git -c init.defaultBranch=main init -q \"$t\" || exit 2; find . -type f -not -path './.git/*' | sed 's|^\\./||' | git --git-dir=\"$t/.git\" --work-tree=. -c core.excludesFile=/dev/null check-ignore --no-index --stdin; s=$?; [ $s -eq 1 ] && exit 0; [ $s -eq 0 ] && exit 1; exit 2", "occurrences": "lines", "title": "Reconcile tracked files with the repository’s ignore rules"}, {"name": "no-build-artifacts-tracked", "expects": "no compiled program is committed — an ELF, Mach-O or Windows executable, or a Java class file, by its own format signature; the build makes those, git keeps sources", "severity": "medium", "evaluator": "sh", "sh": "bad=0; for f in $(find . -type f -not -path './.git/*' -not -path '*/node_modules/*' -not -path '*/vendor/*'); do h=$(head -c 4 \"$f\" | od -An -tx1 | tr -d ' \\n'); case \"$h\" in 7f454c46|cffaedfe|cefaedfe|feedfacf|feedface|cafebabe|4d5a*) echo \"${f#./}\"; bad=1;; esac; done; exit $bad", "occurrences": "lines", "title": "Remove compiled programs from version control"}]
---
Git keeps sources; the build makes the rest. Build output committed to
the repository bloats every clone, drifts from the source it was built
from, and is exactly the kind of file a security scanner cannot read.

What counts as build output depends on the language and its tools, so
the forge holds no list of folder names. The project's own `.gitignore`
is the declaration, and git's matcher is the reference: a file that is
both committed and ignored is the project contradicting itself. Each way
of working makes its ignore list name what its own tools write (`target/`
for Maven and Cargo, `node_modules/` and `dist/` for Node, `__pycache__/`
for Python), and the forge's `.forge/*` runtime files are ignored on
every project with the contract `.forge/app.yaml` as the one exception.

Agents make this mistake when they build locally to test and then
`git add -A`, or force-add a report under `.forge/`. Before fixing, look
at what the named file is. Build output or a forge runtime file:

    git rm --cached <path>

and commit. A source file or a test fixture the rule happens to catch:
the rule is wrong, not the file. Anchor it (`/waypoint`, not `waypoint`),
narrow it, or add an explicit `!<path>` line. Never move, rename or
delete a file to make the check stop seeing it (waypoint's sources and
webalizer-ng's fixtures, 2026-09-08), and never `git add -f`.

Two checks. `no-ignored-files-tracked` asks git which committed files the
tree's own ignore rules cover and names each one. `no-build-artifacts-tracked`
names any committed compiled program — ELF, Mach-O, Windows PE, Java
class — by the file's own format signature, whatever its size or name.
Neither carries a list of the forge's own: what is ignored is the
project's declaration, and what is a program is the format's. Large
media and data files are not this block's business; `no-db-runtime-files-tracked`
covers databases.
