# Change lifecycle

> Historical lifecycle design and implementation notes. For current ownership and
> commands start with [Workflow](../subsystems/workflow-domain.md). The dated diagrams and
> App/store names below are not a second current API. Accepted product intent
> is preserved; differences require explicit review.

Work owns a Change's lifecycle through `ChangeState` and one private
`transitionChangeIn` transaction participant. Work remains inside Forge while
its command/query boundaries are canonicalized. This is not a model-only package
or a completed Work extraction.

A Change is proposed, in progress, landing, shipped or abandoned. Its wire words
remain unchanged. Stored and incoming state decoding is strict; zero represents
no valid state. Missing observations are tracked separately. Queries, Change
projections, remediation facts and cleanup predicates use the typed state.
Workflow roles, derived activity and ADR states are different concepts.

Commands name acts: start, attach work, land, resume work, cancel landing, ship or
abandon. Start accepts a proposed or already in-progress Change. Active Changes
can attach work, land or resume; shipping and abandonment are terminal. Cancelling
a landing run moves a landing Change back to in progress and otherwise preserves
its state, including closure. A proposed or closed Change cannot newly ship.
Repeated completion still uses Work's existing run guard and explicit follow-up
retry path; it does not rewrite an already-shipped Change.

The participant reads and locks the current Change in the requested project and
checks the model's decision. Run attachment requires root work belonging to that
same Change and project. State, current run association, branch naming when
starting, and the terminal timestamp use one conditional update. A suppressed
write is a conflict, not success. Every caller takes the project lock first and
keeps its required Work receipts in the same transaction. The participant does
not perform external effects, authorize arbitrary callers or commit independently.

Run creation, send-back, cancellation, abandonment and shipping retain their
existing atomic operations. Start now commits promotion, any branch name and its
required audit together; workspace preparation follows commit. It reads repository
eligibility from the locked project. Failed workspace preparation is retried by
an explicit start, while an unrecorded start produces no workspace effect.

Human description edits use a typed private command. Selected title, brief and
external reference fields, the title ownership pin, required audit and readback
commit together under the project-first lock. Omitted fields survive concurrent
edits; blank titles remain ignored and brief whitespace remains exact. Historical
Changes remain editable without a lifecycle transition. Foreign or moved scope,
suppressed writes and failed receipts are refused, and request cancellation releases
lock waits. The existing HTTP route still supplies authorization.

Automatic narrative source/publication now has a separate typed, checked owner;
see [narrative observations](change-narrative.md). Human, first-message and generated
titles share [one title ownership model](change-titles.md).

Ordinary [creation and attribution](change-creation.md) now share a typed command
and required initiator receipt. Creation is initialization, not a lifecycle
transition. Specialized import/adoption creation, source ownership and larger
Change read consolidation and the Work operation-authority
boundary remain separate work. Existing HTTP/MCP guards remain in composition;
the private participant is not an unguarded public API for a future transport.
