# Browser game projects

Status: current implementation. Checked on 18 September 2026.

The HTML game seed is an ordinary web project with a Docker packaging recipe.
Caddy serves the committed files and uses no-cache responses so clients can
revalidate after a release. The video-game and `frontend-only` seeds build
with npm and Vite and declare a static site with a build
(`static: { build: ., output: dist }`): their Dockerfile ends with a stage of
the built files, and the forge serves that output with its own file server,
so the page gets `/_forge/env.js` and the release witness
([deploy targets](deploy-targets.md#static-sites-and-readiness)).

Projects still declare native `app.yaml` inputs and use normal Change, check,
preview and landing operations. A `static:` folder is served as it is; a
build runs only when the contract declares one, in the Dockerfile it names.

A successful HTTP response or app-shell check is insufficient evidence that a
game is playable. Browser checks should exercise input, rendering and relevant
game behaviour. Tool/model guidance can assist, but it is not a substitute for
observing those behaviours.

Published historical games use the selected revision's source and packaging.
The native history cutover
records earlier launch evidence, including Night Racer. It is not a guarantee
that every original game bug has been repaired or every version was retested now.

## Implementation references

- [stock/seeds/html-game/Dockerfile](../../stock/seeds/html-game/Dockerfile)
- [stock/seeds/html-game/index.html](../../stock/seeds/html-game/index.html)
- [stock/seeds/video-game/Dockerfile](../../stock/seeds/video-game/Dockerfile)
- [internal/forge/preview/command.go](../../internal/forge/preview/command.go)
