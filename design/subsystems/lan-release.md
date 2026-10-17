# Intranet releases

Status: current implementation. Checked on 18 September 2026.

The intranet path is a Deployment operation using the configured Portainer target
and registry. It packages selected source, records release progress, renders the
stack and delegates provider execution. It is not a separate approval system or
permission to ship unrecorded chat state.

Target and registry credentials are resolved through configured integrations and
secrets. Runtime consumers receive their declared values; provider credentials
are for the operation that needs them. Target-specific domains, volumes and
networking are rendered by the Portainer/Docker implementation.

Every value the stack carries means what it says, as with `docker run` on a
Docker host: Compose interpolates `$` in a stack file, so the renderer writes
each `$` in an environment value, command, healthcheck or mount path as `$$`
(`deploy.ComposeLiteral`; the `forge dev` compose files follow the same rule).

A deployment on Portainer can also be released by name through the general
release door (`RequestRelease`). Both doors render the same stack, needs
included, with one builder; see
[deployment.md](deployment.md#release-bundles) and the

Release records and logs provide the selected source and observed result. Provider
execution can fail or remain uncertain after an external effect, so admission,
progress and successful settlement must not be conflated. Use the common
[deployment runtime](deployment.md) guarantees when diagnosing one.

The old host setup and rollout account remains historical. Its particular stack
names, certificates and live-host observations do not certify a new installation.

## Implementation references

- [internal/forge/deployment/intranet_release.go](../../internal/forge/deployment/intranet_release.go)
- [internal/forge/deployment/release_packaging.go](../../internal/forge/deployment/release_packaging.go)
- [internal/forge/deployment/providers.go](../../internal/forge/deployment/providers.go)
- [internal/forge/deployment/intranet_release_test.go](../../internal/forge/deployment/intranet_release_test.go)
