# Testing: the tiers and their requirements

Status: **as built**, refreshed 2026-10-11. Written 2026-08-31 to close a
debt; the Postgres move (2026-09-05) changed the unit tier's requirements,
and this version says so. The forge demands receipts from every project it
governs; this is where its own receipts come from.

## The tiers

**Unit** — `go test ./...`, the Forge suite plus the domain and infrastructure
packages. Requires Go, a `git` binary, and — since the Postgres move —
a **Docker daemon**: the harness starts (or reuses) a `forge-test-db`
container on port 54329 and clones a template database per test, so every
DB-shaped test runs against real Postgres with the real migrations
(`FORGE_TEST_DB_URL` points it at a database of your own instead). That
container keeps its data in memory and runs with durability off (no fsync,
no synchronous commit, minimal WAL): nothing in it outlives a test binary,
and crash safety was costing the suite a quarter of its wall clock
(126 s to 94 s on 19 September 2026). The settings apply when the container
is created; a container from before then keeps the old ones until it is
removed once with `docker rm -f forge-test-db`. A database of your own
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
WAL volume at the cost of additional checkpoints; paired complete-suite runs
on the local in-memory PostgreSQL 16 server were about 53 s versus 63 s with
the default strategy. Each independent mutable test retains its own database,
and cleanup closes its connections and hands it to two background drop
workers; when two more are already waiting, the finished test waits too, so
a package never piles undropped clones onto the server's tmpfs (on
11 October 2026 such a pile filled it and restarted the server). External test
servers may have different performance; no production database settings change.

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
skips is how a tier rots into decoration. The command took 446–520 s on
a developer Mac (9 October 2026). `go test -tags=integration ./internal/...`
without `-run` takes the tag over every package, not only the
`TestIntegration` tests; some failures (the Docker-selection tests beside
real containers) show only in that form. That run took 754 s on the same Mac.

**Journeys** — `go test -tags=journey ./internal/forge/ -run TestJourney
-v`. Whole operational flows driven through the public API and CLI, the
way a person or agent uses the forge, with the fake pi as every agent
(`testing-map.md`). About a minute: 55 s on a developer Mac (9 October
2026).

**Opt-in tiers** — one test each, seconds to run, each needing something
the other tiers do not. Each has its own build tag. `pi` skips, saying
why, when `FORGE_PI_MODULE` is unset, so read its `-v` output rather than
trusting a pass; the other two fail without their prerequisites.


- `browser` (`go test -tags=browser ./internal/forge/ -run
  TestBrowserSecurityChromium -v`): a real Chromium against
  disposable Forge state — session cookies, cross-origin refusals, the
  terminal's origin check. Needs node and Playwright, named by
  `FORGE_PLAYWRIGHT_MODULE` (its `index.mjs`). About 2 s.
- `pi` (`go test -tags=pi ./internal/forge/ -run TestInferencePinnedPi -v`):
  the Pi version the agent image pins,
  through the inference gateway with each provider's native SDK; no real
  provider. Needs `FORGE_PI_MODULE`, that Pi package's directory. Under 1 s.
- `inference_docker` (`go test -tags=inference_docker ./internal/console/pi/
  -run TestInferenceConsoleMountBoundary -v`): a chat's
  mounts in the `forge-agent` image are read-only where they must be.
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
  process holds at once, the slower each runs. Measured on 11 October 2026:
  root alone at eight tests at once 113 s (quiet host) and 165–178 s (busy
  host), at sixteen 141 s and 162 s, as two processes of eight 81–84 s, as
  three 97 s; the full gate's test stages fell from 174 s to 128 s. Raising
  `-parallel` does not help; the test PostgreSQL used under one of its four
  CPUs on average (peak 1.4). On Linux Go starts processes with `CLONE_VFORK`
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
  There it gets only the CPU the game leaves: on 18 October 2026 a one-thread
  loop ran 68 times slower than from a launchd job, and every gate failed on
  go test's 10-minute timeouts across 30 packages, which read like broken
  code. `make check` runs the check before its web build;
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
  named per test; a developer's live forge and a test run share a daemon
  without shooting each other.
- **Cleanup is `t.Cleanup`, always.** Every container and database created
  is removed even on failure.
