# Application ingress

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
second deployment carrying main's route would take about half of main's
visitors. A name of its own for each other deployment is not built (nor on
Portainer).

Every standing deployment on a Docker host is published at one host port,
picked and recorded before its first start, whether a landing or a release
starts it (`deployment.Deployments.StablePort`). Docker keeps a port it was
given at every start of the container, and picks a port it chose itself
again at every start, so the forge records the port and probes and shows that
one. Previews take a
port Docker picks: they are never restarted in place, and their port is
asked of Docker each time it is used.

Previews are reached under the preview tier domain (`change-<n>`,
`<slug>-release-<n>`), never at a deployment's name.

## No common ingress contract

There is no single owner or type describing hostnames, paths, TLS,
authentication context and probe rendering for every target. Each adapter is
checked on its own.

Any future consolidation should make the selected request context explicit and
avoid trusting arbitrary forwarded headers. Provider routing support, authenticated
front-door checks and application interpretation are separate concerns. See
[browser security](browser-security.md), [health checks](healthchecks.md) and
[deployment runtime](deployment.md) for implemented behaviour.

## Implementation references

- [internal/forge/vhost.go](../../internal/forge/vhost.go)
- [internal/forge/deployment/docker_runtime.go](../../internal/forge/deployment/docker_runtime.go)
- [internal/forge/browser_security.go](../../internal/forge/browser_security.go)
