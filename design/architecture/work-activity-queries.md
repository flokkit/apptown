# Work activity queries

`workflowQueries` owns run-list selection and current activity. Its list input has
named project, lifecycle state, root-only and limit fields; callers cannot provide
SQL fragments. The typed summary carries `RunState` and the same derived activity
record used by run detail and console polling. Overview and API adapters retain
their existing JSON field names. Lists order by update time, then ID for stable ties.

A list uses one summary SELECT and one activity SELECT for all running rows. Empty
or wholly terminal lists need only the summary read. These are two domain reads,
not two total protocol round trips: the list also opens and closes a repeatable-read,
read-only transaction. That snapshot prevents a completion between reads from
combining an old running summary with a newer completed stage. A 64-run active list
previously required 129 SELECTs before any nested calls; it now requires two.

Activity loads current stages and actual child relationships in one recursive SQL
query. `UNION` visits each stored run once, including malformed cycles. Recursion
follows only a running call in the parent's current stage, matching its attempt and
project. The private model also verifies the evidence-selected child against those
relationships: another root may be present in the same batch without becoming this
call's child. A visited path bounds model recursion. Invalid, stale, foreign or cyclic
child references return an error rather than another run's private activity.

The model owns the derived vocabulary and its priority: running work, queued entry
work (including slot waits), failed gates, pending human approval, then readiness.
A pending review counts as queued only when the runtime reports a slot wait. A call
whose child has not yet been recorded still reports working; a child that has
completed awaits ordinary parent settlement. This creates no new recovery work.

The only runtime collaborator is a per-step slot-queue observation. It performs no
SQL or Work mutation. Request cancellation reaches the activity query. Required
read failures propagate, including through console polling; missing historical
change IDs in a console remain intentionally omitted. No session lifetime, checkout,
execution, approval or transition behavior changes.

The larger Work domain remains inside Forge. These are canonical query/model
owners, not a claim of a completed package extraction. Run detail and trace now use [the coherent evidence snapshot](work-evidence-snapshots.md),
including their event and artifact projections. The existing MCP catalog has no
request context in its call value, so its adapter still supplies a background
context; a general MCP context-lifetime change is separate from these query refactors.
