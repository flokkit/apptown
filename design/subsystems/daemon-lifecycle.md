# Daemon startup and shutdown

`cmd/apptownd` owns startup ordering, listeners and background-task lifetime.
Application constructors build their dependencies; they do not repair persisted
work or start workers.

## Startup

1. Open the database and apply schema migrations. Verify-only mode also performs
   this step; it is not a read-only database inspection mode.
2. In normal writable mode, run required database repair before provider recovery,
   execution resumption or request serving. Seed and verify-only modes skip repair.
3. Repair interrupted steps and reviews, and releases without durable deployment
   attempts. Preserve matching live child workflow calls, releases whose
   deployment attempts need their owner's recovery, and steps whose attempt
   recorded a task (`tasks`): the task may still run.
4. Run the remaining owner startup operations. Branch publications and merges
   are inspected before recorded tasks are sorted: a task whose step moved on
   is stopped, and the rest are continued by the workflow dispatcher in their
   place in their stage, never started twice, before the steps after them. Bind every configured listener before starting recurring jobs,
   scheduling, health and deployment observation.

Required repair is one transaction. A statement or commit failure prevents
writable startup. Release history is written only for rows changed by that
transaction, so repeating repair does not duplicate receipts. Construction of a
second App is not a restart and must not trigger repair.

The journal records repair start, elapsed time, committed counts and the counts
preserved for owner recovery, including zero counts. A failed repair logs an
error instead of a success summary. Provider recovery has its own outcomes;
the required-repair summary does not report those providers as recovered.

### Docker modes

Configuration validation runs before the database opens. `-no-local-docker`
requires a worker (`-worker-url`) and an external database (`-db-url`), because
the built-in database is a container on the local Docker. An AppTown in the mode
cannot see work it once ran on its own Docker, so the switch into it is a
procedure the operator follows before the first start in the mode; see
[workers](../operations/workers.md#switching-an-apptown-to-no-local-docker).

In normal mode the daemon also serves its control socket,
`<data>/apptownd.sock` (mode 0600), through which `apptownd backup` asks the
running AppTown for a whole-AppTown backup (`internal/apptown/instancebackup/control.go`).
Whoever can open it can already read the data directory and every secret in
it, so it grants no authority of its own. A socket that cannot be made is
logged and the daemon runs on without it. `apptownd restore` is a one-shot
that runs before the restored AppTown's first start; it ends after the
verify-only startup.

A bind failure closes listeners already opened. A running listener or required
worker-loop failure stops the daemon. Verify-only mode serves its HTTP lifecycle
without background tasks.

## Shutdown

SIGINT or SIGTERM starts one **five-second deadline**. Signal handling is
installed after synchronous startup; a second shutdown signal terminates the
process immediately.

The coordinator cancels recurring tasks and requests HTTP and execution-owner
shutdown concurrently. App cancels console control loops and closes Workflow
admission before waiting. Remediation passes, eval passes and narrative
refreshes are queue jobs and stop with the queue. Repeated stop
calls cannot reopen admission. The synchronous HTTP narrative refresh remains
owned by its request. Individual refresh failures do not stop other services. Telemetry flushes after producers drain, using only the time
remaining on the same deadline.

At expiry the coordinator stops waiting, reports unfinished components and returns
to main. HTTP drains force-close ordinary connections. There is no subsequent
unbounded database close or worker wait. An external command or goroutine that
ignores cancellation may continue until process exit; a remote effect may outlive
AppTown entirely.

## Persisted work

Shutdown preserves console containers, private daemons and session files. A
restart can adopt them without waiting for all user work to finish at shutdown.
The [console guide](console.md) describes retention and resume behaviour.

Pending workflow work and remediation items retain their restart rules.
Interrupted River jobs retain their queue state for retry or exhaustion under
[the configured job policy](jobs.md). Shutdown does not record unfinished work as
successful. [Deployment recovery](deployment.md) observes admitted effects
before settling uncertain outcomes.

The daemon tests cover listener failures, task cancellation, actual SIGTERM and
an uncooperative task within the shared deadline. Startup tests cover repair
rollback, repeated repair and preservation of matching child calls.

The [lifecycle decision](../decisions/background-lifecycle.md) records why this
uses the existing ownership convention and what a DSKit replacement must remove.

## Implementation references

- [cmd/apptownd/main.go](../../cmd/apptownd/main.go)
- [cmd/apptownd/runtime.go](../../cmd/apptownd/runtime.go)
- [cmd/apptownd/startup.go](../../cmd/apptownd/startup.go)
- [internal/apptown/startup_recovery.go](../../internal/apptown/startup_recovery.go)
- [internal/apptown/no_local_docker.go](../../internal/apptown/no_local_docker.go)
- [internal/apptown/config.go](../../internal/apptown/config.go)
- [cmd/apptownd/runtime_test.go](../../cmd/apptownd/runtime_test.go)
- [cmd/apptownd/startup_test.go](../../cmd/apptownd/startup_test.go)
