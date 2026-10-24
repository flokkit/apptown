---
name: repo/no-build-artifacts
tags: repo,hygiene,platform
tier: platform
checks: [{"name": "no-ignored-files-tracked", "expects": "nothing the repository's own .gitignore covers is committed — the project declared what is not source. Decide what each named file is before fixing: build output or an AppTown runtime file under .apptown/ gets untracked (git rm --cached); a source file or a test fixture means the ignore rule is wrong, so fix the rule (anchor it, narrow it, or add an explicit !path). Never move, rename or delete a file to make the check stop seeing it", "severity": "medium", "evaluator": "sh", "sh": "t=$(mktemp -d) && git -c init.defaultBranch=main init -q \"$t\" || exit 2; find . -type f -not -path './.git/*' | sed 's|^\\./||' | git --git-dir=\"$t/.git\" --work-tree=. -c core.excludesFile=/dev/null check-ignore --no-index --stdin; s=$?; [ $s -eq 1 ] && exit 0; [ $s -eq 0 ] && exit 1; exit 2", "occurrences": "lines", "title": "Reconcile tracked files with the repository’s ignore rules", "appliesTo": ["repository"]}, {"name": "no-build-artifacts-tracked", "expects": "no compiled program is committed — an ELF, Mach-O or Windows executable, or a Java class file, by its own format signature; the build makes those, git keeps sources", "severity": "medium", "evaluator": "sh", "sh": "fs=$(find . -type f -not -path './.git/*' -not -path '*/node_modules/*' -not -path '*/vendor/*') || exit 2; bad=0; for f in $fs; do h=$(od -An -tx1 -N4 \"$f\") || exit 2; h=$(printf %s $h); case \"$h\" in 7f454c46|cffaedfe|cefaedfe|feedfacf|feedface|cafebabe|4d5a*) echo \"${f#./}\"; bad=1;; esac; done; exit $bad", "occurrences": "lines", "title": "Remove compiled programs from version control", "appliesTo": ["repository"]}]
---
Git keeps sources; the build makes the rest. Never commit build output or
AppTown's runtime files under `.apptown/` (only `.apptown/app.yaml` is
committed). The project's `.gitignore` names what its tools write (`target/`
for Maven and Cargo, `node_modules/` and `dist/` for Node, `__pycache__/` for
Python).

When a check names a committed file, look at what it is:

- build output or a runtime file under `.apptown/`: `git rm --cached <path>`
  and commit;
- a source file or test fixture an ignore rule happens to catch: fix the rule
  (anchor it, `/waypoint` not `waypoint`; narrow it; or add `!<path>`).

Never move, rename or delete a file to stop a check seeing it, and never
`git add -f`.
