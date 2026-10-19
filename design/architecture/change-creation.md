# Change creation and initiator attribution

Work admission in `internal/workflow` accepts one typed `ChangeDraft`: project, descriptive
fields, opening/backlog intent, optional first-chat naming and typed pinned
acceptance. It does not accept an arbitrary lifecycle or title ownership state.
HTTP and MCP capability guards live in composition; worker creation is
explicitly system-owned. This is not a public, independently authorized Work API.

The creation participant reads current repository configuration under the project
lock. Initial Change fields, title ownership, branch naming when appropriate,
required `change.created` receipt and checked readback commit together. The receipt
identifies the initial Change and its state; later Run admission records its own
operation. It does not depend on a later best-effort audit write by a transport.

Initiator attribution matters to approval policy. HTTP records its verified actor;
MCP retains the person's instrument provenance. Catalog, service and integration
operations and manual remediation retain their initiating person. Automated
remediation records `forge-system`, not the worker's output or an invented human.
Initiator queries read the first creation receipt.

The small creator value has one workspace effect. It invokes that effect only after
creation and attribution commit; backlog and repository-free Changes need no such
effect. A preparation failure returns the same committed Change and an error naming
its ID. The caller can explicitly start that Change again. A post-preparation read
failure likewise retains its committed identity. There is no automatic recreation,
replay queue or hidden recovery loop. The [checked workspace operation](workspace-preparation.md) owns Git, guarded base
publication and attachment synchronization, outside the creation transaction.

Atomic remediation joins the same creation participant in its existing transaction,
so its Change/attribution, run, attempt and item records commit together. The
participant never prepares a workspace or commits independently. Typed acceptance
stays a record through both manual and worker creation, never an encoded object
handed between owners. Serialized storage retains exact integer values.

The same insertion participant covers branch imports and adoption/uplifts.
Import admission checks fully qualified branch refs, records the exact
merge-base and reconstructed provenance, then commits both creation and import
receipts before workspace preparation. Git uses bounded execution outside SQL and
injected repository serialization; the transaction rechecks current project facts.
An external writer that bypasses that serialization is not given a new Git fencing
protocol. Preparation failure retains the committed identity for explicit retry.

Uplift admission locks the project, checks the prepared adoption revision and
refuses another open uplift. Its typed acceptance, Change, adoption-owned target,
negative target revision, target link and both required receipts commit together.
The adoption adapter owns candidate/receipt serialization; Work treats the candidate
as an opaque adoption document and checks the returned target's project, revision
and digest. Uplift admission adds no workspace or generated branch.

Change record and state, legal lifecycle acts, title ownership and decisions, the
point read, creation, human edit and title participants are in
`internal/workflow`, which never imports Forge; Forge has thin assembly adapters.
The admission entries keep their composition guards and named
transaction-participant contract; they are not an independently authorized
public service.
