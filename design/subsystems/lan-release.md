# Intranet releases

The intranet path is a Deployment operation using the configured Portainer target.
It ships the project's latest bundle, which the landing that made it published
to the project's container registry by digest; it builds nothing. A latest
bundle of image IDs, or none, is refused with the same next action as a release
to Portainer by name: attach a container registry to the project, then land a
change. It records release progress, renders the stack and delegates provider
execution. It is not a separate approval system or permission to ship
unrecorded chat state.

Target and registry credentials are resolved through configured integrations and
secrets. Runtime consumers receive their declared values; provider credentials
are for the operation that needs them. The app's public service is given the
project's attached integrations' values as a Docker host gives them, under its
own environment and secrets, and a static public part its browser values
(`APPTOWN_BROWSER_ENV`); other parts and init steps are given neither. Target-specific domains, volumes and
networking are rendered by the Portainer/Docker implementation.

The Portainer environment is `apptownd -portainer-environment` (default `local`),
recorded in the managed `portainer-deploy` binding at every start
(`integration.Seeds`); the seeded `intranet` target reads it from the binding
at use time, so a start with another setting moves later releases there.

Every value the stack carries means what it says, as with `docker run` on a
Docker host: Compose interpolates `$` in a stack file, so the renderer writes
each `$` in an environment value, command, healthcheck or mount path as `$$`
(`deploy.ComposeLiteral`; the `apptown dev` compose files follow the same rule).

A deployment on Portainer can also be released by name through the general
release door (`RequestRelease`). Both doors render the same stack, needs
included, with one builder; see
[deployment.md](deployment.md#release-bundles).

Release records and logs provide the selected source and observed result. Provider
execution can fail or remain uncertain after an external effect, so admission,
progress and successful settlement must not be conflated. Use the common
[deployment runtime](deployment.md) guarantees when diagnosing one.

## Implementation references

- [internal/apptown/deployment/intranet_release.go](../../internal/apptown/deployment/intranet_release.go)
- [internal/apptown/deployment/providers.go](../../internal/apptown/deployment/providers.go)
- [internal/apptown/deployment/intranet_release_test.go](../../internal/apptown/deployment/intranet_release_test.go)
