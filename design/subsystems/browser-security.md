# Browser request security

Root Forge applies browser security to Forge responses inside the virtual-host
router. Hosted applications retain their own origin and security policy.

Forge sets `nosniff`, frame denial, a limited CSP restricting ancestors/base/objects,
and a same-origin referrer policy. This is not a complete script-source CSP.
Untrusted Markdown and file serving need their own content handling.

Unsafe methods use Go's cross-origin protection and, when Origin is present,
require the exact configured scheme, host and port. Sibling application domains
are not trusted origins. Headerless native clients still authenticate through
their normal protocol. Console WebSocket attachment separately requires Origin
because an interactive GET connection can subsequently send input.

HTTPS login uses a host-only `__Host-forge_session` cookie with Secure, HttpOnly,
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

- [internal/forge/browser_security.go](../../internal/forge/browser_security.go)
- [internal/forge/console_pty.go](../../internal/forge/console_pty.go)
- [internal/forge/oauth.go](../../internal/forge/oauth.go)
- [internal/forge/oauth_consent.go](../../internal/forge/oauth_consent.go)
