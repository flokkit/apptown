# AI models (OpenRouter) — admin guide

## What it's for

People want to put AI features in the apps they build here: a summary, a
draft reply, a classifier. Each app then needs a model key. Pasting one
shared key into every app gives no limit per app, no record of who holds
it, and a key that outlives the app. This integration makes a key for each
deployment and each preview instead, with a spending limit, and revokes it
when the deployment is taken down or the preview ends.

## 1. Make a provisioning key

At openrouter.ai, under Settings › Provisioning API keys, create a
provisioning key. It can create, read, change and delete ordinary API keys
on your account; it cannot call models. Put it in AppTown's secrets as
`OPENROUTER_PROVISIONING_KEY` (Infisical, or AppTown's secrets file).

Do not give it a name starting `STEP_`: those secrets are handed to
workflow steps and workers, and AppTown refuses a provisioning key from
one, when the connector is saved and every time it is used.

## 2. Bind it

Integrations › AI models (OpenRouter):

- **Provisioning key**: the secret's name, `OPENROUTER_PROVISIONING_KEY`.
- **Most per key ($)**: the largest limit any one key may have. Required.
  An app that asks for more is refused when it deploys, and so is every
  later deploy that still asks for more.
- **Default limit ($)**: a deployment's limit when the app declares none
  (empty: $1).
- **Preview limit ($)**: every preview's limit, whatever the app declares
  (empty: $1).
- **API URL**: empty for OpenRouter.

Test lists the keys the provisioning key manages and changes nothing.

## 3. What attaching does

Someone who may configure a project's integrations attaches AI models
under Settings › Services (`+ AI models`), or with
`POST /projects/{slug}/integrations {"capability":"llm"}`. Attaching is
only the project's choice of connector: no key is made then. A person who
can deploy but not configure integrations cannot attach it, and declaring
the need in `.apptown/app.yaml` without the attachment makes no key: the
deploy is refused with "attach AI models first", and OpenRouter is not
called.

## 4. What deploying does

For an app that declares `needs: { llm: … }`:

- **A deployment's first deploy** makes an OpenRouter key named
  `apptown-<project>-<deployment>-<id>` (a key made before 1.0 keeps its `forge-` name), with the declared limit and reset.
  AppTown records it on the deployment's list of what it owns, then keeps
  the key in its built-in store. The app gets `LLM_API_KEY` and
  `LLM_BASE_URL`.
- **Every later deploy** keeps the same key and sets its limit and reset
  again, so a change in `.apptown/app.yaml` (including only the reset period)
  takes effect. If OpenRouter refuses the change, the deploy fails.
- **Take-down** revokes the key and deletes the stored copy. A revoke that
  fails is kept as a retained row naming the key's hash: revoke it by hand
  at openrouter.ai, then mark the row released.
- **A preview** gets its own key, named `apptown-preview-<preview>`, with the
  preview limit. Starting the same preview again keeps it. Once the
  preview no longer stands on the worker or this AppTown's Docker (stopped,
  discarded, reaped for age, removed with its project, or cleared when AppTown started again), AppTown revokes its key within a minute. A failed
  revoke becomes a retained row too.

An intranet release (Portainer) is refused for an app that needs AI
models: nothing would revoke a key given to it.

The deployment's card shows what each key may still spend this period and
what it has spent in all, read from OpenRouter when the card shows, or
"allowance unknown" with why.

## Known limits

- If AppTown stops between OpenRouter making a key and AppTown
  recording it, the key is left at OpenRouter. Its name says which
  deployment or preview it was for: delete it by hand.
- On a worker, a preview counts as there while the worker still lists it,
  and the worker lists a preview's folder too: a preview container removed
  by hand on the worker keeps its key until the preview is removed through
  AppTown or AppTown starts again.
- To move to another OpenRouter account, take the deployments down first
  (their keys are revoked), change the connector, then deploy again.
