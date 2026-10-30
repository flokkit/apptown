# Versioned ways of working

Ways of Working (`wow`) owns published guidance blocks, workflows, packs, library
policies and system bindings. Adoption owns each project's selections, installed
rules and revision history.

Workflows live in the library and projects select them
([decision](../decisions/workflows-as-lanes.md)). Publishing a new version does
not update a project's recorded adoption.

A project is on one or more packs, its memberships (`project_packs`), and
gets their union: every pack's guidance, workflows, policies, tools,
services, extensions and requirements. No pack outranks another, and packs
do not include one another; what several packs share is a pack of its own
(stock `server-base`). Where two packs disagree on something a project has
one of (a block's or workflow's pin, a tool's version line, the toolchain,
a service of the same name, a policy of the same name, starter files), the
combination is refused and the words name both packs (`wow.Combine`, and
`adoption.Prepare` for the pins). Every door that adopts packs prepares
through that one rule. An adoption receipt names its packs (`packs`, sorted,
the platform pack included), and its memberships are written in the same
transaction. A receipt recorded before projects took several packs keeps the
JSON it was recorded with; its single `pack` key is not read.

An explicit pack pull moves only that pack's membership to its latest
publication, in the same transaction as lane selections and the adoption
revision; the project's other memberships keep their versions and
actor/time/reason. The pull is judged with the packs it leaves the project
on, as any adoption is, and never deselects a lane another of the project's
packs names. It reports guidance for a person to adopt separately.

Every pack version stores its pinned contents beside the declaration. Each
pack has one recommended version: what new projects get, what applying the pack installs
and sets up, and what projects on other versions are offered. Saving never
moves it, a release's stock versions included, so a what-if can judge a
version before projects are offered it; a person does
(`standards:manage-packs`, no agent credential). Adopting a version that drops a
workflow or replaces a policy keeps the project's existing ones and says so;
removing them is a project edit. A selected workflow no pack of the
candidate pins keeps the version the project's receipt pins, unless the
request asks to advance workflows (`adoption.Request.AdvanceWorkflows`),
which a what-if records and its adopt door repeats. A what-if may also name
other pack versions (`WhatIfRequest.Also`), moved with the named one for
projects already on them and never added to a project; they are recorded,
eligibility counts projects below any of them, and the adopt door repeats
them (`adoption.PicksWith`). A candidate lists every
rule whose version it changes (`Candidate.Moves`); an older version or a rule
no longer carried moves back, which is allowed and is said in the
confirmation, the Change's brief and the revision's reason. Adopting
(`adoption.OperationSelect`) and applying a pack take
`project:select-way-of-working`. A what-if judges a version's checks on
projects' main into Conformance's own tables, never standing, and an eval
re-runs real Changes under the version in throwaway copies. A copy takes
exactly a receipt its source recorded (`Candidate.CopyOf`): it is checked
against that receipt, not against its packs' pins, because a project
advanced to the newest block versions keeps its pack memberships and no
longer carries those pins. Only an eval's copy takes a way of working that
way. Stock
sync publishes available versions; projects change through adoption operations.
A running workflow retains its prepared definition and receipt rather than reading
new library content as it advances.

## Where stock comes from

A release carries its stock as two update files beside its binaries, in
`share/apptown/updates/` (`internal/stockupdate`): a ways-of-working update
(workflows, guidance, policies, packs, the change catalog, starters,
toolchain files, and the scanner and need definitions, which change only
with a release) and a tool versions update (`stock/tool-versions.yaml`).
`scripts/stock-files.go` builds both from `stock/`, named with the build's
version. Each says the AppTown versions it works with (`worksWith`); a
release refuses one outside the range, and a maintainer's `dev-` build,
which has no version to place, validates it and logs that the range was not
judged. `apptownd` reads its stock from the ways-of-working update at start
and `apptown-worker` its runtime images from the tool versions update;
neither starts without them, and neither has a compiled-in copy (`make
check-stock-embed`). AppTown's own documentation (`stock/docs`) is part of
the binary. Tests may read `stock/` directly.

