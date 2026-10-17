---
name: deploy/browser-values
tags: deploy,integration,static,platform
tier: platform
checks: [{"name": "page-loads-browser-values", "expects": "a static app whose page the forge gives values (an attached integration's public key or address) is served exactly those at /_forge/env.js, and its HTML loads that file in <head> before any other script", "severity": "high", "evaluator": "runtime", "probe": "browser-env", "title": "Load the values the forge gives the page"}]
---
A static site cannot read environment variables: the files are served as
they are. When the project is attached to a service a page can use (error
reporting, a feedback widget), the forge serves the values that are safe for
any visitor to read at `/_forge/env.js`, as `window.__FORGE_ENV__`. Secrets
are never there.

Load it exactly like this, as the first script in the page's `<head>`,
before any other script, inline or not:

    <head>
      <script src="/_forge/env.js"></script>
      <script src="game.js"></script>
    </head>

Write the path exactly `/_forge/env.js`. Give that tag no attribute but
`src` (a CSP `nonce` is fine): no `async`, `defer`, `nomodule`,
`integrity`, `type` or `language`. Do not use
`<base href>` in the page. The forge checks this against the running
preview; anything else is refused even where a browser might cope, because
the check cannot tell which arrangements a browser would run. Data blocks
such as `<script type="application/json">` may come before it.

Read from it with a fallback, so the page works unchanged with nothing
attached, in local development and from `file://`:

    const env = window.__FORGE_ENV__ || {};
    if (env.SENTRY_DSN) { /* start error reporting */ }

Never copy a value from it into the code: each deployment gets its own, and
`/_forge/` is the forge's path, not the app's. A server app reads the same
values from its environment instead.
