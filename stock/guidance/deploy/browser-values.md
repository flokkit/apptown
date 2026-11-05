---
name: deploy/browser-values
tags: deploy,integration,static,platform
tier: platform
checks: [{"name": "page-loads-browser-values", "expects": "a static app whose page AppTown gives values (an attached integration's public key or address) is served exactly those at /_apptown/env.js, and its HTML loads that file in <head> before any other script", "severity": "high", "evaluator": "runtime", "probe": "browser-env", "title": "Load the values AppTown gives the page", "appliesTo": ["static"]}]
---
Static pages cannot read environment variables. For attached services a page
uses, AppTown serves visitor-safe values at `/_apptown/env.js` as
`window.__APPTOWN_ENV__`, never secrets. Server apps read them from their environment.

Load exactly `<script src="/_apptown/env.js"></script>` first in `<head>`,
before other scripts. Only `src` and an optional CSP `nonce` are allowed:
no `async`, `defer`, `nomodule`, `integrity`, `type` or `language`.
No `<base href>`. Data blocks such as `<script type="application/json">` may precede it.

Read `window.__APPTOWN_ENV__ || {}` and use a value only if present, so the page
works unattached, locally and from `file://`. Never copy these values into code.
