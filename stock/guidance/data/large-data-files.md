---
name: data/large-data-files
tags: data,repo,size
---
The forge refuses to save work that adds a file over 10 MiB, or more than
50 MiB of new files at once, to a project's repository: history is hard to
undo, and every clone, preview and build carries it. A chat or a step that
writes a large file cannot save until the file is gone, so plan for data
size before writing it.

Never commit a raw dump: a full gazetteer, a whole database export, a
downloaded dataset as it came. Keep only what the app uses, in one of two
ways.

**Trim and split, then commit.** Add a script (for example
`scripts/fetch-data.ts`, run with `npm run data`) that downloads the source
from a pinned URL (a versioned release or a dated file, never "latest"),
checks it against a SHA-256 written in the script, and stops if they
differ. It keeps only the fields and rows the app uses and writes the
result in pieces by a natural key: by first letter, country, region or
year, so the app loads the one piece it needs when it needs it (a dynamic
`import()` or a `fetch()` of a file under `public/data/`). Each piece is
well under 10 MiB; aim for under 1 MiB so the page stays quick on a phone.
Commit the script and its output. The build then needs no network, and
the script records where the data came from and how it was cut.

**Fetch and trim when the app is built.** When even the trimmed data is
too large to commit, the same script runs as part of `npm run build`
(a `prebuild` script) and writes into a folder `.gitignore` names. The
pinned URL and checksum are what make that build repeatable; a source that
moves or disappears fails the build with the checksum's words rather than
shipping different data.

Either way, the README says where the data comes from, its licence, and
the command that refreshes it.
