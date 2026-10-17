# Versioned ways of working

Status: current implementation. Checked on 18 September 2026.

Ways of Working (`wow`) owns published guidance blocks, workflows, packs, library
policies and system bindings. Adoption owns each project's selections, installed
rules and revision history.

Publishing a new version does not update a project's recorded adoption.

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
(`standards:manage-packs`, no agent credential). The one exception is a
compatibility migration that amends the recommended publication
(`Packs.Amend`, the private-Docker declaration): the recommendation moves to
the amended copy. Moving it cuts an unrecommended follow version
of each pack that includes it without a pin. Adopting a version that drops a
workflow or replaces a policy keeps the project's existing ones and says so;
removing them is a project edit. A what-if judges a version's checks on
projects' main into Conformance's own tables, never standing, and an eval
re-runs real Changes under the version in throwaway copies. Stock
sync publishes available versions; projects change through adoption operations.
A running workflow retains its prepared definition and receipt rather than reading
new library content as it advances.

## Adopting changes

Adoption resolves a candidate from specific catalog versions, checks conflicts
and required project capabilities, and records the selected revision. Direct
adoption and adoption through an uplift Change are separate operations. Uplift
records its target through Workflow's transaction participant so the target and
new Change cannot be admitted independently. An adoption whose new checks
already hold needs no code: the Change's working step passes empty with its
account, a person accepts it, and the landing's conformance gate judges the
target's acceptance checks on the tree before the landing moves the revision.

Pack application can include repository and project setup. The Projectlifecycle
facade coordinates that work; a catalog publication alone does not perform it.
Current-edit commands own complete metadata/version updates and required
persistence rather than having handlers compose partial writes.

## Historical evidence

Conformance reads the declarations at their recorded versions. A base/head
comparison distinguishes introduced findings from existing ones; publishing
stricter guidance alone must not silently rewrite an earlier verdict. Exceptions,
required evidence and adoption history remain separate records.

The earlier design contains broader assurance and migration proposals. They do
not add runtime compatibility readers or unsupported policy states.

## Implementation references

- [internal/forge/wow/service.go](../../internal/forge/wow/service.go)
- [internal/forge/wow/pack_inclusion.go](../../internal/forge/wow/pack_inclusion.go)
- [internal/forge/wow/pack_recommendation.go](../../internal/forge/wow/pack_recommendation.go)
- [internal/forge/conformance/what_if.go](../../internal/forge/conformance/what_if.go)
- [internal/forge/verification/what_if.go](../../internal/forge/verification/what_if.go)
- [internal/forge/wow/catalog_edit.go](../../internal/forge/wow/catalog_edit.go)
- [internal/forge/adoption/service.go](../../internal/forge/adoption/service.go)
- [internal/forge/adoption/adopt_transaction.go](../../internal/forge/adoption/adopt_transaction.go)
- [internal/forge/adoption/target.go](../../internal/forge/adoption/target.go)
- [internal/forge/projectlifecycle/packs.go](../../internal/forge/projectlifecycle/packs.go)
