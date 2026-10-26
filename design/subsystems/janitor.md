# Janitor standing consent

A standing approval records a person's consent for bounded routine updates. It
is separate from a vote on an individual Change. AppTown records the exact
meaning, project UUIDs, workflow purposes, selected environment and target UUIDs,
and the person who enabled it. Changes create new revisions; history is retained.

Automatic processing is not available. Saving consent does not update, accept,
land or deploy software. No existing approval or imported rule enables it.

The project’s Janitor page previews the exact consent before saving. Full delivery
requires explicit environments for every selected project, deployment authority
and promotion consent where required. Nothing selects a default environment on
the person's behalf. “Accept and land only” explicitly leaves running software
unchanged. Administrative rules cover a finite captured project set; they do not
expand to later projects.

Only a verified person acting directly in their browser or personal command-line
instrument can enable or revise consent. Management, approval, landing and
release permissions remain distinct. Current project law and approval selectors
apply; unsupported quorums and independent-approval requirements refuse setup.
The working and landing workflow each declare one supported purpose with
`standing: {class: routine-update, means: ...}` on their approval step. This
declaration provides no authority and changes no ordinary human vote.

A rule names bounded patch/minor dependencies, verified same-version base-image
updates, security fixes within those limits, or governed routine way-of-working
transitions. Application source, migrations, major versions, new capabilities,
needs or secrets, weaker checks, unsupported changes and storage-engine
transitions require a person. Missing or nonpassing evidence is never success.

The enabling principal is recorded by UUID. A renamed person retains identity;
a new account with the old name cannot inherit consent. Current eligibility is
shown separately from enabled state. Database or policy unavailability is an
error, not an eligible rule. Disabling retains history and stops future claims;
an external effect already claimed may finish. A manager can disable consent
after the issuer loses deployment permission.

Supported instance restore disables every enabled imported rule after schema
migration and before application recovery. Original consent remains in history.
Re-enabling requires a new local human confirmation. There is no standing-rule
import endpoint.

The CLI provides `apptown janitor list`, `preview`, `enable`, `disable` and
`history`. Preview accepts a definition through `--file`; enable requires the
exact returned `--confirm` digest. Revising requires `--rule` and `--revision`.
Disable also requires the expected revision. Commands emit JSON and never start
a release. If a save response is lost, inspect current rules before submitting
another new rule; an unknown response is not proof the save failed.

## Inspecting a candidate

`apptown janitor assess RULE --run RUN` inspects an exact run without accepting,
landing or deploying it. The caller needs current read and check-execution
permission. The rule must still be eligible for that project and workflow.
Active writers, unsettled tasks, dirty or unpublished work, changed source and
changed consent refuse an assessment. Inspection never repairs or saves source.

The supported dependency classifier reads the complete committed diff. Its
initial npm contract accepts root `package.json` and version-3
`package-lock.json` with a fixed package graph, canonical public-registry sources
and stable forward patch/minor versions within the rule's bounds. New packages,
graph changes, unrelated files or modes, lifecycle/platform changes and unsupported
ranges require a person. Unknown fields remain part of the comparison.

Both exact inputs undergo frozen installation with scripts disabled in the
observed immutable agent image. Installed package manifests, rather than the
lock's claims alone, establish package identity and install behavior. The result
records source commits, tree, rule and run revisions, tool versions, image and
report digest. Failure or incomplete evidence is `cannot-assess`, never routine
success. Worker scratch-directory removal is not observed in this response; its
owner retries removal through the existing sweep. Container settlement is
required. Classification is historical evidence only, not reusable assent.

Base-image inspection supports one root Dockerfile selected by an unchanged
application contract: one app build at `.`, no needs, durable app data, init,
static build or profile overrides. Its single `FROM` names an explicit
`linux/amd64` or `linux/arm64` platform and
`docker.io/library/node:VERSION-VARIANT@sha256:DIGEST`. The stable three-part
Node version and variant must remain identical; only the digest may change.
Multiple stages, arguments, parser directives, continuations and heredocs
require a person. Every other Dockerfile byte is compared.

The image owner reads both immutable manifests and configurations, refuses
indexes and changed runtime configuration, and confirms the platform. The
existing check executor observes Node's actual version in both prepared images,
with entrypoints cleared, network disabled and no project files or commands.
The prepared image ID must match the verified configuration digest. Pulls use
request-owned anonymous authentication configuration on the selected daemon;
an executing host with `DOCKER_AUTH_CONFIG` set cannot supply this observation.
Existing executor tool caches remain available, and remote scratch cleanup is
still unobserved. A matching tag or publisher label alone is insufficient.
Other images and storage-engine transitions are unsupported.

An enabled security-fix category can add OSV evidence to a supported npm update.
Each attributed fix records the advisory identity, source, modified timestamp,
body digest and exact affected-to-fixed package versions. Supported npm SEMVER
ranges and all explicit affected versions participate. A withdrawn record,
unsupported range, changed identity or absent fixed boundary cannot establish a
fix. Unavailable, unsupported and no-supported-fix observations remain distinct;
none means the software is free of vulnerabilities. Image OS advisory mapping
is unsupported. Advisory evidence never broadens the underlying update category
or substitutes for normal checks.

Combined npm and image changes must satisfy both enabled categories and explain
the complete diff. Governed way-of-working classification remains unavailable.
No inspection result enables automatic acceptance, landing or deployment.
