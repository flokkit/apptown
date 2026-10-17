---
name: game/godot-web
tags: game,godot,3d,web
checks: [{"name": "web-export-preset", "expects": "export_presets.cfg has a Web preset exporting to build/web/index.html and project.godot names a main scene", "severity": "high", "evaluator": "sh", "sh": "[ -f project.godot ] || { echo 'no project.godot — this is not a Godot project'; exit 1; }; grep -q 'run/main_scene=' project.godot || { echo 'project.godot names no main scene (application/run/main_scene) — the web build would open on nothing'; exit 1; }; [ -f export_presets.cfg ] || { echo 'no export_presets.cfg — add a Web preset (name=\"Web\", platform=\"Web\", export_path=\"build/web/index.html\")'; exit 1; }; grep -q 'platform=\"Web\"' export_presets.cfg || { echo 'export_presets.cfg has no Web preset — add one named Web on platform Web'; exit 1; }; grep -q 'name=\"Web\"' export_presets.cfg || { echo 'the web preset must be named exactly Web — the build runs: godot --headless --export-release Web'; exit 1; }; grep -q 'export_path=\"build/web/index.html\"' export_presets.cfg || { echo 'the Web preset must export to build/web/index.html — the Dockerfile serves that folder'; exit 1; }", "title": "Configure the Godot browser export"}, {"name": "compatibility-renderer", "expects": "project.godot uses the Compatibility renderer (gl_compatibility), the one that reaches WebGL 2", "severity": "medium", "evaluator": "sh", "sh": "[ -f project.godot ] || exit 0; grep -q 'renderer/rendering_method=\"gl_compatibility\"' project.godot || { echo 'project.godot does not set renderer/rendering_method=\"gl_compatibility\" — Forward+ and Mobile do not run in browsers; add under [rendering]: renderer/rendering_method=\"gl_compatibility\"'; exit 1; }", "title": "Use the Godot renderer supported by browsers"}, {"name": "no-import-cache-tracked", "expects": "the .godot/ import cache is not committed and .gitignore excludes it", "severity": "medium", "evaluator": "sh", "sh": "if [ -d .godot ]; then echo '.godot/ (the import cache) is committed — it is per-machine and regenerated; git rm -r --cached .godot and add .godot/ to .gitignore'; exit 1; fi; grep -qs '^\\.godot' .gitignore || { echo '.gitignore does not exclude .godot/ — add a line: .godot/'; exit 1; }", "title": "Remove Godot import caches from version control"}, {"name": "no-export-output-tracked", "expects": "no web export output (build/, .wasm, .pck) is committed — the build makes it", "severity": "medium", "evaluator": "sh", "sh": "bad=$(find . -path ./.git -prune -o \\( -name '*.wasm' -o -name '*.pck' \\) -type f -print 2>/dev/null | head -5); if [ -n \"$bad\" ] || [ -d build ]; then echo \"export output is committed: ${bad:-build/} — the Dockerfile exports at build time; git rm --cached it and add build/ to .gitignore\"; exit 1; fi", "title": "Remove generated game exports from version control"}, {"name": "web-export-builds", "expects": "the headless web export succeeds in the pinned Godot image, produces index.wasm and index.pck, and stays under 80 MB", "severity": "high", "evaluator": "tool", "tool": "godot-web-export", "title": "Make the Godot browser export build successfully"}, {"name": "plays-in-browser", "expects": "the game is playable in a browser: keyboard AND mouse-or-touch input for moving and the main action, pointer capture only after a click, threads off unless isolation headers are declared", "severity": "medium", "evaluator": "llm", "scope": "tree", "files": ["project.godot", "export_presets.cfg", "**/*.gd", ".forge/app.yaml"], "rubric": "Judge the Godot project as a browser game. It must: (1) name a main scene in project.godot; (2) define movement and the main action for BOTH a keyboard and a pointer/touch (mouse buttons or touch taps/drags), in the input map or via InputMap.add_action in a script; (3) only capture the mouse (MOUSE_MODE_CAPTURED) after the player clicks — browsers refuse pointer lock before a user gesture; (4) keep variant/thread_support=false in the Web preset unless .forge/app.yaml declares cross-origin isolation headers. Cite file and line for each. Missing keyboard OR pointer/touch handling, capturing the mouse in _ready, or threads on without headers is a violation.", "title": "Make the game playable in a browser"}, {"name": "readable-gdscript", "expects": "GDScript reads like a beginner can follow it: typed variables and functions, short single-job functions, plain names, a comment above each game rule", "severity": "note", "evaluator": "llm", "scope": "tree", "files": ["**/*.gd"], "rubric": "Judge the GDScript for a beginner reader: variables and function signatures carry static type hints (var speed: float, func shoot() -> void), functions are short and do one job, names are full words, and each game rule (how fast, how many, what happens on a hit) has a one-line comment above it. Cite the worst offenders with file:line. A few misses are fine; systematic untyped, long, or cryptic code is a violation.", "title": "Make the game scripts easier to read"}, {"name": "runtime-stage-not-build-image", "expects": "the Dockerfile's final stage is not FROM barichello/godot-ci (any tag), the image this pack's seed builds with: a build stage makes the artifact and the final stage starts from caddy:2-alpine with only that artifact copied in. Judged from the FROM instructions as Docker reads them (continued lines, comments and a heredoc's body; a stage name resolves to the image it names); a base image given by a build argument is not judged, an instruction with two heredocs, or heredoc-like text inside a quoted shell string whose end is never found, is reported as not judged", "severity": "high", "evaluator": "sh", "sh": "[ -f Dockerfile ] || exit 0; img=$(awk 'function inst(i, s, a, d, c) { if (toupper($1) == \"FROM\") { i = 2; while ($i ~ /^--/) i++; s = $i; a = tolower(s); if (a in st) s = st[a]; if (toupper($(i+1)) == \"AS\") st[tolower($(i+2))] = s; last = s } else if (toupper($1) ~ /^(RUN|COPY|ADD)$/ && match($0, /(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_][^ \\t\"\\047<>|;&]*/)) { d = substr($0, RSTART, RLENGTH); c = $0; if (gsub(/(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_]/, \"\", c) > 1) many = 1; sub(/^[^<]?<</, \"\", d); hs = (d ~ /^-/); sub(/^-?[ \\t]*[\"\\047]?/, \"\", d); hd = d } } { sub(/\\r$/, \"\") } hd != \"\" { t = $0; if (hs) sub(/^\\t+/, \"\", t); if (t == hd) hd = \"\"; next } /^[ \\t]*#/ { next } b != \"\" && /^[ \\t]*$/ { next } /\\\\[ \\t]*$/ { sub(/\\\\[ \\t]*$/, \"\"); b = b $0 \" \"; next } { $0 = b $0; b = \"\"; inst() } END { if (b != \"\") { $0 = b; inst() } if (many || hd != \"\") print \"<<\"; else print last }' Dockerfile); [ \"$img\" = \"<<\" ] && { echo 'not judged: a heredoc whose end this check cannot find (heredoc-like text inside a quoted string reads as one), or an instruction with more than one heredoc'; exit 2; }; r=${img%@*}; case ${r##*/} in *:*) r=${r%:*};; esac; r=${r#docker.io/}; r=${r#library/}; [ \"$r\" = barichello/godot-ci ] || exit 0; echo \"the final stage runs FROM $img, the image your pack's seed builds with: its compiler, toolchain and their known vulnerabilities ship with the app. Build in a stage FROM barichello/godot-ci, then copy only the exported build/web folder into a final stage FROM caddy:2-alpine (game/godot-web shows the recipe)\"; exit 1", "title": "Run the application from a runtime image, not the build image"}]
---
The game is a Godot 4 project that ships to the browser. Godot's web export
is a static folder — `index.html`, the engine as `.wasm`, the game as
`.pck`, a small loader — and that folder is what the forge serves.

