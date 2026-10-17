# Application ingress

Status: current routing; common ingress ABI proposed. Checked on 18 September 2026.

Root Forge routes public hostnames through VHost. Deployment derives application
URLs and probe request headers from configured host/tier information. Provider
renderers apply their own routing configuration.

Application origins are separate from Forge's browser origin. An application
subdomain does not gain permission to send browser mutations to Forge. Model
inference uses its private listener and worker-network policy rather than public
application ingress.

## Proposed common contract

The earlier design proposes one neutral ingress description for hostnames, paths,
TLS, authentication context and probe rendering. That common ABI is not implemented
as a single owner/type. Existing adapters must still be checked individually.

Any future consolidation should make the selected request context explicit and
avoid trusting arbitrary forwarded headers. Provider routing support, authenticated
front-door checks and application interpretation are separate concerns. See
[browser security](browser-security.md), [health checks](healthchecks.md) and
[deployment runtime](deployment.md) for implemented behaviour.

## Implementation references

- [internal/forge/vhost.go](../../internal/forge/vhost.go)
- [internal/forge/deployment/docker_runtime.go](../../internal/forge/deployment/docker_runtime.go)
- [internal/forge/browser_security.go](../../internal/forge/browser_security.go)
