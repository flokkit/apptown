---
name: lang/selection/typescript-for-web
tags: lang,typescript,frontend
checks: [{"name": "gitignore-names-build-output", "expects": "git would ignore what Vite writes (node_modules/, dist/) beside every package.json at or below the folder of a part built with node, so build output can never be committed by accident; such a part with no package.json fails. If the project commits that output deliberately, this check does not fit it: say so and stop rather than add the line, since an ignore rule over tracked files makes the repository contradict itself", "severity": "medium", "evaluator": "sh", "sh": "t=$(mktemp -d) && git -c init.defaultBranch=main init -q \"$t\" || exit 2; find . \\( -name node_modules -o -name vendor -o -name .git \\) -prune -o -type f \\( -name package.json \\) -print > \"$t/m\" || exit 2; awk -v dir=1 'BEGIN { base = ENVIRON[\"APPTOWN_FOLDER\"]; n = split(ENVIRON[\"APPTOWN_NOT_APP_CODE\"], x, \"\\n\") } { f = $0; sub(/^\\.\\//, \"\", f); if (base != \".\" && base != \"\") f = base \"/\" f; for (i = 1; i <= n; i++) if (x[i] != \"\" && index(f, x[i] \"/\") == 1) next; if (dir) { if (!sub(/\\/[^\\/]*$/, \"\", f)) f = \"\" } print f }' \"$t/m\" | sort -u > \"$t/d\" || exit 2; [ -s \"$t/d\" ] || { w=\"part $APPTOWN_PARTS_HERE is\"; case \"$APPTOWN_PARTS_HERE\" in *\" \"*) w=\"parts $APPTOWN_PARTS_HERE are\";; esac; d=$APPTOWN_FOLDER; [ \"$d\" = . ] && d=\"the repository root\"; echo \"$w built with node (tools: in .apptown/app.yaml) and there is no package.json in $d or below it: add it, or correct the part's tools\"; exit 1; }; bad=0; while IFS= read -r r; do for p in node_modules/probe.js dist/probe.js; do q=${r:+$r/}$p; git -C \"$APPTOWN_ROOT\" --git-dir=\"$t/.git\" --work-tree=\"$APPTOWN_ROOT\" -c core.excludesFile=/dev/null check-ignore -q \"$q\"; s=$?; [ $s -eq 0 ] && continue; [ $s -eq 1 ] || exit 2; echo \".gitignore does not cover ${q%/*}/ (Node writes it)\"; bad=1; done; done < \"$t/d\"; exit $bad", "title": "Declare generated files in the repository’s ignore rules", "appliesTo": ["tool:node"]}, {"name": "lockfile-in-step", "expects": "package.json and package-lock.json are in step in every package directory (npm ci --dry-run succeeds), so an install resolves exactly what is committed", "severity": "medium", "evaluator": "tool", "tool": "npm-lock-check", "title": "Keep the dependency lockfile in sync", "appliesTo": ["tool:node"]}]
---
Web frontends are TypeScript + React + Vite, static SPA served by the backend
binary, client types generated from the OpenAPI spec. No SSR meta-framework until SEO
or first-paint at scale genuinely demands it.

Build output stays out of git: beside every `package.json` in the folder
of a part built with node (`tools: [node]` in `.apptown/app.yaml`) or below
it, `.gitignore` covers `node_modules/` and `dist/`, so an install or a
build can never be committed by accident.
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
