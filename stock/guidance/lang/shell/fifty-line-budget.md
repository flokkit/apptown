---
name: lang/shell/fifty-line-budget
tags: lang,shell,simplicity
---
Shell only for tiny glue. set -euo pipefail and shellcheck always. Past ~50
lines — or the first nontrivial data structure, string surgery, or error handling —
rewrite in Go (if it ships) or Python (if it's tooling).
