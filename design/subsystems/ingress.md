# Application ingress

Root AppTown routes public hostnames through VHost. Deployment derives application
URLs and probe request headers from configured host/tier information. Provider
renderers apply their own routing configuration.

Application origins are separate from AppTown's browser origin. An application
subdomain does not gain permission to send browser mutations to AppTown. Model
inference uses its private listener and worker-network policy rather than public
application ingress.

## Addresses

Which name reaches which deployment, by target:

| Target | `main` | Any other deployment |
| --- | --- | --- |
| AppTown's own Docker host | `<serving label>.<main tier domain>`, through apptownd's front door (VHost); without a main tier domain, AppTown's host and the published port | AppTown's host and its published port |
| An attached Docker host with an ingress domain | `<serving label>.<domain>`, through the host's own Traefik | the host's address and its published port; it carries no Traefik route |
| An attached Docker host without a domain | the host's address and its published port | the same |
| Portainer | one deployment per project on the target, named for its configured release tier, with retained deployment-owned addresses | other deployment names are refused |
| App Platform | the platform's own name for each app | the same |

A project's serving label is captured from its initial address and stays fixed.
Project settings and AppTown address aliases do not add application hosts or
certificate names. Built-in routing, attached Docker Host rules, emitted URLs and
page-check Host headers use that same label. A failed identity read does not
synthesize an address from display text. Target domains remain explicit operator
configuration; Portainer has its separate deployment address owner below.

The serving label belongs to `main` on every Docker host
(`docker.Scope.RoutesDeployment`). Traefik joins every container that
declares one router into one service and splits requests between them, so a
second deployment carrying main's route would take about half of main's
visitors. Other Docker deployments have no hostname route of their own.
Portainer retains its fixed tier-name restriction; its address owner separates
routing from project and environment display names.

Portainer address admission records a UUID-derived hostname and optional customer
hostname. Existing observed legacy addresses are preserved after explicit
confirmation. Rendering uses immutable admitted host and certificate inputs, including exact
HTTPS origins for every admitted hostname;
changing a target domain does not move an existing deployment. See
[deployment addresses](deployment.md#deployment-addresses) for claims and restore
behavior.

Every standing deployment on a Docker host is published at one host port,
picked and recorded before its first start, whether a landing or a release
starts it (`deployment.Deployments.StablePort`). Docker keeps a port it was
given at every start of the container, and picks a port it chose itself
again at every start, so AppTown records the port and probes and shows that
one. Previews take a
port Docker picks: they are never restarted in place, and their port is
asked of Docker each time it is used.

Previews are reached under the preview tier domain (`change-<n>`,
`<serving label>-release-<n>`), never at a deployment's name. Historical
release previews retain their original resource names and selected release when
the AppTown address changes; local and worker preview retention use those names.

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

- [internal/apptown/vhost.go](../../internal/apptown/vhost.go)
- [internal/apptown/deployment/docker_runtime.go](../../internal/apptown/deployment/docker_runtime.go)
- [internal/apptown/browser_security.go](../../internal/apptown/browser_security.go)