- **Mutation checks for anything load-bearing.** An invariant test is not
  done until breaking the invariant makes it fail; two tests once passed
  while testing nothing (a silently failed seed, a goroutine race) and only
  mutation runs exposed them.
- **Probes poll with deadlines**, never sleep-and-hope, and hit local ports
  directly — DNS and proxies are someone else's test.
- **A deadline is a hang guard, not a pass condition.** On macOS the first
  start of a freshly written executable (a test's fake `docker` script)
  waits for the system's first-run check of the new file; under a busy
  suite that took over ten seconds. A wait that includes such a start ends
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
Go already runs different packages concurrently. The September 19 comparison
of four and eight concurrent tests is recorded in the

Keep tests serial when they replace package-wide hooks or change the environment
or logger. The same applies to callers of helpers that do these things. Serial
test cleanup finishes before Go resumes parallel tests. DigitalOcean fixtures
supply per-instance `digitalocean.Options` through Deployment's constructor;
API routing, polling and external effects no longer require global replacement.
Their independent tests and release-ownership cases can run in parallel. Application-owned fake
Docker clients and database-local veto triggers can run concurrently. Wait for
background work in cleanup before dropping its database or directory. Do not add `t.Parallel()`
inside shared fixture helpers: the caller must choose and expose scheduling.

When table cases own independent fixtures, register their parallel subtests in
one flat loop. A synchronous group wrapped around parallel cases can keep an
ancestor's parallel slot occupied while its children wait in the queue, and can
serialize later groups. Combine group/case names in one `t.Run` when the grouping
is only descriptive; retain real sequencing when cases share mutable fixtures.

`failNextStatement(t, db, fragment)` now arms one database and its transactions,
including the native query handles used by sqlc. A compare-and-swap consumes the
fault exactly once; another test's database cannot intercept it. Its cleanup
cannot clear a newer installation. Rollback and read-failure tests can therefore
run concurrently, including tests that create transactions before arming a fault.

Preview records and boot locks live in `previewRuntime`, owned by each App;
change IDs and release names from another application cannot collide. The
model-catalog cache and its invalidation are also per App. Probe timing belongs
to an `appProbe`; the nil/default prober retains the real three-second settle
window, while fixtures configure their own instance. No production timeout or
shutdown bound was shortened. The existing MCP session map is synchronized and
checks application/user ownership, so its presence alone does not require a
serial test.

The River crash-rescue probe and its subprocess helper were removed at the
operator's request. They exercised River's maintenance election/rescue machinery
and added a roughly 21-second wait to the measured default suite. Forge's queue
admission, atomic receipts, retries, attempt fencing and bounded shutdown tests
remain. The remediation shutdown test uses `testing/synctest` to exercise the
real five-second cap with virtual time; production timing is unchanged.

A separate `integration`-tagged Jobs subprocess test now checks the immediate
process-death gap and Forge's exclusive-startup settlement. It kills a worker and
waits for confirmed exit, without waiting for rescue or shortening timeouts. It
is not the retired maintenance/rescue probe and adds no subprocess to the default
suite.

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

Initial scheduling validation on 11 September 2026: the Forge package took 162.105 seconds before
these changes and 94.812 seconds in the final full gate (about 42% less elapsed
time, on the same machine). That checkpoint had 553 explicitly parallel top-level
tests, 549 more than before. All 803 remaining top-level tests passed under
`-race -shuffle=on` in 125.597 seconds (seed `1789092902059038000`). The virtual
shutdown test also passed 20 consecutive race-detector runs. `make -j2 check`
passed with the ordered web/Go build, including generated SQL verification,
formatting, vet, staticcheck, all default Go tests and the web build. These are
observed local timings, not a fixed runtime promise for CI or a busy database.

### Follow-up: remove shared state that forces serial execution

With the concurrency limit still four, the measured serial phase fell from
53.540 to 17.817 seconds, and the complete Forge package from 92.506 to 73.968
seconds (about 20% less total time). This batch made 177 existing tests parallel;
with four new isolation regressions, 734 of 807 top-level tests opted into
parallel execution. Tests still exercise real database rollback, authorization,
receipts, preview state, boot serialization and model-catalog responses.

Validation: `make check GO_TEST_FLAGS=-json` passed all standard gates;
`go test -race -shuffle=on -json ./internal/forge -count=1` passed all 807 tests
in 94.530 seconds (seed `1789094222018398000`), compared with 125.597 seconds
before the batch. `make check-sql` found no ignored writes. The integration tier
also compiled with `-tags=integration -run '^$'`; its live-container tests were
not run. The normal full gate retained the existing Vite bundle-size warning.

### Follow-up: typed conformance evaluation

`checkEvaluator` replaces the process-wide shell, source-tool, model and runtime
hooks with three application-bound operations. Shell checks and source scanners
share one command executor; checklist interpretation receives an already-resolved
model and packed evidence. These evaluation methods do not receive App. Production
commands still use Docker and runtime checks still observe a standing deployment.
Fixtures configure their evaluator before use, with no global restoration.

`checkResult` is the typed receipt shared by every evaluator, the baseline,
acceptance checks and fleet review. It retains exact block/version/check identity,
execution observations, evidence and baseline annotations until serialization.
Attribution now matches the full finding identity, including the adopted version;
a same-named older check cannot supply the evaluator for a newer finding. Explicit
exit 0, zero duration and `gates: false` survive JSON serialization. AI findings
remain non-gating. `reviewStore` owns creation and typed identity/history reads,
sharing one column list and decoder; it returns database errors to its callers.
Fleet progress writes, verdict settlement and best-effort projections remain
separate work, not a completed review-domain extraction.

The remediation fixture now records a real selected, pinned workflow instead of
bypassing eligibility through `canRunOverride`. This batch removes all five global
hooks, makes 36 more existing tests parallel, and adds three regressions for
concurrent evaluator isolation, exact provenance/JSON receipts and store failures.
773 of 810 top-level tests are explicitly parallel; 37 remain serial. App's
production method count falls from 919 to 915.

Validation at the unchanged concurrency of four: the serial phase fell from
17.817 to 9.459 seconds (47% less), and the normal Forge suite from 73.968 to
69.584 seconds (6% less). `make check GO_TEST_FLAGS=-json` passed the full web,
generated-SQL, formatting, vet, staticcheck and Go gates. The shuffled race run
passed all 810 top-level tests in 90.448 seconds (seed `1789095397306939000`).
`make check-sql` found zero ignored writes across 242 production targets. The
integration tier compiled; live-container integration tests were not run.
The existing Vite bundle-size warning remains.

Runtime priorities remaining after this batch (previous normal-run serial costs):

1. Cloud-driver test hooks: 17 tests, about 3.1 seconds. Own the API client and
   reconcile operations on the driver/runtime instance.
2. Agent runner: six tests, about 2.6 seconds. Supply the runner through the
   existing execution owner so fixtures do not replace `piRunner` globally.

Environment and logger tests total about one second; keep them serial unless
another correctness need justifies changing the process boundary. These numbers
are serial work available to overlap, not a promise of equal wall-clock savings:
Postgres fixture creation, I/O and CPU contention still limit the parallel phase.

### Follow-up: new tests are parallel too (30 September 2026)

The Forge package had drifted back to 185.5 seconds (531 top-level tests, of
which 104 ran serially and took 132 seconds between them; the parallel tests took
about 28). Most serial ones came from the isolated-execution work and build their
own App, database and directory; they had simply not called `t.Parallel()`. The
94 that change no environment, logger or package-level variable now do; ten stay
serial (`t.Setenv`, `log.SetOutput` directly or through `BridgeLogs`, a package
hook). A 95th was made parallel at first and put back: it replaces the standard
logger's writer through a helper, which a search of the test's own body missed
(the independent review caught it). The package took 88.3
seconds afterwards on the same machine, and passed twice under
`go test -race -shuffle=on -count=1 ./internal/forge` with no race reported.
Add `t.Parallel()` to a new independent test by default; the classification rule
above is what keeps one serial.

## Where the next coverage comes from

In order of value: a real-provider tier for the cloud drivers behind a
record-and-replay HTTP fake (the App Platform driver is tested against a
fake today); a containerised chat harness so the chat door and delegation
round-trip leave live-fire; a fresh era snapshot in the Postgres format so
the upgrade test runs again (`testing-map.md` §5). The ceiling on unit
coverage is architectural: one package is glue over everything; the
boundaries in `code-structure.md` move the number, not more handler tests.
