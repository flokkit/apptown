# Work application commands

`Preparation.Resolve` turns Workflow's checked `StartSource` into a frozen
`PreparedRun`. It resolves the recorded adoption, selected workflow and guidance,
checks current policy, and records integration/adoption receipts. Root landing
starts first commit pending workspace edits with their existing console provenance.
That Git effect precedes the admission transaction; it is not an atomic database
operation.

Workflow owns source validation, replay, run admission, parent linking and dispatch
after commit. `workflowexec.Calls` supplies interpolated inputs through
`Execution.Call`, so child starts share exactly the same preparation and admission.
Remediation uses the prepared adoption ID for its acceptance checks.

The source interface names three operations of the Adoption collaborator; it
carries neither App nor the dispatcher. Tests substituting workflow definitions
still run real preparation, receipt resolution and admission. Persistence rollback,
parent-link and landing integration checks remain at those actual boundaries.

`LandingEffects` supplies SCM's per-operation driver and observes its results:
drift audit, contract snapshot and branch publication. It does not own merge
admission or settlement. `Uplift.OpenUplift` translates an Adoption candidate into
Workflow's checked Change admission. Adoption supplies its target recorder at
invocation; Workflow keeps all target, Change and receipt writes in one transaction.

`RemediationStarts.Resolve` selects an eligible lane, invokes shared preparation,
and builds acceptance from that exact adopted receipt. Remediation supplies its
verification observation when invoking the operation. `verification.Fleet` owns
the post-landing review and reads remediation attribution through its owner query;
it no longer needs the remediation service merely for that fact. Busy checking
receives only the conversation registry. Attempt lifecycle and its transaction
remain in Remediation.

`FollowUps.Complete` and `FollowUps.Abandon` are the durable queue handlers. They
read current recorded scope before invoking `AcceptedLandings`, `UpliftSettlement`,
`Resources`, or narrative refresh. These collaborators own distinct operations;
none retains App. Workflow's transaction participants still admit the jobs, River
still owns delivery, and keys, receipts and per-effect retry budgets are unchanged.
The startup adoption hook calls the same target settlement operation.

`Resources` cleans up an explicitly completed run or abandoned Change. It retains
the existing best-effort Docker behavior; workspace removal errors still fail the
job. Periodic maintenance and authorized project deletion have different policies
and are separate owners. These moves do not claim new cancellation guarantees for
legacy Docker calls or change the daemon's bounded shutdown.
