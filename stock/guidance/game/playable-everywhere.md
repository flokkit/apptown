---
name: game/playable-everywhere
tags: game,family
checks: [{"name": "touch-and-keys", "expects": "the game handles both touch/pointer and keyboard input and scales to the window", "severity": "note", "evaluator": "llm", "scope": "tree", "files": ["**/*.js", "**/*.ts", "**/*.html", "**/*.css"], "rubric": "The game must be playable with a keyboard AND with a pointer/touch (mouse, touchscreen): both kinds of input handler exist for every player action (keydown/keyup alongside pointerdown/touchstart/click), and the canvas or layout scales to the window (resize handling, viewport units, or CSS scaling) rather than assuming one fixed screen size. Cite the handlers you found; if one kind of input is missing, or the size is fixed, that is a violation, and evidence with no game code at all is one too.", "title": "Support touch and keyboard controls", "appliesTo": ["static"]}]
---
The game must be playable with keyboard AND touch, fit a phone screen (scale the canvas, never assume a size), and restart without a page reload (a tap or key after game over). Test both input modes before finishing.
