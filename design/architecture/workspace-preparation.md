# Checked Work workspace preparation

`internal/workflow` owns workspace source observations and preparation. AppTown
supplies a database, Git execution value and the same repository-lock value used
by SCM. There is no App dependency, global fake, replay job or workspace conversion.
Consumers use thin adapters to the owned source record and private sqlc
query; generated rows never cross the package boundary.

A Change operation reads current project/Change scope, branch, base, selected
source, state and associated Run. A Run operation takes one database snapshot of
its checked source/ancestry, current state/stage/retry revision and Change facts.
Only root Runs prepare a workspace, under the depth and continuation rules,
per-Change, per-Run and project paths and current workflow identity.
The snapshot transaction ends before Git runs, including with one database connection.

Preparation acquires the injected repository lock before SQL locks. Waiting and
Git receive the caller's context, with a ten-minute operation bound. Git commands
also require a positive timeout, bounded output and process-group cancellation
through `execution.Runner`. This does not change the application shutdown policy or
make long-lived chats expire. The Run starter supplies a background context, not
the request's.

A new standalone clone is checked out from the selected branch commit, or the
project's declared default branch when a new branch is being created. The hub's
current checkout is not a substitute for that default. Clone objects are independent
copies, the host remote is removed and the helper identity is configured. A temporary
sibling directory is renamed into place only after checkout, configuration and base
read succeed. Failure removes only that operation's staging directory. No existing
user directory or pointer-style worktree is reset, deleted or converted.

An existing standalone clone must have its expected checked-out branch and a
readable commit. Its human edits and Git metadata are preserved. A missing branch
with a recorded base requires inspection; preparation does not invent
replacement history. A workspace-owned branch can be used even if the hub lacks
its current ref. Imported merge-bases and recorded Run bases are never
overwritten because a workspace was recreated.

After Git, publication locks the project and the scoped Run/Change rows, rechecks
the prepared facts and verifies affected-row counts. Both eager Change preparation
and Run preparation use the same Change-base participant. Run base, any initial
Change branch/base and a required `workspace.prepared` receipt commit together.
The receipt records branch, observed tip and whether this call created the clone;
it also covers an explicit retry that observes a clone left by a failed transaction.
Preparation never claims it created a clone that already existed, and never infers
a missing earlier receipt.

A failed transaction can leave a complete standalone clone while leaving all durable
bases/receipts unchanged. The error is explicit; retrying preparation uses the same
Change/Run identity. Source conflicts require inspection. There is no automatic
cleanup/recreation loop. SQL guards fence database facts and the lock serializes
cooperating AppTown writers; neither is a new fencing protocol for arbitrary external
Git or filesystem writers.

Canonical attachments are copied after durable publication. Missing attachments are
normal; copy/read errors are reported by preparation. A failure at that point can
leave a committed base and partial attachment copies, and explicit preparation
copies again from the canonical directory. An upload keeps its canonical file and
logs a synchronization failure.
