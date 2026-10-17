# Change narrative observations

Work owns the typed narrative source and publication. The source query reads the
Change, repository configuration, owned root Runs and newest two eligible
report/plan artifacts in one readonly snapshot. Child-run reports remain eligible;
foreign-project rows and scan reports do not. Required read failures return no
partial source. Run state uses Work's strict state type.

The builder has three bound effects: model-slot resolution, live conversation/Git
evidence, and model completion. It has no App dependency. Forge binds those effects;
no SQL transaction remains open during them. Chat evidence retains the last two
substantial helper reports per matching session. Committed Git comparisons read
the change workspace directly, falling back to the project repository only when
the workspace is absent. Broken or unreadable workspaces remain errors; neither
narrative collection nor the detail/diff GET routes publish refs. A recorded base preserves the historical diff after
landing. Git uses the existing bounded process runner, with cancellation and a
one-minute command limit; the model retains its one-minute HTTP timeout. The
builder supplies a two-minute context for source reads, effects and publication.
Model-slot/secret lookup and session mutexes still use their existing adapters;
this slice does not claim their complete context propagation or extract Sessions.

Publication takes the project lock and rechecks repository configuration, Change
scope, body, lifecycle, attached run, branch, base and the previously observed
summary. A different completed summary or changed source is a conflict requiring
explicit refresh. These are observation checks, not a durable generation protocol:
new artifacts, Git commits or chat messages can arrive during a model call, and
ABA changes are not detected. The narrative remains replaceable advisory prose,
not evidence used to authorize shipping or recover workflow execution.

A generated title applies only while generated-owned and still equal to its observed
words. Pinning even the same words is a human ownership decision. Title-only or
empty model output never changes a title. Stored notes are bounded to 2,000 UTF-8
bytes without splitting a code point; accepted title limits retain their existing
90-byte bound.

Title/summary, eligible empty notes on same-project linked releases, and the
required audit commit together. Delivery owns the named release-note transaction
participant, preserves nonempty notes and checks complete affected-row counts.
This retains the existing linked-release behavior, including releases with more
than one Change; it does not invent a combined narrative for them. Suppression,
read/write/receipt failures and deferred commit rejection roll back publication.

Model usage is already incurred before publication. It is recorded independently
even for empty output, conflict or cancellation. Existing best-effort accounting
now has a five-second detached write limit; failed spend writes remain logged,
not durably retried. This is not a complete accounting-domain extraction.

Explicit HTTP refresh returns failure instead of claiming success with an old
summary. Job and background invocations log failure and retain their best-effort
policy; a summary failure does not replay jobs or undo accepted Work. Long-lived
chat sessions and historical application contracts are unchanged. The first-chat
naming now uses [explicit title ownership](change-titles.md), with no prefix heuristic.

Background console and branch-import refreshes share one narrative owner created
in `New`. It closes admission before cancellation and draining, under the same
shutdown deadline as workflow and remediation. Request context values survive
background admission; request cancellation does not end admitted work. Explicit
HTTP refresh and queue follow-ups remain synchronous and belong to their callers.
Tests use the same App shutdown path before removing temporary resources. A drain
timeout reports unfinished work; it cannot force a goroutine to stop or end a
long-lived console container.
