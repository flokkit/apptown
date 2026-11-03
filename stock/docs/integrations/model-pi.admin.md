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

- **Built-in pi providers** (OpenRouter, Anthropic, OpenAI): set the credential
  as a secret reference (never a pasted value); for OpenRouter, curate
  the enabled models from its live catalog — only curated models appear
  in slot selectors.
- **Custom OpenAI-compatible endpoints** (LM Studio, vLLM, llama.cpp):
  base URL, API dialect, endpoint key as a secret reference. AppTown
  GENERATES pi's config per container from the binding — including
  live-discovering what the endpoint currently serves, so swapping
  models on the box needs no binding edit.

For **OpenAI**, keep the binding name `openai`. Add your key to instance
secrets as `OPENAI_API_KEY` and keep that reference in Provider credentials.
Test connection asks OpenAI for its models and fails if the key is refused.
Browse the provider catalog to select models, or leave Enabled models empty
to offer the live list in slot selectors. The default is
`openai/gpt-5.6-luna`; slots can use `openai/gpt-5.6-terra` or
`openai/gpt-5.6-sol` when OpenAI lists them for your key. AppTown sends
requests to `https://api.openai.com/v1` using the Responses API.

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
| Main · strong | `main-strong` | Planning, writing code, chat and delegated work |
| Main · fast | `main-fast` | Summaries, briefs and quick lookups |
| Second opinion · strong | `secondary-strong` | A second opinion, including reviews and security reviews |
| Second opinion · fast | `secondary-fast` | Quick independent checks, including guidance checks and eval judges |

Individual tasks are the advanced level. Workflows name a task with, for
example, `model: review`. Set a task's cell to override its basic choice.
The task slots are `plan`, `implement`, `review`, `security`, `fast`, `chat`,
`worker` and `scout`. The `main-strong` slot remains the main model and the end
of every fallback chain.

Unset tasks follow this visible chain: `plan`, `implement`, `chat` and
`worker` → `main-strong`; `fast` → `main-fast`; `scout` → `fast`;
`review` → `secondary-strong`; `security` → `review`.
An unset `secondary-strong` uses `main-strong`. An unset `secondary-fast` uses
`main-fast`, which itself uses `main-strong` when unset.

The table has a column per profile, such as `standard` or `high`.
Each project chooses its profile. AppTown walks the requested task and its
fallbacks, ending at `main-strong`, using only that profile's cells. If none
is set, it walks the same chain using `standard`'s cells. For `standard`,
one walk is enough. A basic choice in the project's profile therefore takes
priority over a task set in `standard`. A project that needs different models
gets its own profile, even if it is the only project on it. Set cells through the API
(`PUT /model-slots` with `slot`, `profile` and `pattern`). Evals compare a
profile against the source project's profile. Both arms use the source
profile's secondary fast model for judging.

Receipts record the slot, the profile that decided, the model and
provider that answered, and the pi runtime version baked into the agent
image.

The basic choices' names from before RC14 (listed in its release notes) fail
at resolution and cannot be saved as slots. An unset custom name, including a typo, falls back to
`main-strong`. Stored workflow model requests must use the current names; edit
them by hand when changing model choices.
