# AI models — developer guide

## What it's for

Your app can call AI models (chat, summaries, extraction) through
OpenRouter, with its own key and a spending limit, without anyone pasting a
key anywhere.

## Declare it

The project must have AI models attached (Settings › Services). Then, in
`.apptown/app.yaml`:

```yaml
needs:
  llm:
    limit: 5            # US dollars of credit; omitted = the connector's default
    limitReset: monthly # daily | weekly | monthly; omitted = never renews
```

The limit may not exceed what the connector allows. Previews always get
the connector's preview limit.

## What you get

Each deployment and each preview has its own key. The app's parts receive:

- `LLM_API_KEY` — the key.
- `LLM_BASE_URL` — the base URL of an OpenAI-compatible API
  (`https://openrouter.ai/api/v1` with OpenRouter).

## What your code must do

- Use an OpenAI-compatible client with exactly those two variables:

  ```js
  const client = new OpenAI({ apiKey: process.env.LLM_API_KEY, baseURL: process.env.LLM_BASE_URL })
  ```

- Never log, print, return or commit the key, including in error messages.
  Call models from the server; a browser must never receive the key.
- Absent variables mean AI is off (local development): say so plainly and
  keep the rest of the app working.
- HTTP 402 means the allowance is used up: show the person that AI is
  unavailable until it resets. Set a timeout on every call.

The deployment's card under Settings › Deployments shows what the key may
still spend.
