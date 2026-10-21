# Testing: the tiers and their requirements

AppTown demands receipts from every project it governs; this is where its
own receipts come from.

## The tiers

**Unit** — `go test ./...`, AppTown suite plus the domain and infrastructure
packages. Requires Go, a `git` binary and a **Docker daemon**: the harness starts (or reuses) a `forge-test-db`
container on port 54329 and clones a template database per test, so every
DB-shaped test runs against real Postgres with the real migrations
(`APPTOWN_TEST_DB_URL` points it at a database of your own instead). That
container keeps its data in memory and runs with durability off (no fsync,
no synchronous commit, minimal WAL): nothing in it outlives a test binary,
and crash safety would cost the suite about a quarter of its wall clock. The
settings apply when the container is created; `docker rm -f forge-test-db`
makes the next run create it afresh. A database of your own
should be configured the same way, through `ALTER SYSTEM`, or it will be
the slow one. The
landing machinery is tested against real git repositories in temp
directories, the only honest way to test a merge. This tier holds the
institutional invariants: auth modes, the authorization kernel, approvals
and quorum, gates, the run lifecycle, workflow validation, adoption and
the baseline, the front door's routing. Agent steps use the **fake pi**
(`testdata/fake-pi`): a small program that reads its prompt for a directive
and performs exactly that — real edits, real commits, zero tokens. The
suite runs in a few minutes.

Database fixtures use PostgreSQL's `FILE_COPY` template strategy. It reduces
WAL volume at the cost of additional checkpoints, which makes the suite
faster on the in-memory test server. Each independent mutable test retains its own database,
and cleanup closes its connections and hands it to two background drop
workers; when two more are already waiting, the finished test waits too, so
a package never piles undropped clones onto the server's tmpfs (such a pile
fills it and restarts the server). External test servers may perform
differently; no production database setting changes.

**Race** — `make check-race` (`go test -race ./internal/... ./cmd/...`),
the unit tier under the race detector; the goroutine-heavy engine, chat and
preview state earn it. It is slower than the unit tier, and `make check`
does not run it; run it before changing concurrent code.


**Integration** — `go test -tags=integration ./internal/... -run
'^TestIntegration' -v`. Real containers through the production code paths:
the static, build and service-backed preview boots, the deploy and boot
reconciler standing a killed environment back up on its port, the front
door proxying a tier hostname end to end, a fleet conformance review, and the
console's private Docker daemon surviving stop/start and recreation. A Jobs
subprocess crash check establishes which durable queue rows survive a killed
worker and how fresh admission/retry behaves after restart. Include all
internal packages so extracted integration tests remain in the tier.
Needs the daemon and network access to pull small pinned images on first
run. A missing daemon is a **failure, not a skip** — a skip that always
skips is how a tier rots into decoration. It takes several minutes.
`go test -tags=integration ./internal/...` without `-run` takes the tag over
every package, not only the `TestIntegration` tests; some failures (the
Docker-selection tests beside real containers) show only in that form.

**Journeys** — `go test -tags=journey ./internal/forge/ -run TestJourney
-v`. Whole operational flows driven through the public API and CLI, the
way a person or agent uses AppTown, with the fake pi as every agent
(`testing-map.md`). About a minute.

**Opt-in tiers** — one test each, seconds to run, each needing something
the other tiers do not. Each has its own build tag. `pi` skips, saying
why, when `APPTOWN_PI_MODULE` is unset, so read its `-v` output rather than
trusting a pass; the other two fail without their prerequisites.


- `browser` (`go test -tags=browser ./internal/forge/ -run
  TestBrowserSecurityChromium -v`): a real Chromium against
  disposable AppTown state — session cookies, cross-origin refusals, the
  terminal's origin check. Needs node and Playwright, named by
  `APPTOWN_PLAYWRIGHT_MODULE` (its `index.mjs`). About 2 s.
- `pi` (`go test -tags=pi ./internal/forge/ -run TestInferencePinnedPi -v`):
  the Pi version the agent image pins,
  through the inference gateway with each provider's native SDK; no real
  provider. Needs `APPTOWN_PI_MODULE`, that Pi package's directory. Under 1 s.
- `inference_docker` (`go test -tags=inference_docker ./internal/console/pi/
  -run TestInferenceConsoleMountBoundary -v`): a chat's
  mounts in the `apptown-agent` image are read-only where they must be.
  Needs Docker and that image. About 1 s.

**Web** — `make check-web`: `tsc --noEmit` and the Vite build; `vitest` for
the pure functions (formatting, diff rendering, markdown, ranking; eleven
test files). Layout is verified by looking at rendered pages, not by tests.

**Live-fire** — not a test target. Real model calls, the private Docker
daemon, cloud reconciles, GitLab pushes, PTY chat sessions. These are
exercised by using the platform and watched through its own receipts.
Faking them would test the fakes. When one breaks a second time in the
same way, promote it into the integration tier with a real backing
service, never a mock.

