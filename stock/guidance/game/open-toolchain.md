---
name: game/open-toolchain
tags: game,family
---
The helper may use the whole JavaScript ecosystem: npm libraries (three.js, physics, sound), a bundler, TypeScript — whatever makes the game better. Two rules: dependencies are pinned by a committed lockfile, and the game ships as static files: the repo Dockerfile builds them and ends with a `FROM scratch AS site` stage holding only `dist/`, which AppTown serves (`static: { build: ., output: dist }` in `.apptown/app.yaml`) — a player needs only the play link, never the toolchain. AppTown's file server serves files that exist; keep the game on one page. No server unless a change says why.

Declare test commands in `.apptown/app.yaml` with dependency installation
first (`npm ci --no-audit --no-fund && npm test`). Make the test script write
JUnit to `$APPTOWN_TEST_REPORT` so AppTown can record counts, including zero.
The stock Node 24 starter uses:

```sh
node --test --test-reporter=spec --test-reporter-destination=stdout --test-reporter=junit --test-reporter-destination="$APPTOWN_TEST_REPORT"
```
