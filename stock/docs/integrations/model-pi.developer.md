# Model providers & slots — developer guide

## What it's for

The agents that build, review and scan your code
need a language model to think with. Providers are where models live
(a cloud API, a GPU box in the cupboard); slots are the names a workflow
uses for a *kind* of thinking — "review", "implement", "fast" — so the
instance can decide which model answers to each name without editing
every workflow.

## In workflows

Reference slots, not models: `with: { model: review }`. Slot names route
via the instance mapping for the project's profile; a slash value
(`gpu1/Qwen3.6`) is a concrete-pattern escape hatch you should rarely
need. Profiles have four basic choices: `main-strong` (Main · strong),
`main-fast`, `secondary-strong` and `secondary-fast`. Individual tasks
are the advanced level. The task slots are plan, implement, review,
security, fast, chat, worker and scout. An unset task follows its fallback:
review → secondary-strong, security → review, fast → main-fast,
scout → fast, and the others → main-strong. Quick independent checks ask for
secondary-fast. Unset secondary choices use the main choice of the same speed;
main-fast falls back to main-strong. See the [admin guide](model-pi.admin.md)
for profile lookup and task overrides.

## What you can rely on

- Step evidence shows which slot resolved to which model/provider —
  "the cop ran on the security tier" is checkable, not folklore.
- No step or chat holds a provider key: they reach models through
  AppTown's inference gateway with a token of their own, and the keys stay
  with AppTown ([What chats and steps can use](../guides/runtime-environments.md)).
- The project chat runs the `chat` slot (falling back to main-strong);
  delegates it spawns run `worker` and `scout`. Paid models are
  available there when bound.

## Choosing tiers

Cheap local models for bulk implementation; map review/security to a
paid frontier model when correctness matters more than cost. The Usage
page shows spend per workflow so the trade-off is visible.