## The gates the repository holds itself to

- `make check` builds the web assets, then runs every other stage at once
  (`scripts/gate.py`): the unit tier beside gofmt, `go vet`, staticcheck, the
  lint and shape checks, the web type check, lint and tests, and the checkers'
  own tests. Each stage is a make target with its own log; the gate ends with
  one line per stage (exit, seconds), shows the log of any that failed, and
  fails if any did. Its length is the unit tier's, not the sum of the stages.
  The unit tier is three stages: every package but the root, and the root
  package (`internal/forge`) as two test processes, split by name
  (`ROOT_TESTS_A` with `-run`, the rest with `-skip`, so every test runs
  once). On macOS Go starts each child process with `fork`, and frequent forks
  from a larger process sharply cut the memory work its other goroutines get
  done (a probe spawning ~200 processes a second lost most of it at a 300 MB
  heap); the root's tests start git thousands of times, so the more tests one
  process holds at once, the slower each runs. Two processes of eight tests
  at once beat one of eight or sixteen, and raising `-parallel` does not help:
  the test database is not the limit. On Linux Go starts processes with `CLONE_VFORK`
  instead, so on Linux a single `go test ./...` process does not slow this way.
  Each stage's make is a direct child of the gate, in its process group, so
  the queue's KILL reaches them all; on INT or TERM the gate signals only the
  makes it has not yet reaped, waits for them, and exits. Each run writes a
  new log directory, and a stage's result is the exit status that run reaped
  (`make check-gate` tests this on stub stages; it runs last and alone,
  after the other stages, because its tests send signals).
  `make check-go` and `make check-web` still run their parts one after
  another. The in-repo **pre-push hook** (`make hooks` installs it) runs
  `make check`; a push that fails it does not leave the machine.
- On a Mac the gate refuses to start while its own process is in macOS's
  throttled band (task priority 4, `MAXPRI_THROTTLE`), which is where Game
  Mode puts every other app while a game is the frontmost full-screen app.
  There it gets only the CPU the game leaves, many times slower than normal,
  and every gate fails on go test's 10-minute timeouts across many packages,
  which reads like broken code. `make check` runs the check before its web build;
  `gate.py` repeats it before any stage and exits 3,
  and when a gate ends still in that band its summary says its timeouts may
  be the demotion rather than the code, without changing its result. The
  remedy is to leave the game or take it out of full screen. CI's Linux jobs
  have no such band and skip the check.
