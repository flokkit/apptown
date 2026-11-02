# Work activity queries

`workflowQueries` owns run-list selection and current activity. Its list input has
named project, lifecycle state, root-only and limit fields; callers cannot provide
SQL fragments. The typed summary carries `RunState` and the same derived activity
record used by run detail and console polling. Lists order by update time, then ID for stable ties.

A list uses one summary SELECT and one activity SELECT for all running rows. Empty
or wholly terminal lists need only the summary read. These are two domain reads,
not two total protocol round trips: the list also opens and closes a repeatable-read,
read-only transaction. That snapshot prevents a completion between reads from
combining an old running summary with a newer completed stage. However many runs
are active, a list takes two reads, not one per run.

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
whose child has not yet been recorded reports working; a child that has
completed awaits ordinary parent settlement. This creates no new recovery work.

The only runtime collaborator is a per-step slot-queue observation. It performs no
SQL or Work mutation. Request cancellation reaches the activity query. Required
read failures propagate, including through console polling; a console with no
change ID omits it. Run detail and trace use
[the coherent evidence snapshot](work-evidence-snapshots.md), including their
event and artifact projections. The MCP catalog has no request context in its
call value, so its adapter supplies a background context.

Home's activity response also includes `open` (at most 20 changes) and
`openTotal`. `workflow.Service.OpenChanges` selects unclosed changes whose
recorded requester principal is the reader's principal, within projects they
can currently read. Approving or viewing another person's change does not add
it. Selection orders by the latest run activity or change creation time, then
change ID. Changes with no active run remain visible as idle, with `idleSince`.
The selection and run activity use one read snapshot; read failures fail the
response rather than appearing as no open work.

Each row has one state and a reason supplied by Work: running (including queued
work), waiting on you, waiting on someone else, or idle. The existing run
classifier supplies the step or failed check. Approval authority determines
whether the reader may decide; a reader who already voted waits on other
approvers. Reasons retain the declared people, groups or roles, using project
default approvers when the step declares none. These observations grant no
permission to act.
