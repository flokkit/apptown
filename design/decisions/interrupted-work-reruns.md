# Interrupted work: prefer explicit reruns

Forge prefers simple code and explicit reruns to making interruptions
transparent. Automatic recovery must justify its complexity with a concrete
benefit to the person using it.

## Policy

- Builds, scans, tests and generated summaries can be interrupted and rerun.
  Record that they stopped and discard their temporary resources. Do not build
  reconstruction or replay machinery to save repeatable work.
- Preserve saved conversations, source edits and completed evidence. A rerun
  must not destroy useful work or silently reuse a stale pass.
- After a workflow interruption, an explicit retry or a fresh run is the normal
  next action, and it respects current gates and authority.
- Git merges and provider deployments have external effects. When their result
  is uncertain, say so and require inspection before repeating them. An
  explicit unresolved state is better than a universal reconciler.
- Keep the small correctness core: atomic writes for outcomes and required
  receipts, stale-attempt and generation checks, and bounded shutdown. Simpler
  retries never turn a missing check into a pass or make a partial write
  acceptable.
- Chats are durable and their runtimes are created on demand; a conversation
  does not live and die with a process or container.

## Library-managed retries

The objection is to bespoke reconstruction code, not to every automatic retry.
Background jobs run on River ([jobs](../subsystems/jobs.md)): safe, repeatable
operations may use its bounded retries, interrupted-job rescue and scheduling.
Expensive model work still defaults to an explicit rerun, and an uncertain
external effect still needs its owner's decision before it is repeated. River
does not resume a suspended workflow and does not replace atomic domain
outcomes or generation fences. A one-attempt limit alone does not stop River
requeueing a job during graceful shutdown, so an operation that must not be
rerun automatically also handles interruption as a terminal outcome.

## Evidence, not recovery

A stage records the checks it expects when it is entered. That is evidence of
what must pass, not a recovery mechanism: a missing check blocks the stage with
or without a restart, and expectations are never backfilled from current
attachments.
