---
name: game/kid-readable
tags: game,family,prose
checks: [{"name": "reads-like-a-kid-can", "expects": "names are full words, functions are short, game rules carry plain-words comments", "severity": "note", "evaluator": "llm", "rubric": "Judge the diff's code readability for a beginner: full-word names (not abbreviations), short single-job functions, a plain-language comment above each game rule. Cleverness that saves lines at the cost of readability is a violation.", "title": "Make the game code understandable to a young reader"}]
---
Kids will READ this code — that is half the point of the project. Short functions with one job. Full words for names (basket, not bkt; timeUntilNextEgg, not t). A comment above each game rule saying what it does in ordinary words ("the more you catch, the faster eggs fall"). Prefer the obvious way over the clever way, even when the clever way is shorter.
