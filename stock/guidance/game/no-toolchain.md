---
name: game/no-toolchain
tags: game,family,simplicity
checks: [{"name": "plain-files-only", "expects": "no package.json, node_modules, or bundler configs", "severity": "high", "evaluator": "sh", "sh": "BAD=$(ls package.json vite.config.* webpack.config.* tsconfig.json 2>/dev/null; [ -d node_modules ] && echo node_modules); [ -z \"$BAD\" ] || { echo \"toolchain files found in a no-toolchain game project: $BAD\"; exit 1; }", "title": "Keep the game in plain HTML, CSS and JavaScript"}, {"name": "game-shell-exists", "expects": "index.html exists at the static root", "severity": "high", "evaluator": "sh", "sh": "[ -f index.html ] || { echo \"no index.html at the repo root — the game has no front door\"; exit 1; }", "title": "Provide the game’s starting page"}]
---
Plain files only: HTML, CSS, JS, images, sounds. No package.json, no node_modules, no bundler, no framework, no build step, no TypeScript. The file you edit is the file that ships. Small games should be a single index.html.
