---
name: deploy/browser-values
tags: deploy,integration,static,platform
tier: platform
checks: [{"name": "page-loads-browser-values", "expects": "a static app whose page AppTown gives values (an attached integration's public key or address) is served exactly those at /_apptown/env.js, and its HTML loads that file in <head> before any other script", "severity": "high", "evaluator": "runtime", "probe": "browser-env", "title": "Load the values AppTown gives the page"}]
---
A static page cannot read environment variables. When the project is attached
to a service a page uses (error reporting, a feedback widget), AppTown serves
the values safe for any visitor at `/_apptown/env.js`, as
`window.__APPTOWN_ENV__`. Secrets are never there.

Load it as the first script in `<head>`, before any other script, exactly:

    <head>
      <script src="/_apptown/env.js"></script>
      <script src="game.js"></script>
    </head>

The path is exactly `/_apptown/env.js`; the tag has no attribute but `src` (a
CSP `nonce` is fine): no `async`, `defer`, `nomodule`, `integrity`, `type` or
`language`. No `<base href>` in the page. Data blocks such as
`<script type="application/json">` may come before it.

Read it with a fallback, so the page works with nothing attached, locally and
from `file://`:

    const env = window.__APPTOWN_ENV__ || {};
    if (env.SENTRY_DSN) { /* start error reporting */ }

Never copy a value from it into code. A server app reads the same values from
its environment.
