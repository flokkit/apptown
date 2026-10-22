# Versioned ways of working

Ways of Working (`wow`) owns published guidance blocks, workflows, packs, library
policies and system bindings. Adoption owns each project's selections, installed
rules and revision history.

Workflows live in the library and projects select them
([decision](../decisions/workflows-as-lanes.md)). Publishing a new version does
not update a project's recorded adoption.

An explicit pack pull records its captured selected publication as the primary
membership in the same transaction as lane selections, primary pack identity and
adoption revision. Replacing a primary removes its previous membership; platform
and supplemental memberships retain their original versions and actor/time/reason.
The pull continues to report guidance for a person to adopt separately. Execution
reads the recorded primary pin, never a later publication as a repair or fallback.

A pack may include another pack (`includes`, with `replaces`, `drops` and
`clears`) and add its own items. Every version stores the resolved contents
and pins beside the declaration and the exact publication it included;
inherited items keep that publication's pins whatever the library publishes
since, and a library move cuts only the pack's own items. Each pack has one
recommended version: what new projects get, what applying the pack installs
and sets up, and what projects on other versions are offered. Saving never
moves it, a release's stock versions included, so a what-if can judge a
version before projects are offered it; a person does
(`standards:manage-packs`, no agent credential). Moving it cuts an unrecommended follow version
of each pack that includes it without a pin. Adopting a version that drops a
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
- [internal/apptown/wow/pack_inclusion.go](../../internal/apptown/wow/pack_inclusion.go)
- [internal/apptown/wow/pack_recommendation.go](../../internal/apptown/wow/pack_recommendation.go)
- [internal/apptown/conformance/what_if.go](../../internal/apptown/conformance/what_if.go)
- [internal/apptown/verification/what_if.go](../../internal/apptown/verification/what_if.go)
- [internal/apptown/wow/catalog_edit.go](../../internal/apptown/wow/catalog_edit.go)
- [internal/apptown/adoption/service.go](../../internal/apptown/adoption/service.go)
- [internal/apptown/adoption/adopt_transaction.go](../../internal/apptown/adoption/adopt_transaction.go)
- [internal/apptown/adoption/target.go](../../internal/apptown/adoption/target.go)
- [internal/apptown/projectlifecycle/packs.go](../../internal/apptown/projectlifecycle/packs.go)