- The public repository's CI is `.github/workflows/ci.yml`, on GitHub
  Actions, for every push to `main` and every pull request that changes
  code. Its `web` job runs make check's web stages (`npm ci`, `tsc
  --noEmit`, the Vite build, lint and vitest) and keeps the build; `checks`
  runs make check's checker stages over that build (source formatting,
  vet, staticcheck, the lint, shape, exec, SQL-clock, reachability and
  public-export checks, and govulncheck), then `make check-gate`; `test`
  runs the unit tier in four parts (the root package's two halves, the
  rest of `internal/forge`, and everything else) with the harness's own
  test PostgreSQL container. It does not run the race, integration,
  journey or opt-in tiers, which need a Docker daemon and images for 10 to
  60 minutes each; run those locally with the commands above.
- `make check-sql` and `make check-vuln` exist for ignored database writes
  and `govulncheck`.

## Conventions

- **Names collide with nobody.** Integration containers embed the test pid
  (`itest-<pid>`, change ids offset to 910xxx); test databases are clones
  named per test; a developer's live AppTown and a test run share a daemon
  without shooting each other.
- **Cleanup is `t.Cleanup`, always.** Every container and database created
  is removed even on failure.
- **Mutation checks for anything load-bearing.** An invariant test is not
  done until breaking the invariant makes it fail: a silently failed seed or
  a goroutine race can make a test pass while testing nothing.
- **Probes poll with deadlines**, never sleep-and-hope, and hit local ports
  directly — DNS and proxies are someone else's test.
- **A deadline is a hang guard, not a pass condition.** On macOS the first
  start of a freshly written executable (a test's fake `docker` script)
  waits for the system's first-run check of the new file, which under a busy
  suite can take over ten seconds. A wait that includes such a start ends
  on the event it waits for (or on the work finishing without it), bounded
  by the test binary's deadline, not a few seconds of wall clock.
- **A script a test runs is written with `execfile.Write`.** With
  `os.WriteFile`, a fork in a parallel test can inherit the open write
  descriptor, and Linux then refuses to run the file ("text file busy",
  Go issue 22315). Prefer the adapter's in-process fake when the test needs
  only an answer from docker or git; keep a script where the process
  boundary (arguments, streams, environment, exit status, cancellation) is
  the guarantee. The shape gate refuses other executable writes in tests
  (`testexecwrites`) and counts the scripts (`testexecfixtures`).
- **No silent reads or writes.** A source test rejects explicitly discarded
  single-row reads;
  native/rebound SQL parameter tests cover quoted question marks and JSON
  operators without banning valid PostgreSQL; the write helpers (`stateWrite`,
  `inTx`, `logWrite`) say which
  writes may be best-effort.
- **The stock library is under the same law it imposes**: a schema test
  strict-validates every stock workflow; a sync test loads the whole stock
  guidance set the way boot does.
- **Fixtures that name an id** say `OVERRIDING SYSTEM VALUE` and call
  `syncSequences` after; production code never names an id.

## Fast, isolated test execution

Independent default-tier tests call `t.Parallel()` explicitly. Database-backed
suites use eight concurrent tests by default, even on hosts with many CPUs:
a test can open several database pools or race transaction writers internally.
An explicit `go test -parallel N` overrides this limit. Each database fixture
still has its own migrated template clone and each application its own directory.
Go already runs different packages concurrently.

Keep tests serial when they replace package-wide hooks or change the environment
or logger. The same applies to callers of helpers that do these things. Serial
test cleanup finishes before Go resumes parallel tests. DigitalOcean fixtures
supply per-instance `digitalocean.Options` through Deployment's constructor;
API routing, polling and external effects need no global replacement.
Their independent tests and release-ownership cases can run in parallel. Application-owned fake
Docker clients and database-local veto triggers can run concurrently. Wait for
background work in cleanup before dropping its database or directory. Do not add `t.Parallel()`
inside shared fixture helpers: the caller must choose and expose scheduling.

When table cases own independent fixtures, register their parallel subtests in
one flat loop. A synchronous group wrapped around parallel cases can keep an
ancestor's parallel slot occupied while its children wait in the queue, and can
serialize later groups. Combine group/case names in one `t.Run` when the grouping
is only descriptive; retain real sequencing when cases share mutable fixtures.

`failNextStatement(t, db, fragment)` arms one database and its transactions,
including the native query handles used by sqlc. A compare-and-swap consumes the
fault exactly once; another test's database cannot intercept it. Its cleanup
cannot clear a newer installation. Rollback and read-failure tests can therefore
run concurrently, including tests that create transactions before arming a fault.

Preview records and boot locks live in `previewRuntime`, owned by each App;
change IDs and release names from another application cannot collide. The
model-catalog cache and its invalidation are also per App. Probe timing belongs
to an `appProbe`; the nil/default prober retains the real three-second settle
window, while fixtures configure their own instance; no production timeout or
shutdown bound is shortened for a test. The MCP session map is synchronized and
checks application/user ownership, so its presence alone does not require a
serial test.

River's own maintenance election and rescue machinery is not tested here; it
would add a long wait to the default suite for River's behaviour, not AppTown's.
AppTown's queue admission, atomic receipts, retries, attempt fencing and bounded
shutdown are. The remediation shutdown test uses `testing/synctest` to exercise
the real five-second cap with virtual time.

An `integration`-tagged Jobs subprocess test checks the immediate process-death
gap and AppTown's exclusive-startup settlement. It kills a worker and waits for
confirmed exit, without waiting for rescue or shortening timeouts, and adds no
subprocess to the default suite.

Go's test cache is on everywhere except the gates of record. The push gate
(`make check`), CI, the race tier and the integration and journey
tiers run with `-count=1`: each push is witnessed fresh, and the tagged
tiers exist to exercise Docker, PostgreSQL and Git, which the cache cannot
see. Local and targeted runs (`go test ./internal/forge/scm`) use the cache;
a stale pass there is caught by the push gate. Add `-count=1` locally only
to re-run on purpose: chasing a flake, timing a run, or after changing
something outside Go such as the installed `git` or the test database.

Use `make check` for the full local gate. Its dependency ordering also makes
`make -j2 check` safe: Vite finishes replacing `web/dist` before Go compiles the
embedded assets. These two builds cannot overlap; tests within Go still do.
To check scheduling changes, run
`go test -race -shuffle=on -count=1 ./internal/forge`. For timings, use
`go test -json -count=1 ./internal/forge`, or `make check GO_TEST_FLAGS=-json`
to retain the full gate while collecting timing events. Compare the package's elapsed time,
not the sum of parallel tests' durations. Keep database-heavy benchmark runs
separate so they do not compete for the same server.

Add `t.Parallel()` to a new independent test by default; the rule above
(environment, logger or a package-level hook) is what keeps one serial.

## Where the next coverage comes from

In order of value: a real-provider tier for the cloud drivers behind a
record-and-replay HTTP fake (the App Platform driver is tested against a
fake); and a containerised chat harness so the chat door and delegation
round-trip leave live-fire. The ceiling on unit coverage is architectural:
the root package is glue over everything, and the ownership boundaries in
[domains.md](domains.md) move the number, not more handler tests.
