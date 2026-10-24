---
name: frontend/vite-typescript
tags: frontend,typescript,build
checks: [{"name": "builds", "expects": "the locked dependencies install and the project's own build script (npm run build: the type check, then vite build) succeeds, so what the Dockerfile builds is known to build", "severity": "high", "evaluator": "tool", "tool": "npm-build", "title": "Make the app build", "appliesTo": ["tool:node"]}, {"name": "tests-pass", "expects": "the locked dependencies install and the project's own tests (npm test, which runs vitest run) all pass", "severity": "high", "evaluator": "tool", "tool": "npm-test", "title": "Make the tests pass", "appliesTo": ["tool:node"]}, {"name": "lockfile-in-step", "expects": "package.json and package-lock.json are in step in every package directory (npm ci --dry-run succeeds), so an install resolves exactly what is committed", "severity": "medium", "evaluator": "tool", "tool": "npm-lock-check", "title": "Keep the dependency lockfile in sync", "appliesTo": ["tool:node"]}, {"name": "gitignore-names-build-output", "expects": "git would ignore what Vite writes (node_modules/, dist/) beside every package.json at or below the folder of a part built with node, so build output can never be committed by accident; such a part with no package.json fails. If the project commits that output deliberately, this check does not fit it: say so and stop rather than add the line, since an ignore rule over tracked files makes the repository contradict itself", "severity": "medium", "evaluator": "sh", "sh": "t=$(mktemp -d) && git -c init.defaultBranch=main init -q \"$t\" || exit 2; find . \\( -name node_modules -o -name vendor -o -name .git \\) -prune -o -type f \\( -name package.json \\) -print > \"$t/m\" || exit 2; awk -v dir=1 'BEGIN { base = ENVIRON[\"APPTOWN_FOLDER\"]; n = split(ENVIRON[\"APPTOWN_NOT_APP_CODE\"], x, \"\\n\") } { f = $0; sub(/^\\.\\//, \"\", f); if (base != \".\" && base != \"\") f = base \"/\" f; for (i = 1; i <= n; i++) if (x[i] != \"\" && index(f, x[i] \"/\") == 1) next; if (dir) { if (!sub(/\\/[^\\/]*$/, \"\", f)) f = \"\" } print f }' \"$t/m\" | sort -u > \"$t/d\" || exit 2; [ -s \"$t/d\" ] || { w=\"part $APPTOWN_PARTS_HERE is\"; case \"$APPTOWN_PARTS_HERE\" in *\" \"*) w=\"parts $APPTOWN_PARTS_HERE are\";; esac; d=$APPTOWN_FOLDER; [ \"$d\" = . ] && d=\"the repository root\"; echo \"$w built with node (tools: in .apptown/app.yaml) and there is no package.json in $d or below it: add it, or correct the part's tools\"; exit 1; }; bad=0; while IFS= read -r r; do for p in node_modules/probe.js dist/probe.js; do q=${r:+$r/}$p; git -C \"$APPTOWN_ROOT\" --git-dir=\"$t/.git\" --work-tree=\"$APPTOWN_ROOT\" -c core.excludesFile=/dev/null check-ignore -q \"$q\"; s=$?; [ $s -eq 0 ] && continue; [ $s -eq 1 ] || exit 2; echo \".gitignore does not cover ${q%/*}/ (Node writes it)\"; bad=1; done; done < \"$t/d\"; exit $bad", "title": "Declare generated files in the repository’s ignore rules", "appliesTo": ["tool:node"]}]
---
Apps are TypeScript, built with Vite, tested with Vitest, and served as
the static files the build writes to `dist/`.

**The scripts in package.json are the declaration.** Keep these four, with
these names, because the Dockerfile, the checks and the README run them:

    "dev":     "vite"
    "build":   "tsc --noEmit && vite build"
    "test":    "vitest run"
    "preview": "vite preview --host 0.0.0.0 --port 8080"

`npm run build` checks the types before it builds, so a type error fails
the build. `tsconfig.json` keeps `"strict": true`. Two checks run these
scripts on every change in a clean Node image: `builds` (`npm ci`, then
`npm run build`) and `tests-pass` (`npm ci`, then `npm test`).

**Tests.** Put the app's rules (calculations, parsing, what a saved value
means) in plain modules with no page access, and test them with Vitest
beside the code (`src/thing.test.ts`). Every change that alters behaviour
adds or updates a test. Test the page itself (the DOM) only where the
logic lives there; Vitest's `jsdom` environment is a development
dependency added when it is needed, not by default.

**Dependencies.** A few npm libraries are fine where they do something
hard (dates and time zones, astronomy, charts, a UI library such as React when the app
outgrows plain DOM code). Each one adds to every visitor's download, so
prefer the platform (`Intl`, `fetch` of the app's own files, CSS) for
small jobs. `package-lock.json` is committed beside `package.json` and
agrees with it; change dependencies with `npm install <name>` so both
move together. `node_modules/` and `dist/` are in `.gitignore`.

**Building and serving.** `.apptown/app.yaml` declares the app a static
site with a build (`static: { build: ., output: dist }`). The
repository's Dockerfile builds it in a Node stage (`npm ci`,
`npm run build`) and ends with a stage that holds only the files:

    FROM scratch AS site
    COPY --from=build /app/dist /dist

AppTown builds that stage for previews and releases and serves its
`dist/` with its own file server; Node never ships. Keep the
Dockerfile's Node major version the same as `mise.toml`'s. The built
folder holds only folders and files: a symbolic link in it is refused.

What the file server does, and does not do:

- It serves `/_apptown/env.js` (guidance `deploy/browser-values`), so the
  page loads it first in `<head>`, and readiness is the site's root.
- It serves only files that exist. A path with no file is a 404, so an
  app with several screens uses hash routes (`#/items/42`), which a
  reload keeps. An app that needs path routes (`/items/42`) keeps its own
  server instead: `build: .` with a Caddy stage that answers unknown
  paths with `index.html`, and a `/healthz` readiness path.
- It sends validators (ETag, Last-Modified) and no `Cache-Control`
  header, so a browser may reuse a file briefly before asking again.
  Vite names files under `/assets/` by their content, so a new release
  never reuses an old script.

Use relative imports and paths from `/` for the app's own files; never an
absolute address of the place it is hosted, which differs between
previews and releases.
