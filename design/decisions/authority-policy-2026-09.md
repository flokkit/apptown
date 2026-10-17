# Shared authority policy, September 2026

The authority-kernel plan was approved for complete local execution on 14
September. The final root/profile ruling supersedes earlier promises to preserve
team restrictions on root. This inventory separates intentional corrections from
mechanical adapter removal. No production rollout is part of this execution.

| rule | earlier implementations | settled behavior |
|---|---|---|
| Administrator | account role in account/token administration; grants in generic checks; partial identity administrator accepted by configuration | Only current instance-administrator grants confer root. Account role in responses is derived. Identity and grant administration require root. |
| Root's project reach | household special case in the evaluator and roster; extra project ownership seeds; team restricted inheritance | Root reaches every project through one grant rule, after credential limits. Remove synthetic household ownership grants. |
| Approval eligibility | account role plus profile decides whether approver expressions and author segregation apply to root | Root is exempt from eligibility and self-approval refusal. Named approvers still constrain non-root; author segregation becomes per-project. Quorum stays with Workflow. |
| Credentials | token administration checks a personal token's admin ceiling; OAuth and instruments narrow generic permissions | A token cannot exercise root unless its ceiling permits it and its owner still holds root. OAuth/instrument bounds run before root. Long-lived callers re-read grants. |
| Bootstrap | admin count opens unrelated account, Work, conformance, ADR and adoption commands | Initial setup creates authority explicitly. No empty administrator set grants ordinary permission or turns an absent caller into system work. |
| Missing caller | Work/conformance/ADR caller errors, Access principal error; thin adapters sometimes pass an empty principal | Missing identity/provenance is an authentication failure. Explicit anonymous callers can use explicit read grants. No silent system fallback. |
| Unverified caller | Work and procedures track verification; others use presence of a principal | One caller carries provenance separately from name and receipt. An unverified caller cannot invoke ordinary commands. Configured open-mode transport provenance is distinct from an arbitrary internal name. |
| Console | several adapters substitute the person before disabled-account checks; generic Access also checks the console owner | The shared checked authority observes the console owner's current disabled status and grants. Instrument limits remain independent. |
| Instrument scope | Work rejects missing/cross-project scope; conformance rejects fleet/cross-project access; generic evaluator bounds nonzero project IDs | Preserve bounded instruments and explicit admitted system work. Domain-specific run/remediation/console bindings must survive the extraction; resource completion must retain their IDs. |
| Receipt actor | procedure commands compare a supplied actor string against verified receipt attribution; Work carries a receipt string separately | One Caller carries identity, verification and receipt separately. Existing actor-taking commands retain the equality check. Attribution never grants authority. |
| Fact/read failure | checked Access returns errors; ADR uses Evaluate, which logs failures and can answer from incomplete facts | ADR moves deliberately to Require: failed policy reads propagate as operational errors, rather than authorization answers. Required receipts remain required. |
| Exception creation | request capability immediately saves an effective accepted-risk/false-positive decision | Require exception:approve in both service and route. No fictitious pending request stage. |

## Validation and compatibility boundaries

No compatibility reader will reconstruct removed grant conditions, role versions,
account flags or profiles. Migrations preserve historical receipts and archive
retired data atomically; unexpected live policy requires explicit conversion.
The local production-copy rehearsals run only schema migration and inspection.

Denial code changes must be recorded per affected operation. Exception creation
now returns missing_capability, or outside_scope when approval is held elsewhere,
with HTTP 403. Root's expanded approval/project reach is an explicit allow change.
Other existing domain eligibility errors must retain their meaning during moves.
Missing-caller HTTP translation and mixed-refusal precedence will be verified
against each migrated transport rather than assumed from a kernel unit test.

Operation tests must prove refusal precedes record writes, run admission or
container starts. Shared identity-policy tests cannot replace those checks.

## Extraction result and refusal ordering

The shared `access.Checked` matrix (`TestCheckedPolicyAcrossSetupStates`) exercises
verified/unverified callers, project and anonymous grants, missing capability,
OAuth narrowing, run limits, console ownership and root derived from grants both
before and after setup. Existing account/credential/root tests cover expiry,
revocation and resulting last-root protection. Caller scopes are copied on both
sides of the context boundary. No decision cache or credential lifetime was added.

The common kernel is not a universal eligibility order. Work keeps disabled-account
and approver eligibility checks before its approval capability; Conformance keeps
its instrument scope check before the human capability path. Adoption retains the
receipt-actor equality check. ADR's routing lookup verifies provenance and account
status without pretending that scope discovery is a record read permission.
`RequireEnabledIn` centralizes the account preflight without granting permission.

General denial is access.Denied. Work and ADR missing caller remain HTTP 401;
Conformance caller_required remains HTTP 403. Workflow's DecisionDenied remains
its separate eligibility result. ADR policy read failure deliberately becomes 500.
Project resource completion now requires an actual containing project: direct
internal ADR/conformance/adoption calls with nonexistent project IDs fail the
lookup instead of using an empty owner group. Existing slug-based transport doors
already resolve the project first. Missing-resource translations remain 404 where
those doors map sql.ErrNoRows; this does not create a new permission denial code.

The four complex App authority adapters are gone. Run read binding belongs to
Workflow, remediation observation freshness belongs to Remediation, permitted
review operations to Conformance, and preview/protected-release rules to Deployment.
Their narrow scope ports grant no general human authority and do not expire
already-admitted work. The preview source adapter retains App only to translate
Work records and Console labels; it makes no eligibility or capability decisions.
