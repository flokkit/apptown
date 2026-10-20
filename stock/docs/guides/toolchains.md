# Project toolchains

Keep required tool versions in committed declarations so a new session can
reconstruct its environment.

## Development tools

Put `mise.toml` or `.mise.toml` at the repository root. For example:

```toml
[tools]
node = "24"
python = "3.13"
```

Use the versions appropriate to your project. In a writable checkout:

```sh
mise install
mise exec -- node --version
```

The standard agent image supplies a baseline tool inventory. AppTown attempts
project toolchain preparation, but some preparation paths are best-effort.
Verify the actual tool/version before trusting a build or test. An agent starting
successfully is not proof every declared tool installed; a session whose install
failed is told so. When the declared toolchain does not install and the tests
then fail, the tests check reports AppTown's error ("the toolchain this
project declares could not be installed"), not a failure of the change: run the
checks again. A project chat's snapshot
is read-only; use a change workspace for edits and project-local installs.

## Test counts

The tests check sets `APPTOWN_TEST_REPORT` (`/work/.forge-test-report`) for its
command. A command that writes a report there, as JUnit XML or as a
`go test -json` stream, gets its counts recorded: how many tests passed,
failed and were skipped, and the first skipped tests' names. AppTown's
detected Go command, `go test -json ./... > "$APPTOWN_TEST_REPORT"`, does this
already, and its receipt's log is rendered from the stream: build output, each
package's result, and the full output of every test that failed or skipped,
with the skip's reason. Another runner opts in through its JUnit reporter, for
example `pytest --junitxml="$APPTOWN_TEST_REPORT"` or
`vitest run --reporter=junit --outputFile="$APPTOWN_TEST_REPORT"` in the step's
`command` or the project's test script.

The exit code alone decides the check. A pass with skipped tests, or whose
command wrote no report AppTown can read, reads "passed with notes" and says
which. Limits: the receipt keeps the last 4,000 bytes of the log; a report over
64 MiB is not read; while Go tests run, the live feed shows only what the
command prints to stderr, since its stdout is the report.

Installing a tool for an experiment is possible. Record the required setup in
the repository if the project depends on it; an unrecorded container install
can disappear when the runtime is replaced. Custom images have their own inventory.

## Build and deployment

A `build` part in `.apptown/app.yaml` is a Docker build context: its
`Dockerfile` supplies the build tools and the final runtime. An `image` source already carries its runtime; a `static`
source serves prepared files. AppTown does not automatically install mise tools
inside those deployed images.

Keep development and Docker build/runtime versions compatible. A Java project,
for example, may need a JDK for development and a compatible JRE in the final
image. A version pin records intent; it does not freeze every transitive download
or prove the commands actually used that toolchain.

## Package routing

Bound registries can provide mirror settings during helper preparation. Nested
Docker builds and containers do not inherit those settings automatically; supply
needed build arguments or environment explicitly. Mirror routing is not an
enforced network allowlist. A refused download needs diagnosis; it does not by
itself prove an intentional security restriction.

[What chats and steps can use](/docs/guides/runtime-environments) explains
tools, private Docker, credentials and retention. The
[contract reference](/docs/guides/contract-reference) explains how to package
the application independently of a warm agent session.
