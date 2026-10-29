# Project addresses and request results

A project UUID identifies its repository, work, deployments and ownership.
Its address (`projects.slug`) names it in links, API paths, Git URLs and
`slug#N` references. A project has one address at a time: changing it moves
the project, and the old address names nothing afterwards.

An address is taken while a project has it as its address or as its serving
label, or while a purged project's unfinished cleanup still names it. The
serving label is the project's first address, captured at creation and never
changed (see [ingress](ingress.md)); so a project's first address stays with
it until it is purged, and any later address is free again once the project
moves off it. Every lookup by address reads the current address; readers that
saved a project's identity earlier (a chat, a queued backup or rebuild) use
its UUID, never the address they saw.

Address edits use the settings administrator admission and the expected
settings revision. The new address, the canonical settings, the revision and
the `project.renamed` receipt commit together; an edit whose receipt cannot be
written changes nothing. A changed address must have the address shape.

Renaming does not move repositories, change upstream identity, rewrite task input,
recreate deployments or change their serving labels. Links, Git remotes, checkout
directories and `slug#N` references made with the old address stop resolving, and
name the new owner if another project later takes the address. A rename during a
preview, import or deployment can refuse that in-flight operation when its observed
address changes; retry the operation after the rename completes.

## Creating a project

A creation request is scoped to the verified actor UUID and a client request UUID.
Its fingerprint includes the normalized declaration, automatic-address choice,
requested and resolved owning group, selected pack and the typed setup intent:
source, branch, credential name, bundle digest, service selections and upstream
attachment choice. Missing services and an explicitly empty selection differ;
service ordering does not. No credential values or bundle bytes are stored in the
request result.

The creation transaction reserves a UUID and checks the address before writing
the project, initial adoption, ownership, audit receipt and original result
snapshot together. An automatic proposal that is taken chooses a suffix from that
reserved UUID once; an explicit address that is taken is refused. Two different
requests racing for the same free address can see the loser refused; it retries.
Failed transactions retain no claim.
A completed retry is resolved before fresh-only pack availability and default-group
selection; it keeps its original group and checks current project read permission
and active lifecycle. The response is the original committed result. A client that
continues setup reads the project again by the result's address and compares its
UUID: if the project has moved since, the address is not found or names another
project, and the client stops.

Repository initialization/import, applying a selected pack, selecting services,
upstream attachment and first-version creation remain separate operations in their
existing owners. The local creation receipt does not prove those effects happened
or make them safe to repeat after an unknown response. Legacy explicit creation
without a request UUID remains a single creation operation with no replay promise.
