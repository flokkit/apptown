# Model providers & slots — admin guide

## What it's for

Every agent on this AppTown needs a language model to
think with, and you decide which ones, at what cost, for which jobs.
Providers are the backends agents can reach; slots are the routing
table — "review" goes to the careful model, "cheap" to the fast one —
changed here, once, for every workflow at once.

Two pieces: PROVIDERS (inference backends agents can reach) and SLOTS
(the routing table workflows reference).

## Providers

Integrations → Model providers. Two kinds:

- **Built-in pi providers** (OpenRouter, Anthropic): set the credential
  as a secret reference (never a pasted value); for OpenRouter, curate
  the enabled models from its live catalog — only curated models appear
  in slot selectors.
- **Custom OpenAI-compatible endpoints** (LM Studio, vLLM, llama.cpp):
  base URL, API dialect, endpoint key as a secret reference. AppTown
  GENERATES pi's config per container from the binding — including
  live-discovering what the endpoint currently serves, so swapping
  models on the box needs no binding edit.

Provider credentials stay on AppTown. Pi and its delegates use their AppTown
session token through the private inference proxy. The curated model list is
also an admission allowlist; when using fuzzy `~` selectors, curate concrete
model IDs. Model agents run in containers. Without a worker they run on AppTown's own Docker. With a worker, agent and review steps and most change
chats run there, while project chats and chats that continue a step's agent
stay on AppTown; with `-no-local-docker` AppTown runs none and refuses
what the worker cannot take. They
also need AppTown's private inference listener; setting it up is under
"Configure model access" in [Setting up a server](/docs/guides/server-setup).

## Slots

Integrations → Model slots. Workflows say `model: review` (a slot
name); the slot maps to a concrete pattern. Stock slots: default, plan,
implement, review, security, fast, chat (the project chat), worker and
scout (a helper's delegates).
An unmapped slot falls back along a visible chain — security → review,
scout → fast, everything else → default — and every chain
ends at default.

The table has a column per **profile**: `standard` and `high` ship, and
each project declares which it wants (its demand level). Resolution
walks, most specific first: a per-project override for that slot, the
instance cell for the project's profile, the `standard` cell, then the
slot's fallback. Map `review` to a paid model and every reviewer in
every workflow upgrades in one edit. Per-project overrides ("this
project's reviews run on the good model") are listed on the slots page
and set through the API (`PUT /model-slots` with a `project`).

Receipts record the slot, the profile that decided, the model and
provider that answered, and the pi runtime version baked into the agent
image.
