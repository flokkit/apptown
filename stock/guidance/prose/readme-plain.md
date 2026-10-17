---
name: prose/readme-plain
tags: prose,docs,simplicity
checks: [{"name": "readme-not-a-brochure", "expects": "README.md stays under 60 lines with no badges", "severity": "note", "evaluator": "sh", "sh": "[ ! -f README.md ] && exit 0; L=$(wc -l < README.md); [ \"$L\" -gt 60 ] && { echo \"README.md is $L lines; guidance caps it at 60 — move detail to a linked design doc\"; exit 1; }; grep -q \"shields.io\\|img.shields\" README.md && { echo \"README.md carries badges; guidance says plain door, not brochure\"; exit 1; }; exit 0", "title": "Describe the project plainly in the README"}, {"name": "readme-plain-tone", "expects": "README follows the 4-part plain format without marketing tone", "severity": "note", "evaluator": "llm", "scope": "tree", "files": ["README.md"], "rubric": "README.md follows the plain format: the name plus a one-sentence purpose, the commands to run it, then one-line configuration entries and a note on where data lives. The configuration and data parts are only for apps that have them: an app with no configuration, or one that keeps no data, skips that part, and a missing configuration or data section is never a violation on its own. Violations: marketing adjectives (blazing, powerful, seamless, next-generation), emoji, feature claims the repo does not back, roadmaps, ASCII art, walls of badges. Cite the offending line. A missing README is a violation. A short, plain, true sentence is never a violation for where it sits or for going beyond the four parts: how to play or use the app, what a key does, that the starter is meant to be changed, or how the forge serves it (the play link shows the latest landed version, which the platform backs, not the repository's files). Flag tone and unbacked claims, not plain facts.", "title": "Use a plain, factual tone in the README"}]
---
README.md is a plain door, not a brochure. Format, in order:

1. Project name and ONE sentence saying what it does.
2. Run: the commands to build, run, and test — copy-pasteable, nothing else.
3. Configure: each env var / flag, one line each. Skip if none.
4. Data: where state lives and what survives a redeploy. Skip if stateless.

That is the whole format. Hard rules: no badges, no emoji, no screenshots,
no marketing adjectives, no feature lists for features that do not exist,
no roadmap, no ASCII art, no table of contents. A README under 60 lines is
almost always better than one over it. If something needs a long
explanation, that explanation belongs in a design doc the README links to.
