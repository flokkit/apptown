# Application ingress

Status: current routing; common ingress ABI proposed. Checked on 18 September 2026.

Root Forge routes public hostnames through VHost. Deployment derives application
URLs and probe request headers from configured host/tier information. Provider
renderers apply their own routing configuration.

Application origins are separate from Forge's browser origin. An application
subdomain does not gain permission to send browser mutations to Forge. Model
inference uses its private listener and worker-network policy rather than public
application ingress.

## Addresses

Which name reaches which deployment, by target:

| Target | `main` | Any other deployment |
| --- | --- | --- |
| The forge's own Docker host | `<slug>.<main tier domain>`, through forged's front door (VHost); without a main tier domain, the forge's host and the published port | the forge's host and its published port |
| An attached Docker host with an ingress domain | `<slug>.<domain>`, through the host's own Traefik | the host's address and its published port; it carries no Traefik route |
| An attached Docker host without a domain | the host's address and its published port | the same |
| Portainer | one deployment per project, named for the target's tier, at the project's name | refused |
| App Platform | the platform's own name for each app | the same |

The project's name belongs to `main` on every Docker host
(`docker.Scope.RoutesDeployment`). Traefik joins every container that
declares one router into one service and splits requests between them, so a
second deployment carrying main's route took about half of main's visitors
(soak finding 55, 19 October 2026,
plan). A name per
deployment is after 1.0, as it is on Portainer.

Previews are reached under the preview tier domain (`change-<n>`,
`<slug>-release-<n>`), never at a deployment's name.

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
