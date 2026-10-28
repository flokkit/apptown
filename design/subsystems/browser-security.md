# Browser request security

Root AppTown applies browser security to AppTown responses inside the virtual-host
router. Hosted applications retain their own origin and security policy.

AppTown sets `nosniff`, frame denial and a same-origin referrer policy. Its
Content Security Policy defaults to no resources, permits scripts and fonts only
from its own origin, and allows local stylesheets and inline styles for React,
Mermaid and the terminal. Inline scripts and eval are not permitted. The OAuth
login page loads its fixed script from `/oauth/login.js` on the same origin.

Images may come from AppTown, raster data URLs, or `https://apptown.dev`, the
public origin of the tour's private storage bucket. `web/resource-policy.json`
is the single remote-image origin list consumed by both the browser middleware
and the Markdown renderer. The standalone tour website has its own document
and is outside this middleware; hosted apps and previews keep their own policy.
Markdown is sanitized in an inert fragment: disallowed HTTP(S) images become
labelled links, unsafe schemes become text, and alternative image fetch paths
such as srcset, media and SVG are removed. Raster data images are limited to
PNG, JPEG, WebP and GIF. Blob images are not enabled. Raw attachments retain
their stricter content-based response headers.

Connections may reach AppTown's API and same-host WebSocket endpoint, plus the
validated HTTP(S) origin of AppTown's configured errors attachment. The same
reporting owner and attachment cache supply the browser DSN and CSP exception;
request parameters and other projects cannot select it. No diagnostic origin
is admitted when the browser DSN is invalid or absent. An open document keeps
its CSP until refreshed after a reporting configuration change. Sentry's code
is bundled locally; the exception admits reporting connections, not scripts.
External navigation and authentication redirects remain available.

Local script permission and inline styles do not establish complete XSS
resistance. Untrusted Markdown and file serving retain their own sanitization
and content-handling boundaries.

Unsafe methods use Go's cross-origin protection and, when Origin is present,
require the exact configured scheme, host and port. Sibling application domains
are not trusted origins. Headerless native clients still authenticate through
their normal protocol. Console WebSocket attachment separately requires Origin
because an interactive GET connection can subsequently send input.

HTTPS login uses a host-only `__Host-apptown_session` cookie with Secure, HttpOnly,
Path=/ and SameSite=Lax. HTTP development uses its separate cookie name. Public
host/TLS configuration determines the origin; arbitrary forwarded host/protocol
headers do not select it.

OAuth consent uses a short-lived server record bound to person, browser and request.
Allow/cancel consumes it once. Replaying consent or modifying its bound request
cannot issue another code. Failure after consumption requires reopening consent.

These checks do not establish provider-key containment, tenant isolation or complete
CSRF/XSS resistance across every hosted app. See [authentication](auth.md) and
[inference security](inference-security.md).

## Implementation references

- [internal/apptown/browser_security.go](../../internal/apptown/browser_security.go)
- [internal/apptown/console_pty.go](../../internal/apptown/console_pty.go)
- [internal/apptown/oauth.go](../../internal/apptown/oauth.go)
- [internal/apptown/oauth_consent.go](../../internal/apptown/oauth_consent.go)
