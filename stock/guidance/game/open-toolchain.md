---
name: game/open-toolchain
tags: game,family
---
The helper may use the whole JavaScript ecosystem: npm libraries (three.js, physics, sound), a bundler, TypeScript — whatever makes the game better. Two rules: dependencies are pinned by a committed lockfile, and the game ships as static files: the repo Dockerfile builds them and ends with a `FROM scratch AS site` stage holding only `dist/`, which the forge serves (`static: { build: ., output: dist }` in `.forge/app.yaml`) — a player needs only the play link, never the toolchain. The forge's file server serves files that exist; keep the game on one page. No server unless a change says why.
