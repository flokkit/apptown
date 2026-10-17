# ADR: Runtime needs and per-environment fulfillment

> Superseded in part, 17 October 2026: the needs contract is now one per need,
> stated in the catalogue (`provides:`) and in the services-and-data guide;
> `dynamodb` was withdrawn before 1.0 and error reporting, sign-in and telemetry
> are integrations, not needs.

Status: **proposed** (2026-09-01). Consolidates the dependency catalog
(`dependencies.go`, built 2026-09-01) and project integrations
(`project_integrations.go`, `connectors.go`, service-kinds) into one app-facing
surface. Companion to design/architecture/contracts.md (the contract) and design/subsystems/integration-runtime.md (the current integration model).

## Context

The forge grew two systems that are, on inspection, the same idea:

- **Dependencies** (just built): an app declares needs (`needs:` in
  `.forge/app.yaml` today; `x-forge-needs:` was the retired compose form);
  the docker provider fulfills them with local emulators + injected connection
  env; a `cloud` block awaits a terraform executor. Per-app resources,
  provisioned fresh, emulator-in-preview / real-in-cloud.
- **Project integrations** (older): a project attaches SSO, error reporting,
  customer feedback, a container registry, telemetry; the forge provisions or
  registers with an org service and injects env. Org-shared singletons the app
  connects to.

Both are: *the app needs a capability → the environment fulfills it → env is
injected → the app code doesn't change.* SSO is the case that exposes the
unity — it's a **need** ("users log in") fulfilled by **registering with** a
shared Authentik, and it would benefit from the same emulator-in-preview trick
dependencies get (a mock OIDC in previews instead of poking ephemeral clients
into the real Authentik).

The distinction people reach for — "dependency vs integration" — turns out not
to be a difference in *kind*. It's a difference in **fulfillment strategy**.

## Decision

**One declaration surface, one catalog, per-environment fulfillment.**

1. The app declares **needs** in the contract, versioned in the repo,
   environment-independent:
   ```yaml
   x-forge-needs: [postgres, s3, sso, errors]
   ```
   The need is the noun. It never changes across environments.

2. A **capability catalog** entry per need declares how it is fulfilled, via one
   of two strategies (an entry may support both, chosen per environment):

   - **PROVISION** — the app gets *its own* instance.
     - *local*: an emulator container (minio, dynamodb-local, a postgres).
     - *cloud*: a terraform-provisioned resource (a bucket, a table, an RDS).
   - **REGISTER** — the app connects to a *shared org singleton* and gets its
     own client/key inside it.
     - *local*: a mock/dev emulator where one exists (a mock OIDC, a local
       collector) — so previews never touch the shared prod instance.
     - *deployed*: mint a client/key via the org connector + adapter (today's
       integration provisioning — `provisionAuthentik`, `provisionErrors`, …).

   Both strategies **inject env** into the app; both keep the **binding**
   (bucket name, OIDC client id/secret, DSN, endpoint) in the control plane,
   per-environment; the **need** stays in the repo. Same governance split as
   the runtime contract.

3. The **active provider / environment picks the fulfillment**. The docker
   provider fulfills PROVISION needs with emulators and REGISTER needs with a
   local mock (or the org connector if the env is a deployed one). A cloud/org
   provider fulfills PROVISION with terraform and REGISTER with the connector.

`service` and `integration` stop being user-facing categories. They are the two
verbs — provision a per-app instance, or register with a shared one — and the
catalog entry says which, per environment.

## The map — every project-level integration under this model

| Capability | Today | Strategy | Local (preview) | Deployed | Injects |
|---|---|---|---|---|---|
| **postgres / db** | compose service, or none | PROVISION | postgres container | container, or managed (RDS via terraform) | `DATABASE_URL` |
| **s3** | dependency (new) | PROVISION | minio | terraform S3 bucket | `AWS_ENDPOINT_URL_S3`, creds, region, bucket |
| **dynamodb** | dependency (new) | PROVISION | dynamodb-local | terraform table | `AWS_ENDPOINT_URL_DYNAMODB`, creds, region, table |
| **redis / cache** | compose service | PROVISION | redis container | container, or managed (ElastiCache) | `REDIS_URL` |
| **mail** | — (new) | PROVISION | mailpit | SES/relay (terraform or register) | `SMTP_HOST/PORT/USER/PASS` |
| **sso** | integration (Authentik: direct / provision) | REGISTER | mock-OIDC / dev Keycloak | register OIDC client in org Authentik | `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET` |
| **errors** | integration (GlitchTip, provision) | REGISTER | none, or a local GlitchTip | register project in org GlitchTip | error DSN |
| **feedback** | integration (Customer Feedback, provision) | REGISTER | none / stub | register app, mint key | app key + service URL |
| **telemetry** | integration (OTLP service-kind) | REGISTER | local OTLP collector | org OTLP endpoint | `OTEL_EXPORTER_OTLP_ENDPOINT` |
| **llm** (5 Oct 2026, built) | attachment of the `openrouter` connector | REGISTER (`openrouter-key`) | its own small-limit OpenRouter key per preview | one OpenRouter key per deployment, revoked at take-down | `OPENROUTER_API_KEY`, `OPENROUTER_BASE_URL` |
| **container-registry** | integration (mint per-project token) | PROVISION (per-project token in a shared registry) | local registry / skip | mint scoped deploy token | repo, username, token |

