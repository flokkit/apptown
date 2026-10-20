# Contracts: the interfaces between code and the platform

This is the map of the promises that
cross the line between an app's code and the systems that build, run,
and ship it. Each contract has a direction (who promises what to whom),
an artifact (where the promise is written), and an enforcement point
(what breaks loudly when the promise is false). The types are numbered;
the numbering is used elsewhere in the repo.

This file is the map. For supported runtime fields, validation and local
development, use the shipped [contract reference](../../stock/docs/guides/contract-reference.md).

## Type 1 — code → platform: "how to run me"

The runtime declaration is `.apptown/app.yaml`, read from the selected source
revision. `internal/deploy/appcontract` parses it into app parts, ordered
initialization, needs and secret metadata. Each app part selects a build context,
image or static assets; exactly one is public. A `dataDir` declares where that
part needs durable data, while the target owns volume placement. Needs name
catalog capabilities; the active target decides how to fulfill them. The file
describes app intent; provider credentials and host topology belong in platform
configuration. The parser rejects
unknown fields and unsupported versions; a missing or invalid declaration
cannot be repaired by changing a newer revision. The boot gate, previews and
deployments consume the selected declaration, subject to target support.

`apptown dev` prepares local needs for processes; `apptown dev up` generates and
runs Compose for the app and its local needs. Generated Compose is an output of
the native contract, not a second authored runtime contract. `mise.toml` or
`.mise.toml` declares development toolchains; deployed images and Dockerfiles
supply their own tools. See [toolchains](../../stock/docs/guides/toolchains.md).

Forge owns host bindings, restart policy, volume placement and credentials, so
full Compose is not accepted as the contract: it mixes app intent with host
topology and permits unsafe controls. `.apptown/app.json` and
`.apptown/compose.yaml` are not read. The [native-contract decision](../decisions/forge-native-contract.md)
says why the contract is not a Compose file.

## Type 2 — platform → code: the ABI

The inverse promise: what a selected target supplies to app code. For the
Docker driver, the current behavior includes:

- A cold preview starts from selected source and prepared needs, without
  inheriting a warm development process. Standing deployments may retain
  declared data across releases.
- `PORT` for a container with a declared or default container port; host-port
  assignment remains target topology.
- `DATA_DIR` for a declared `dataDir`, applied after contract environment in
  the Docker public app so it points at the mounted volume.
- Fulfilled local needs and app parts on the runtime network, reached by their
  assigned names and connection environment.

Build and agent workspaces have their own toolchain, mirror and registry
preparation. Those facilities, including the dind buildx setup, are not
credentials or tools implicitly inherited by every deployed app image.

**Provider-extension ruling: accepted direction, target-specific
implementation.** Providers should extend the common app interface rather
than redefine its meaning. A provider may add connection environment for a
fulfilled need; resolution and precedence belong to its binding and driver.
Credential references are named in configuration, and values are resolved for
authorized consumers at use time rather than committed in contracts, source
or images. The currently implemented targets are Docker host, Portainer and
DigitalOcean App Platform; their supported features differ. An app using only
common declarations is not thereby guaranteed to run on every target: the
driver must reject unsupported features. AWS and Kubernetes examples in the
original ruling were future illustrations, not implemented adapters. The
[target-support guide](../subsystems/deploy-targets.md) and
[integration-runtime guide](../subsystems/integration-runtime.md) describe the
current limits; the shipped contract reference owns exact injected values.

## Type 3 — work → gates: evidence

What agents promise the gates: artifacts by kind (`build-report`,
`scan-report`, `sbom`, `release`), report files, review claims that
must match the diff, conformance with imported guidance. The workflow
spec (stages → steps → gates) is this contract's grammar; every run
pins the binding versions and config hashes it used, so receipts cannot
be retro-edited.

Enforced by: `artifact/require`, `agent/review`, `guidance/conformance`,
scans, and the gate rule itself. Second-most machine-checked.

## Type 4 — engine → infrastructure: deployment conventions

What a promotion IS, mechanically, per target:

- Always: ship the COMMITTED tree; the image digest is the receipt; the
  target platform's architecture is checked, not assumed.
- Portainer/intranet: stack `forge-<slug>`, Traefik label conventions,
  `forge-data-<slug>` volumes, vanity domains as extra Host rules.
- The boundary treaty: terraform owns the Docker host, Traefik, DNS, and pull
  credentials; AppTown owns released stacks and never touches
  /api/registries or UniFi. A new provider (AWS) would carry its own
  treaty — e.g. terraform owns the account, roles, and network; forge
  owns task definitions or whatever the unit of release is there. The
  treaty is part of the provider contract and is recorded where the
  provider is implemented.

Enforced by: release machinery + the guards learned from live failures
(platform mismatch refusal, one release in flight, failed attempts are
numbered records).

## Type 5 — app → app: embed contracts

Product-level interfaces between OUR apps: Customer Feedback's one-line
script tag + app key; an error-reporting DSN. Today these live in the
providing app's README. The intended future: each becomes an AppTown
integration capability (attach "feedback" to a project → the key is
minted and the snippet/env injected), which promotes it to Type-1-style
formality — declared, provisioned, revocable.

Formality today: none. This is the newest and least-guarded type.

## The rulings embedded here

- Contracts are declared by the APP only where the app is the
  authority (how to run, what it needs). Topology, credentials and
  conventions are platform authority and are injected, never declared.
- Providers extend the ABI; they do not fork it.
- No credential ever crosses in a contract artifact; only secret NAMES
  travel in config, values resolve at use time.
- Every contract's documentation lives beside its enforcement (this
  file is the map, not the source of truth for any single one).

## Code map: the dependency catalog

`internal/dependency` owns the catalogued kinds: the `Kind` shape, the
parse rules (a slug capability; fulfilled by PROVISION or REGISTER), the
stock sync from `stock/dependencies/*.yaml` that never overwrites a custom
kind, and `Save` with its receipt (a stock kind is refused). Forge keeps
the fulfilment of declared needs (compose generation, targets, the
services editor) and the doors.
