---
name: lang/selection/typescript-for-web
tags: lang,typescript,frontend
checks: [{"name": "gitignore-names-build-output", "expects": "git would ignore what Vite writes (node_modules/, dist/), so build output can never be committed by accident. A repository with no package.json has no web frontend yet and nothing for this check to judge. If the project commits that output deliberately, this check does not fit it: say so and stop rather than add the line, since an ignore rule over tracked files makes the repository contradict itself", "severity": "medium", "evaluator": "sh", "sh": "[ -n \"$(find . -name package.json -not -path '*/node_modules/*' -not -path './.git/*' 2>/dev/null | head -1)\" ] || { echo 'no package.json: no web frontend here yet, nothing for the ignore rules to cover'; exit 0; }; t=$(mktemp -d) && git -c init.defaultBranch=main init -q \"$t\" || exit 2; bad=0; for p in node_modules/probe.js dist/probe.js; do git --git-dir=\"$t/.git\" --work-tree=. -c core.excludesFile=/dev/null check-ignore -q \"$p\" || { echo \".gitignore does not cover ${p%/*}/\"; bad=1; }; done; exit $bad", "title": "Declare generated files in the repository’s ignore rules"}, {"name": "lockfile-in-step", "expects": "package.json and package-lock.json are in step in every package directory (npm ci --dry-run succeeds), so an install resolves exactly what is committed", "severity": "medium", "evaluator": "tool", "tool": "npm-lock-check", "title": "Keep the dependency lockfile in sync"}]
---
Web frontends are TypeScript + React + Vite, static SPA served by the backend
binary, client types generated from the OpenAPI spec. No SSR meta-framework until SEO
or first-paint at scale genuinely demands it.

Build output stays out of git: once the frontend exists (a
`package.json` anywhere in the tree), `.gitignore` names `node_modules/`
and `dist/`, so an install or a build can never be committed by accident.
When the backend binary embeds the SPA (`//go:embed frontend/dist`), the
container build produces it: a frontend stage runs `npm ci` and
`npm run build`, and the Go stage copies `dist/` in before `go build`.
Committing `dist/` so the embed compiles is the shortcut this rules out
(reading-list and sourdough-app-go did, 2026-09-08). The Go tree must
still compile on a clean checkout, where `dist/` is empty: embed with
`//go:embed all:frontend/dist`, commit an empty `frontend/dist/.keep`,
and ignore the contents rather than the directory (`frontend/dist/*`
with `!frontend/dist/.keep`; git cannot re-include a file under an
ignored directory). The image build fills `dist/` and the same embed
carries the real files.

Dependencies are what the files say: `package-lock.json` is committed
beside every `package.json` and agrees with it, so `npm ci` installs
exactly what is committed, everywhere.
