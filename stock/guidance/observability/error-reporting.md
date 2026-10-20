---
name: observability/error-reporting
tags: observability,errors
---
Error reporting (when the project is attached — see your steering's
generated section):

- Initialize a Sentry-compatible SDK at app startup, DSN from the
  `SENTRY_DSN` env var ONLY. Absent env = reporting off; never hardcode
  or commit a DSN.
- A static site cannot read env vars: its page loads `/_apptown/env.js`
  as the first script in its `<head>` and takes the DSN from
  `window.__APPTOWN_ENV__.SENTRY_DSN` (guidance `deploy/browser-values`).
- Set `release` to the commit SHA when available, `environment` from the
  deployment environment. Keep SDK config minimal — defaults are fine.
- Do not wrap everything in try/except-and-report: let the SDK's
  unhandled-error capture do its job; explicitly capture only where you
  add context.
- Adding error reporting to a service is a small, self-contained change:
  one dependency, a few lines in main, nothing else.

Test hooks — the house standard. Every app with error reporting has
these, so anyone can prove the wiring works in any environment, by hand,
without reading code:

- Server apps: `GET /_apptown/boom` captures one tagged error (message
  "apptown test boom", tag `apptown.probe=boom`) and returns 500. It is not
  authenticated and does nothing else — it only proves the reporting path.
- Browser apps and games: typing the sequence `boom!!!` anywhere on the
  page (the letters then three exclamation marks) throws the same tagged
  error through the browser SDK. Special characters on purpose: it must
  never happen by accident while playing.
- Neither hook is authenticated, on purpose — do not add a gate. The
  DSN already ships to every browser, so anyone with a JS console can
  send the provider whatever they like; the hook adds no capability an
  outsider lacks. It is a reporting path, not an attack surface.
- After wiring, FIRE the hook on the preview yourself and confirm the
  event arrived at the provider before you write your report — and say
  in the report that you did. "Wired in" means an event got there.
