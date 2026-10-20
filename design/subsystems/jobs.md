# Background jobs

Forge uses River OSS v0.47.0 in its existing PostgreSQL database and daemon.
River owns admission, deduplication, claiming, retry timing, periodic scheduling,
and rescue after a worker dies. Forge owns workflow gates, deployment attempts,
release settlement and saved conversations. A queue result cannot override any
of those domain records.

## Boundary and admission

`internal/forge/jobs/river.go` binds an immutable operation registry to River using
`riverdatabasesql`, the existing SQL pool and poll-only operation: four workers
on the default queue, two on the `operations` queue that runs the
operations ledger's work (`internal/forge/operations`), and one on the
`conformance` queue that runs fleet reviews and what-ifs, so they run one at a
time without holding up take-downs, one on the `remediation` queue, so two
remediation passes never overlap, and one on the `evals` queue, so two eval
passes never overlap. A kind's queue is recorded once
(`jobQueues` in `job_wiring.go`) for both its registration and every enqueue.
It has no import of the root App package. `internal/forge/job_wiring.go` supplies handlers, retry
policy and reporting.

An open request is unique by operation and key. A duplicate preserves the first
request's arguments, schedule and attempt policy. Completed work releases its
key. Active states are available, pending, running, retryable and scheduled.
JSON numbers preserve int64 precision through admission and execution, including
PostgreSQL's JSON array conversion path.

Completion, abandonment and deployment uncertainty insert their required jobs
inside the same transaction as the domain outcome. Owners do so through
`jobs.Enqueuer`; its one implementation (`jobQueue` in `job_wiring.go`) puts
each job on its kind's queue with its key's attempt budget and refuses an
unknown kind. The operations ledger inserts on its own queue
(`InsertOperationTx`). A failed insert rolls back
the outcome. HTTP completion returns after that transaction; follow-ups run in
the daemon. Explicit advancement of a completed run requeues retained failed or
cancelled follow-ups; it does not execute them inside the request.

## Retry policy

| Operation | Attempts per admission | Interruption behavior |
| --- | --- | --- |
| Inventory | 2 | River may retry. |
| Deployment observation | 3, 20-minute handler budget | Observe the same attempt; never repeat create/apply. |
| Completion cleanup, adoption, parent handoff, landing admission | 3 | Repeat only under existing domain identity and revision checks. |
| Abandonment cleanup | 3 | Cleanup may retry. |
| Deleted project's cleanup (`project-cleanup`) | 5 per round | Then the cleanup is marked as needing a person ([project deletion](project-deletion.md)). |
| Completion/abandonment narrative | 1 | Explicit regeneration/rerun after interruption. |
| Narrative refresh (`narrative`, key `change/<id>`) | 1 | Asked for when a chat ends, a turn hands back new work, or a branch is imported; merged while queued or running. It runs as the forge's engine. A refresh that returns an error fails the job; completion is not a publication receipt (`change.narrative-updated` is). The next trigger or the Distill button refreshes again. |
| Backup (`backup`), backup sweep (`backup-sweep`), database backup (`db-backup`) | 1 | Explicit rerun or a later calendar tick. |
| Release execution and Docker rebuild | 1, 20-minute handler budget | Inspect the admitted attempt or explicitly redeploy. |
| Operation (`operation`, the `operations` queue) | 1, two-hour handler budget | The operation reads unknown; asking again is a fresh admission. |
| Remediation pass (`remediation-pass`), the `remediation` queue | 1, 20-minute handler budget | Scheduled every minute (key `scheduled`) and asked for by a person's action (key `wake`); each key is coalesced while queued or running. Only one pass runs at a time, so a merged request waits for the running pass to end. A stopped pass stops between operations; the next pass reconciles from the tables. |
| Eval pass (`eval-pass`), the `evals` queue | 1, two-hour handler budget | Scheduled every minute (key `scheduled`) and asked for when an eval is started or continued (key `wake`), each key coalesced while queued or running. A stopped pass returns cancellation; an arm it cut off while copying or starting fails as interrupted at the next pass, which carries on from the tables. |
| Fleet review (`conformance-review`) and what-if (`what-if`), the `conformance` queue | 1, two-hour handler budget | Admission saves the request and queues the job together; the job names only the record's ID and the first delivery claims it. Stopped by shutdown or the budget, a review ends `error` and a what-if `interrupted`, keeping what they recorded; a restart ends queued and running ones the same way. Asking again is a fresh admission. |

