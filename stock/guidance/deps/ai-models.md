---
name: deps/ai-models
tags: deps,ai,llm,security
---
AI models (the `llm` need), when the app calls a model:

- Declare the need in `.forge/app.yaml` (`needs: { llm: { limit: 5 } }`).
  The forge gives each deployment and each preview its own key with a
  spending limit. Never ask for, paste or commit a key of your own.
- Use an OpenAI-compatible client. Take the base URL from
  `OPENROUTER_BASE_URL` and the key from `OPENROUTER_API_KEY`, and from
  nowhere else. Name the model in the request (for example
  `anthropic/claude-haiku-4.5`); keep the model name in one place.
- Never log, print, return or store the key, including in error messages
  and debug output. Call the model from the server only: a browser must
  never receive the key.
- Absent variables mean AI is off (local development): the feature says so
  plainly, and the rest of the app works.
- A key that has used its allowance gets HTTP 402 (or 403). Treat it as a
  normal state the person sees ("AI is unavailable until the allowance
  resets"), not a crash. Set a timeout on every call and handle failure.
- Keep prompts and calls in one small module, so the model, limits and
  error handling are in one place.