Error-reporting guidance is not in any pack: it reaches a project when it
attaches the errors integration, as the generated section of its agents'
steering (`agentenv`).

A pack names the version line of each tool it prescribes (`tools: {node:
"24", postgres: "16"}`); the tool versions list says what each line means
exactly, and a pack may name only lines the list has. A proposed service's
image tag is on its tool's line. Changing a line is a new version of the
pack. The runtime images AppTown runs for itself (the shell guidance checks
run in, df, the static server, the private Docker daemon) are read from the
list, by digest; the private Docker image is release-bound, and an
attempt with private Docker records the digest the worker measured. The list has one owner per process (`internal/toolversions`,
`Versions`), filled at start from the shipped update; owners that run these
images take it, and each unit of work (a check, a guidance step, a review,
a what-if, a boot, a release build, a preview, a backup, restore or
rehearsal, a cache pass, a disk admission) takes one snapshot at its start
and uses it throughout, so a newer list never mixes images within one unit.
The private Docker image is read from the start snapshot only. A guidance
step's evidence names the snapshot (`toolVersions`) beside its shell image.

Every published workflow, block and pack version records where it came
from as two fields: its **source** and its **signer**. Stock comes from the
built-in source, `flokkit`, signed `shipped with AppTown <version>,
unsigned` (this release does not sign its shipped updates); the instance's
own versions are source `here`, with no signer; stock published before the
record began is signed `shipped with AppTown before <build>, unsigned`,
since the build that published it was never recorded.

## Adopting changes

Adoption resolves a candidate from specific catalog versions, checks conflicts
and required project capabilities, and records the selected revision. Direct
adoption and adoption through an uplift Change are separate operations. Uplift
records its target through Workflow's transaction participant so the target and
new Change cannot be admitted independently. Opening an uplift Change starts its working run
(quick-change, else implement, the lane remediation uses); a run admitted
and then failed is reported as that run, and a refusal before any run says
why, while the Change stands. An adoption whose new checks
already hold needs no code: the Change's working step passes empty with its
account, a person accepts it, and the landing's conformance gate judges the
target's acceptance checks on the tree before the landing moves the revision.
The person opening it may accept it in advance, on the condition AppTown
witnesses that it needs no code and every step and check passes outright
([landing acceptance](../architecture/work-landing-acceptance.md)).

Pack application can include repository and project setup. The Projectlifecycle
facade coordinates that work; a catalog publication alone does not perform it.
Current-edit commands own complete metadata/version updates and required
persistence rather than having handlers compose partial writes.

## Evidence at recorded versions

Conformance reads the declarations at their recorded versions. A base/head
comparison distinguishes introduced findings from existing ones; publishing
stricter guidance alone must not silently rewrite an earlier verdict. Exceptions,
required evidence and adoption history remain separate records.

## Implementation references

- [internal/apptown/wow/service.go](../../internal/apptown/wow/service.go)
- [internal/apptown/wow/combine.go](../../internal/apptown/wow/combine.go)
- [internal/apptown/wow/pack_resolution.go](../../internal/apptown/wow/pack_resolution.go)
- [internal/apptown/wow/pack_recommendation.go](../../internal/apptown/wow/pack_recommendation.go)
- [internal/apptown/conformance/what_if.go](../../internal/apptown/conformance/what_if.go)
- [internal/apptown/verification/what_if.go](../../internal/apptown/verification/what_if.go)
- [internal/apptown/adoption/service.go](../../internal/apptown/adoption/service.go)
- [internal/apptown/adoption/adopt_transaction.go](../../internal/apptown/adoption/adopt_transaction.go)
- [internal/apptown/adoption/target.go](../../internal/apptown/adoption/target.go)
- [internal/apptown/projectlifecycle/packs.go](../../internal/apptown/projectlifecycle/packs.go)
