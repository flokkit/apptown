# SCM operations and Work authority

`internal/scm` owns a run's merge operation. Its supported API is `Merge`, `Read`,
`Inspect` and `InspectPending`; its store and transition methods are private.
The package imports neither AppTown, HTTP nor generated Work rows. Commands and
results exchange typed values, with no untyped maps.

## State and receipts

A merge record is idle (no row), pending (an admitted local operation with an
expected target commit), or recorded (that operation's proven result). One state
model validates every transition. A new operation can follow a recorded result
when there is new work; an existing operation identity cannot describe a new
merge. A retry cannot replace a pending operation.

Local execution begins only after its intent commits. Settlement writes the
result, the root Change's selected source and the required Work event in one
transaction. Each write must affect its expected row. Repeating the same proven
settlement is idempotent; altered operations, sources and results are rejected.
External Git/provider execution never holds a SQL connection.

An explicit inspection records an exact operation trailer reachable from the
configured default branch, cancels an operation whose target is proven unchanged,
or retains an unresolved operation with a required audit. It never replays work
or resumes a workflow. Fresh inspection can record an old effect after Work ends;
a stale prepared Work snapshot cannot settle it or reopen the run.

Merge admission and SCM assertions resolve the repository, branch, adoption and
merge request facts in one database observation and pass them to the adapter by
value; dynamic merge gates compare them again before committing an outcome.
Missing or unreadable facts are errors: they never grant permission by
pretending a run has no repository or merge request. A malformed recorded merge
request blocks merging; it never selects a local merge instead.

A provider merge uses the previously recorded MR identity and exact published
source SHA. The adapter verifies source/target branches and the provider's actual
merge, squash or fast-forward result, then proves it on the fetched default
branch. It cannot substitute another MR or fall back to a local merge. Provider
protocol words such as `opened` and `merged` are adapter representations, separate
from SCM's state type.

Work also hands SCM an immutable attribution with each admitted merge: who
opened the change and what admitted the merge, read in the admission
transaction and compared again at check-in. SCM renders it into the merge
commit's trailers and, before a provider merge, into the merge request's
description; a refused description write stops the attempt before the merge.
It is a public copy of part of Work's record, never an input to a gate (see
the [SCM package guide](../../internal/apptown/scm/README.md#what-gitlab-is-told)).

## Collaborators

Work supplies an operation-scoped immutable `Claim`, minted from an admitted
advancement or SCM assertion. The claim keeps the full Run, Step and Work source
snapshot private. Its three methods are `Source`, `CheckIn` and `RecordIn`.
`CheckIn` takes the shared project/run locks, compares the exact snapshot, and
checks current gates, project rules and uplift revision before a new effect.
`RecordIn` participates in SCM's transaction to write Work's source and receipt.
These are named transaction participants, not general SQL callbacks.

The fixed Work port resolves claims and separately authorizes startup inspection.
An actor label is attribution, not permission. An inspection scope cannot admit
new effects; a claim for one run cannot list every pending operation. The wider
Work command/query API still needs its own complete authority boundary before
Work can be extracted. Existing transport authorization remains in force.

The three-operation `Driver` port prepares external facts, executes admitted
work, and inspects an old operation. AppTown Git/GitLab adapter receives a
process runner and one provider-client factory. It receives no App or database.
Provider credentials are frozen for one operation, and the askpass helper is
published atomically for concurrent project operations.

AppTown assembles the participants. After successful settlement it retains the
existing best-effort contract snapshot and remote mirror behavior; these do not
turn a failed required receipt into success.

## Repository serialization and cancellation

An injected `RepositoryLocks` value is shared by merge, adoption and drift
resolution in one AppTown instance. Writers of one project serialize; unrelated
projects can proceed concurrently. Waiting honors cancellation and idle lock
entries are removed. There is no process-global merge mutex.

Git commands in this adapter use the existing execution runner, including process
group cancellation, bounded output and a ten-minute command timeout. Provider
requests use the operation context. This does not impose a lifetime on chats or
change the bounded application shutdown policy. Other Git call sites
do not go through this adapter and do not have its guarantees.

Repository drift has one checked reader, shared by merging, the project view and
drift resolution. NUL-delimited status preserves exact paths and rename pairs;
an unavailable Git read is an error, not a clean tree.

## Storage and upgrade

SCM alone accesses `run_merges` in ordinary application code. Schema declarations,
one-time migrations and explicit database-fault injection tests are representation
or validation sites, not alternative state owners. There is no compatibility
reader or recovery framework.