Two observations from the map:

- **A capability can flip strategy by deployment.** `postgres` is PROVISION as a
  container you own, but "a database in the org's shared cluster" is REGISTER.
  The catalog entry can offer both; the environment/provider chooses. `sso` is
  almost always REGISTER; `s3` almost always PROVISION — but neither is
  categorically fixed.
- **Almost everything gains a local emulator/mock.** The biggest concrete win of
  the merge: REGISTER needs (sso, telemetry, errors) get a *preview* fulfillment
  that isn't the shared prod instance — a mock OIDC, a throwaway collector — so
  previews are self-contained and never register ephemeral state in real org
  services. (5 October 2026: `llm` is the first REGISTER need executed at
  boot and a deliberate exception: a model has no useful emulator, so each
  preview registers its own small-limit key, which it owns and which is
  revoked when the preview ends; see
  the plan.)

## Parameterized needs — the shape of a need

A bare capability name is almost a lie. `postgres` isn't a need; `postgres ≥16
with pgvector` is. `sso` isn't a need; `authorization-code + PKCE, scopes
[openid, email, groups]` is. So a need can carry parameters. The flat list stays
valid as sugar for "no parameters — give me the catalog default":

```yaml
x-forge-needs:
  postgres: { version: ">=16", extensions: [pgvector] }
  sso:      { flow: auth-code-pkce, scopes: [openid, email, groups] }
  s3:       {}          # bare — catalog default
