# Model providers & slots — admin guide

## What it's for

Every agent on this AppTown needs a language model to
think with, and you decide which ones, at what cost, for which jobs.
Providers are the backends agents can reach; slots are the routing
table — "review" goes to the second opinion, "fast" to the quick model —
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
"Configure model access" in [Setting up a server](../guides/server-setup.md).

## Slots

Integrations → Model slots. Each **model profile** has four basic choices:

| Choice | Slot | Used for |
| --- | --- | --- |
| Primary · strong | `default` | Planning, writing code, chat and delegated work |
| Primary · fast | `primary-fast` | Summaries, briefs and quick lookups |
| Secondary · strong | `secondary-strong` | A second opinion, including reviews and security reviews |
| Secondary · fast | `secondary-fast` | Quick independent checks, including guidance checks and eval judges |

Individual tasks are the advanced level. Workflows name a task with, for
example, `model: review`. Set a task's cell to override its basic choice.
The task slots are `plan`, `implement`, `review`, `security`, `fast`, `chat`,
`worker` and `scout`. The `default` slot remains the main model and the end
of every fallback chain.

Unset tasks follow this visible chain: `plan`, `implement`, `chat` and
`worker` → `default`; `fast` → `primary-fast`; `scout` → `fast`;
`review` → `secondary-strong`; `security` → `review`.
An unset `secondary-strong` uses `default`. An unset `secondary-fast` uses
`primary-fast`, which itself uses `default` when unset.

The table has a column per profile, such as `standard` or `high`.
Each project chooses its profile. For each task, AppTown first tries the
cell for that profile, then the `standard` cell. If neither is set, it
follows the task's fallback and repeats the same lookup. A task set in
`standard` therefore also takes priority over a basic choice in another
profile. A project that needs different models gets its own profile,
even if it is the only project on it. Set cells through the API
(`PUT /model-slots` with `slot`, `profile` and `pattern`). Evals compare a
profile against the source project's profile. Both arms use the source
profile's secondary fast model for judging.

Receipts record the slot, the profile that decided, the model and
provider that answered, and the pi runtime version baked into the agent
image.