Other handlers have a ten-minute budget. Twenty minutes is the default
queue's ceiling, and two hours the `operations`, `conformance` and `evals` queues' (`jobs.New` refuses
a kind above its queue's ceiling). River rescue starts after two hours and
ten minutes, beyond the largest handler budget. These are cancellation budgets;
code that ignores its context can outlive them. The daemon still exits within
its single five-second shutdown budget. Queue startup failure or unexpected
worker-loop exit stops the daemon, and shutdown errors are reported.

River normally refunds an attempt on shutdown cancellation. One-attempt work
instead returns a terminal cancellation with useful error text, without wrapping
`context.Canceled`. Interruption policy is persisted separately from River's
mutable attempt limit: an explicit retry must not enable free shutdown retries.
A killed process cannot report cancellation. At the next exclusive startup,
Forge settles its running jobs before reading release ownership or starting
consumers. A recorded cancellation request wins; otherwise the saved
`RetryOnInterrupt` flag chooses terminal cancellation with an explicit-rerun
reason, or availability now with one attempt refunded. Increasing `MaxAttempts`
through manual retry does not change that saved policy. River's age-based rescue
remains unchanged for ordinary operation.

Startup must establish that the old daemon has exited. A different client ID
alone is insufficient; overlapping daemons are unsupported. `apptownd` takes a
Postgres advisory lock on its database before migrating and holds it for its
life, so a second daemon on the same database refuses to start; only
`-reset-password`, which may run beside a serving forge, skips it. A lost
database connection releases the lock silently (accepted). Repair excludes the
current client, foreign job kinds and non-running rows, and fails atomically on
malformed requests or missing owner identities. Jobs' single version-pinned
mutation joins step, release and review repair in the same transaction, with one
required `jobs.interruption-settled` audit receipt per row. A repeated repair adds
nothing. River remains the schema owner; this exception adds no Forge migration.
The committed startup summary reports `river rows settled=N`.

Delivery is at least once. Queue uniqueness does not stop an old, uncooperative
worker from continuing external work after another attempt begins. Deployment
generation checks, immutable request identities, atomic receipts, adoption
markers and parent stage/retry checks remain necessary. River output is diagnostic
information, not proof of a release or workflow outcome.

## Calendar duties and visibility

River's elected leader runs these schedules (`forgeJobSchedules` in
`job_wiring.go`), each under the key `daily`, in the daemon's local timezone:

| Schedule | When | What it does |
| --- | --- | --- |
| `db-backup` | 02:00 (`0 2 * * *`) | Dumps the built-in database to `<data>/backups/forge-<date>.sql.gz` and keeps the newest 14, auditing `db.backup` or `db.backup-failed`. A day's existing dump is not replaced. With an external database (`-db-url`) it does nothing and says so; that database's backups are its operator's. |
| `backup-sweep` | 03:00 (`0 3 * * *`) | Requests a `backup` job for each standing deployment with a container, starts the daily copies, moves sets to the backup store when one is attached and clears what interrupted rehearsals left. A take-down's final set is kept with its project; nothing here prunes it. |
| `restored-copy-sweep` | every 5 minutes | Takes down restored copies whose time is up. |
| `project-deletion-pass` | every 5 minutes (and queued for a project when its deletion is asked) | Moves each project being deleted one step: one take-down, main last, or deleted and kept ([project-deletion.md](project-deletion.md)). |
| `project-purge-pass` | 04:00 (`0 4 * * *`) | Admits a `project.purge` operation for each deleted project whose kept period has ended, and removes the backup sets of projects deleted before projects were kept, 30 days after each deletion. |

Neither runs merely because the daemon restarted. Downtime may miss a tick; the
next scheduled run is the next opportunity. Ordinary workflow schedules retain
their own durable identity.

`GET /api/v1/jobs` lists open jobs first, then recent jobs. The Infrastructure
page translates River states into everyday words: queued, waiting to retry,
done, failed and stopped. Read/decoding failures are reported rather than
silently dropping records.

River owns terminal-job retention. Queue history is temporary; permanent domain
receipts stay in Forge's stores and audit log. Retrying an old completed run only
requeues follow-ups that are still retained. After retention, use the explicit
domain action (for example redeployment); missing queue history never triggers
automatic reconstruction.

## Testing and migration history

Forge tests queue admission, exact argument preservation, transactional outcome
insertion, explicit retry and interruption policy. Domain tests own stale-attempt
and duplicate-outcome checks. Daemon tests cover queue failure and bounded
shutdown. The integration tier kills a real worker subprocess, confirms its exit,
and checks both the unmodified restart gap and Forge's repair. The repair case
covers cancellation precedence, explicit retry history, same-key admission and
execution with original inputs. It does not wait for River's rescue horizon.
Run the pinned contract and crash tests on every River upgrade, including patches:

```sh
go test -race -tags=integration ./internal/forge/jobs
```

See [database startup and migration](postgres-migration.md) for River's tables.

## Implementation references

- [internal/forge/jobs/river.go](../../internal/forge/jobs/river.go)
- [internal/forge/job_wiring.go](../../internal/forge/job_wiring.go)
- [internal/forge/database_dump.go](../../internal/forge/database_dump.go)
- [internal/forge/deployment/jobs.go](../../internal/forge/deployment/jobs.go)
- [internal/forge/jobs/river_contract_test.go](../../internal/forge/jobs/river_contract_test.go)