```

**The parameters split the same way the whole model does — requirement vs.
binding.**

- **The app's requirement → repo, versioned, and a *constraint*, not a pin.**
  The app declares a floor and a feature set (`version: ">=16"`,
  `extensions: [pgvector]`, `flow: auth-code-pkce`) because that is what is
  actually true: its SQL breaks without pgvector, its code is written for one
  flow, and it does *not* care whether it gets 16.4 or 17. Pinning an exact
  patch version in the repo is the mistake — it makes the app lie about what it
  requires and welds it to one environment. This is semver ranges / mise tool
  constraints / terraform version pins, applied to services.
- **The environment's choice → binding, control-plane, per-environment.** The
  exact image or managed offering, the patch version, the pool size — and the
  parts that *cannot* live in the repo because they differ per environment: the
  OIDC redirect URIs (a preview URL ≠ a prod URL), the client secret, the
  issuer. The app touches those only through injected env; it never states them.

On sprawling standards (OIDC being the clearest example): **the app describes the
small slice it uses, not the standard.** A web app uses one flow, a few scopes,
maybe refresh + back-channel logout — a finite profile. The thousands of other
extensions are things it never reads, so they are not its need. An exotic
requirement (CIBA, DPoP) becomes a *named profile* the catalog either offers or
doesn't — never an ad-hoc field.

### You do not schematize the universe

The parameters do **not** require a schema per capability. Schema is an optional
upgrade for the popular few, never the entry fee. Three rungs, all of which
work:

- **Tier 0 — bare name, no schema.** `postgres` = the catalog default. The
  catalog entry already carries an image + inject env (the built shape); you get
  a working default. This rung exists today.
- **Tier 1 — freeform passthrough, no schema.** The need carries an opaque
  params bag and the fulfillment template *interpolates* it — the same
  substitution as `{host}`, one level up: the entry's image / command /
  terraform vars reference `{version}`, `{extensions}`. The forge does not
  *understand* the params, it substitutes them; a bad value fails loud at
  fulfillment. ~90% of the value for the cost of a template string — the catalog
  author writes a **template, not a schema**.
- **Tier 2 — schema only where it earns it.** `postgres` and `sso` get a real
  schema (validation, defaults, autocomplete) because everyone uses them and
  their failures are nasty. A bespoke one-off capability stays at Tier 1
  forever.

Two things keep even Tier 2 cheap: **borrow the schema, don't invent it** —
postgres versions/extensions are a known list, OIDC flows/scopes are defined by
the spec, and for cloud-backed needs the terraform module's `variables.tf` *is*
the schema (point at it, don't write a second) — and **write it once per
capability, not per app** — one postgres schema serves every app in the org, so
the schema count is bounded by distinct capability *kinds* (a small curated
catalog), not by apps.

This is how every real ecosystem already does it: Helm `values.yaml` is
freeform and `values.schema.json` is optional and usually skipped; Docker takes
arbitrary env with no schema; terraform lets you pass vars freely and the typed
schema is the module author's call. Freeform by default, schema as opt-in
quality.

**The one honest tradeoff:** Tier 1 moves validation from authoring-time to
fulfillment-time — a bad param surfaces at boot, not in the editor. That is the
same "fail loud, slightly later" bargain the rest of the contract already
accepts; Tier 2 buys the early failure back for the needs where it is worth it.

**The rule that stops sprawl:** keep the constraint language deliberately dumb —
a version range, a named feature/extension list, a small profile enum. The
instant an arbitrary predicate is tempting, the answer is a new *named profile*
added centrally to the catalog entry, not a bigger per-app DSL. A
Turing-complete needs file would make every app's needs hard to read and
maintain. The complexity of a
capability lives once, in its catalog entry, maintained by whoever owns it; an
app states only its deviations from the default and stays a one-liner.

## Scope — needs vs. forge machinery

**In scope** (app-facing needs the app declares): postgres/db, s3, dynamodb,
redis, mail, sso, errors, feedback, telemetry, container-registry.

**Out of scope** (ambient forge machinery, stay as instance-level connectors —
they are how the *forge* builds and runs, not things an *app* declares it
needs): `scanner` (osv/gitleaks/sbom), `model` (pi/anthropic), `secrets`
(infisical), `executor` (docker), `deploy` (docker/portainer), `scm` (gitlab),
and the package-registry *mirror* (`registry`). These already bind once for the
whole instance; folding them into per-app `x-forge-needs` would be a category
error.

## Consequences

- **One mental model.** A person declares *what the app needs*; the forge
  decides *how each environment supplies it*. The service/integration/dependency
  vocabulary collapses into needs + fulfillment strategy.
- **Previews stop touching prod.** REGISTER needs get preview emulators; no more
  ephemeral OIDC clients in the real Authentik.
- **A new capability is one catalog entry** carrying its PROVISION and/or
  REGISTER fulfillments per environment — the `dependencyKind` shape generalized
  to hold a `register` block (connector + adapter) alongside the existing
  `local`/`cloud` provision blocks.
- **Governance is unchanged and reinforced:** the need is versioned code; the
  binding is control-plane, per-environment, injected last (topology wins). SSO
  config-as-code (the earlier ruling) rides on this: the *need* and any
  schema/policy files are in the repo; the *client credentials* are the binding.
- **The Integrations tab becomes "how this project's needs are bound"** —
  a view over per-environment fulfillment — rather than a separate noun.

## Migration / compatibility

- `project_integrations` + connectors + the `provision*` adapters **stay** —
  they become the REGISTER backend. Nothing is thrown away; it's re-fronted.
- `x-forge-needs` is the additive surface. A need with a REGISTER catalog entry
  resolves to the existing attach/provision path; existing attaches keep working
  and are read as bindings for their capability.
- The `dependencyKind` struct gains a `register:` block (connector name + which
  adapter mints the client/key + the env it yields), parallel to `local:` and
  `cloud:`. Stock catalog entries move from `connectors.go`/service-kinds into
  `stock/dependencies/*.yaml` over time; both readable during transition.
- Instance-level connectors untouched.

## Open questions

- **Binding granularity across environments.** A project has several
  environments (preview, main, prod). Is the SSO client one-per-environment or
  shared? Likely per-environment for isolation, one-per-deployed-env in
  practice. The binding table is keyed `(project, environment, capability)`.
- **container-registry** sits between PROVISION and REGISTER (a per-project token
  *in* a shared registry). Modeled as PROVISION with a shared backing; worth a
  second look.
- **Emulator fidelity** for REGISTER previews — a mock OIDC that returns a fixed
  user is enough to exercise the middleware; a full dev Keycloak is heavier but
  real. Start with the mock.
- **The parameter/profile vocabulary per capability** — the "Parameterized
  needs" section sets the rules (constraint-not-pin, borrow-the-schema,
  keep-it-dumb); the actual accepted fields for postgres, sso, etc. are still to
  be pinned down per catalog entry as each reaches Tier 2.
- **Ordering** — some needs depend on others (an app needs its DB migrated
  before it seeds); the fulfillment order and readiness gating want a pass.

## Why this is the right shape

It is the capability/binding pattern the forge already runs three times
(scanners, registries, model slots) — a capability resolved to a binding —
lifted to the app's *runtime* needs and made per-environment. The forge already
believes "declare intent, the platform supplies the topology." This makes the
app's backing services obey the same rule as its toolchain and its deploy
target: **the app says what it needs; where and how it gets it is the
environment's job, bound in the control plane, never hard-wired in the repo.**
