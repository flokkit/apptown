---
name: frontend/vite-react
tags: frontend,typescript,build
checks: [{"name": "builds", "expects": "the locked dependencies install and the project's own build script (npm run build: the type check, then vite build) succeeds, so what the Dockerfile builds is known to build", "severity": "high", "evaluator": "tool", "tool": "npm-build", "title": "Make the app build", "appliesTo": ["tool:node"]}, {"name": "tests-pass", "expects": "the locked dependencies install and the project's own tests (npm test, which runs vitest run) all pass", "severity": "high", "evaluator": "tool", "tool": "npm-test", "title": "Make the tests pass", "appliesTo": ["tool:node"]}, {"name": "lockfile-in-step", "expects": "package.json and package-lock.json are in step in every package directory (npm ci --dry-run succeeds), so an install resolves exactly what is committed", "severity": "medium", "evaluator": "tool", "tool": "npm-lock-check", "title": "Keep the dependency lockfile in sync", "appliesTo": ["tool:node"]}, {"name": "gitignore-names-build-output", "expects": "git would ignore what Vite writes (node_modules/, dist/) beside every package.json at or below the folder of a part built with node, so build output can never be committed by accident; such a part with no package.json fails. If the project commits that output deliberately, this check does not fit it: say so and stop rather than add the line, since an ignore rule over tracked files makes the repository contradict itself", "severity": "medium", "evaluator": "sh", "sh": "t=$(mktemp -d) && git -c init.defaultBranch=main init -q \"$t\" || exit 2; find . \\( -name node_modules -o -name vendor -o -name .git \\) -prune -o -type f \\( -name package.json \\) -print > \"$t/m\" || exit 2; awk -v dir=1 'BEGIN { base = ENVIRON[\"APPTOWN_FOLDER\"]; n = split(ENVIRON[\"APPTOWN_NOT_APP_CODE\"], x, \"\\n\") } { f = $0; sub(/^\\.\\//, \"\", f); if (base != \".\" && base != \"\") f = base \"/\" f; for (i = 1; i <= n; i++) if (x[i] != \"\" && index(f, x[i] \"/\") == 1) next; if (dir) { if (!sub(/\\/[^\\/]*$/, \"\", f)) f = \"\" } print f }' \"$t/m\" | sort -u > \"$t/d\" || exit 2; [ -s \"$t/d\" ] || { w=\"part $APPTOWN_PARTS_HERE is\"; case \"$APPTOWN_PARTS_HERE\" in *\" \"*) w=\"parts $APPTOWN_PARTS_HERE are\";; esac; d=$APPTOWN_FOLDER; [ \"$d\" = . ] && d=\"the repository root\"; echo \"$w built with node (tools: in .apptown/app.yaml) and there is no package.json in $d or below it: add it, or correct the part's tools\"; exit 1; }; bad=0; while IFS= read -r r; do for p in node_modules/probe.js dist/probe.js; do q=${r:+$r/}$p; git -C \"$APPTOWN_ROOT\" --git-dir=\"$t/.git\" --work-tree=\"$APPTOWN_ROOT\" -c core.excludesFile=/dev/null check-ignore -q \"$q\"; s=$?; [ $s -eq 0 ] && continue; [ $s -eq 1 ] || exit 2; echo \".gitignore does not cover ${q%/*}/ (Node writes it)\"; bad=1; done; done < \"$t/d\"; exit $bad", "title": "Declare generated files in the repository’s ignore rules", "appliesTo": ["tool:node"]}]
---
A browser UI is TypeScript and React, built with Vite and tested with
Vitest. No server-side-rendering framework until search ranking or first
paint at scale demands it.

**The scripts in package.json are the declaration.** Keep these four, with
these names; the Dockerfile, the checks and the README run them:

    "dev":     "vite"
    "build":   "tsc --noEmit && vite build"
    "test":    "vitest run"
    "preview": "vite preview --host 0.0.0.0 --port 8080"

`tsconfig.json` keeps `"strict": true`. The checks `builds` and
`tests-pass` run `npm ci` and these scripts on every change.

**Tests.** Put the app's rules (calculations, parsing, what a saved value
means) in plain modules and test them with Vitest beside the code
(`src/thing.test.ts`). Every change that alters behaviour adds or updates a
test. Add `jsdom` only when logic lives in the DOM.

**Dependencies.** A few npm libraries where they do something hard (dates,
charts); each adds to every visitor's download, so prefer the platform
(`Intl`, `fetch`, CSS). `package-lock.json` is committed beside every
`package.json` and agrees with it (`npm install <name>` moves both).
Beside every `package.json` of a part built with node, `.gitignore`
covers `node_modules/` and `dist/`.

**Served by the app's server**, when it has one (a browser-only app is
served as static files: `frontend/browser-only`). A frontend stage of the
image build runs `npm ci` and `npm run build`, and the server stage copies
`dist/` in; never commit `dist/` to make a build pass. A Go binary embeds
it with `//go:embed all:frontend/dist`, an empty `frontend/dist/.keep`
committed and the contents ignored (`frontend/dist/*`,
`!frontend/dist/.keep`), so a clean checkout compiles. Client types come
from the server's OpenAPI description.

Use relative imports and paths from `/` for the app's own files, never the
address it is hosted at, which differs between previews and releases.
