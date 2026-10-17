# Interrupted work: prefer explicit reruns

Decision: 10 September 2026, confirmed by the operator during workflow cleanup.
Default to simpler code and explicit reruns instead of making interruptions
transparent. Automatic recovery must justify its complexity with a concrete
user benefit. This supersedes roadmap assumptions that every kind of unfinished
work should resume automatically.

## Policy

- Builds, scans, tests and generated summaries can be interrupted and rerun.
  Record that they stopped; discard their temporary resources. Do not build
  reconstruction or automatic replay machinery merely to save repeatable work.
- Preserve saved conversations, source edits and completed evidence. A rerun
  should not destroy useful work or silently reuse a stale pass.
- After a workflow interruption, make an explicit retry or fresh run the normal
  next action. Retrying must still respect current gates and authority.
- Git merges and provider deployments can have external effects. If their result
  is uncertain, say so and require inspection before repeating the operation.
  Prefer an explicit unresolved state over a complicated universal reconciler.
- Keep the small correctness core: atomic writes for outcomes and required
  receipts, stale-attempt/generation checks, and bounded shutdown. Simpler retry
  behavior does not make missing checks into passes or partial writes acceptable.
- Keep chats durable, with runtimes created on demand. A conversation's lifetime
  does not need to match a process or container's lifetime.

## Refinement: library-managed retries

Later on 10 September the operator approved evaluating River OSS to replace
Forge's custom queue. The preference is against bespoke reconstruction code,
not against every automatic retry. Safe repeatable operations may use bounded
library-managed retries, interrupted-job rescue and scheduling. Expensive model
work still defaults to explicit reruns; external uncertainty still requires
domain authority before repeating an effect. River does not resume a suspended
workflow or remove the need for atomic domain outcomes and generation fences.

The River adoption checks and migration plan
record the implementation boundary and per-operation policy. They establish that
a one-attempt limit alone does not prevent River from requeueing during graceful
shutdown; explicit-rerun operations need terminal interruption handling as well.
The probe does not change production behavior.

## Application to the current gate slice

A stage records its expected scanner instances at creation. This is evidence of
what must pass, not a mechanism to recover execution. Missing checks block even
without a restart. An older expanded-scanner stage lacking that evidence should
be rerun; do not backfill inferred expectations from current attachments or guess
that surviving records are complete. No new automatic replay is needed.

## Next deletion slice

Inventory automatic workflow restart paths, starting with orphaned automatic
steps and dispatch recovery. Replace their automatic execution with a clearly
reported interrupted state and an existing explicit retry/fresh-run action.
Preserve active child-call ownership, completion receipts and saved chats while
changing that behavior. Review which completion follow-ups should remain automatic
as part of the normal user-requested workflow, rather than removing them blindly.

Keep this separate from changing provider reconciliation. Provider observation
can be useful, but should not become an excuse to rebuild a general recovery
framework. Inventory concrete callers before deleting shared machinery.

Status: policy accepted; the gate slice does not change existing automatic
restart behavior. Queue replacement is now the next implementation direction,
with deletion of custom replay paths guided by the refinement above.