**One rail for the build: the Dockerfile.** A multi-stage image exports the
game headlessly in the pinned Godot image and serves the result with caddy.
The forge builds that image for previews, the gate and the deploy, and the
`web-export-builds` check runs the same export on every change, so "check
passes" means "the game builds for the web".

**Check what this session provides.** Writable change chats and steps that
declare `docker: true` have private Docker at `DOCKER_HOST=tcp://dind:2375`,
unless the operator disables it with `-no-private-docker`. Read-only project chats
have no daemon. Run `forge preview` in a change chat to build and boot the
Dockerfile independently on the forge server, including when private Docker
is disabled. The Godot executable lives in the pinned build image, not
necessarily on the chat's PATH. For direct headless checks, declare the matching engine in
`mise.toml` and run `mise install`, then `mise exec -- godot --headless ...`.
For the 4.4.1 build image, the mise tool pin is `godot = "4.4.1-stable"`
under `[tools]`. Keep that declaration with the project so a recreated console
can install it again. Alternatively, when private Docker is available, run
headless game checks in the repository's pinned Godot image (the one the
Dockerfile's export stage names). Do not assume a missing local executable means the server's
build toolchain is missing.

In a change chat or workflow implementation step with `DOCKER_HOST` set to
its private daemon, rehearse the Dockerfile with

    docker build -t game . && docker run --rm -p 8080:8080 game

and probe `http://dind:8080`. From a developer workstation using its local
Docker daemon, that address is `http://localhost:8080`. The game's Dockerfile
determines its container port; match the published port to that declaration.

**Renderer: Compatibility.** Only `gl_compatibility` reaches WebGL 2.
Forward+ and Mobile are desktop and native renderers; a project set to
them exports to a black canvas.

**Threads off.** The threaded web build needs cross-origin isolation
headers (`COOP`/`COEP`) from the front door. The single-threaded export
(`variant/thread_support=false`) needs nothing and runs everywhere,
including Safari. Turn threads on only when the contract declares the
headers.

**Input for everyone.** Keyboard for moving and mouse for looking and
shooting, plus touch: drag to look, tap to act. Capture the mouse only
after a click — browsers refuse pointer lock before a user gesture — and
release it on Escape.

**What git keeps.** Sources: `project.godot`, `export_presets.cfg`, scenes
(`.tscn`), scripts (`.gd`), assets. Not the `.godot/` import cache (per
machine, regenerated) and not `build/` (the export). Both are in the seed's
`.gitignore`.

**Language: GDScript**, typed. Godot's C# flavour does not export to the
web in the version we pin; Rust or C++ through GDExtension can, and is the
escape hatch for a hot path, not for game logic.

**Health.** The exported folder carries a static `healthz` file and the
contract declares it as the readiness path; nothing to write in the game.

Checks: the export must succeed in the pinned image and stay under the
size budget; the Web preset, the renderer, and the ignore rules are
mechanical; two rubrics look at playability in a browser and at whether
the GDScript reads plainly. Run them yourself before handing back with
`forge check`.
