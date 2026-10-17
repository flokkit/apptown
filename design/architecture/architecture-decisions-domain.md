# Architecture decision records

`internal/adrs` owns the ADR table, its state type, lifecycle decisions, private
queries and required receipts. Forge supplies one authority port and composes
post-commit Work assertion refresh. The package imports database/audit infrastructure
and never imports Forge. No record, query or command crosses the boundary as an
untyped map, SQL tail or generated database row.

Public operations create a draft, edit selected fields, propose a successor, read
a detail, list/filter records, summarize a Change or count selected projects.
They require explicit caller authority. The port receives the operation and
project scope and returns the verified receipt actor. Forge preserves capability,
account, OAuth/instrument scope and explicit unseeded-operator behavior. Minimal
project-ID resolution exists for routing authorization; it does not return content.
The wider Access kernel remains outside this domain.

Draft/proposed/rejected decisions allow content edits. Accepted, deprecated and
superseded content is immutable. Tags and the linked Change retain their existing
editable semantics. Commands request redraft, propose, accept, reject or deprecate;
only acceptance of a successor can retire a predecessor. Superseded is terminal.
A deprecated successor can be accepted again without retiring its predecessor a
second time. Creation validates title and same-project Change links.

Every mutation takes the project lock first. Number allocation and insertion share
that transaction. Edits merge requested fields into a freshly locked row and apply
the owned lifecycle rule; a detached snapshot never authorizes editing content.
Successor acceptance and predecessor retirement use the same guarded operation and
commit with the acceptance receipt. Competing successors have one winner. Missing
or suppressed writes and failed/deferred receipts roll the operation back. There
is no recovery journal or automatic replay.

Detail reads keep the decision, its successor and lifecycle options in one read-only
repeatable-read snapshot. An accepted successor takes precedence over still-draft
proposals in the link shown for the old record. The UI consumes these lifecycle
options and no longer duplicates a transition table or content-lock predicates.
Options are current eligibility observations; commands still check authority and
fresh state. Display labels/styles remain client presentation. The binary and its
embedded UI ship together; no legacy-options fallback is introduced.

Two named composition operations cover existing cross-domain work:

- `MatchingIn` returns typed, exact-tag matches under Work's project lock and its
  committing assertion transaction. Matching rows stay share-locked through the
  Work result and receipt. Ordinary assertion SQL no longer reaches ADR storage.
- `RemoveProjectIn` owns the ADR part of project deletion and verifies that no
  scoped records remain before commit. Project authorization and the wider deletion
  operation remain composition concerns. Whole-project deletion still has older
  multi-step/filesystem behavior outside this slice; it is not newly claimed atomic.

Strict decoding adds no migration or runtime repair. Before rollout, inspect any
historical rows with an unknown state, missing/foreign Change link or predecessor,
or multiple accepted successors. Preserve historical content when correcting such
records. The new commands refuse invalid links/retirement instead of guessing an
intended history. No production data has been changed by this extraction.
