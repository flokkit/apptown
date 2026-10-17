# Contributing

Thank you for your interest. Please read this before you open anything.

## Issues are welcome

Bug reports, questions and suggestions are welcome as
[issues](https://github.com/flokkit/approost/issues). Use the bug report
template, and never paste secrets, tokens, passwords or private host names
into an issue. A security problem goes through private reporting instead
([SECURITY.md](SECURITY.md)).

## Pull requests by arrangement

This repository is exported from the maintainers' development repository, one
commit per export, so a pull request cannot be merged here as it stands. If you
would like to change something:

1. **Open an issue first** and describe the change. Wait for a maintainer to
   agree before you put work into it.
2. For a small fix, a maintainer may port it into the development repository
   themselves, with credit to you in the commit, and it arrives here with the
   next export.
3. For anything larger, agree the approach in the issue first.

A pull request opened without an issue may be closed with a pointer here.

## Working on the code

- Build: see the quickstart in the [README](README.md).
- Check: `make tools` once (the pinned analyzers), then `make check`, which runs
  the same checks as CI ([.github/workflows/ci.yml](.github/workflows/ci.yml))
  plus the web tests. It needs Docker: the database tests start their own
  PostgreSQL.
- Read [CLAUDE.md](CLAUDE.md) for the design rules the code follows and
  [design/README.md](design/README.md) for where each part is described.

By contributing you agree that your contribution is licensed under the Apache
License 2.0 ([LICENSE](LICENSE)), and that you follow the
[code of conduct](CODE_OF_CONDUCT.md).
