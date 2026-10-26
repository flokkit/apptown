# Project addresses and request results

A project UUID identifies its repository, work, deployments and ownership.
Its canonical address (`projects.slug`) names current links. `project_addresses`
permanently assigns every current and historical address to that UUID; purging a
project never frees an address for another project. Ambiguous historical deletion
names remain reserved without assigning them to an invented owner. Lookup joins
the reservation to an existing project, so a purged address does not resolve.

Address edits use the existing settings administrator admission and expected
settings revision. They require an authenticated actor UUID and request UUID.
The address claim, canonical settings, revision, audit receipt and result snapshot
commit together. Retrying the same actor/request and input returns its original
result after current authority and lifecycle checks; different input is refused.
The old address continues to resolve to current canonical metadata. Git credentials
match the resolved project UUID; Work and ADR address filters resolve to UUID before
selecting records. Addresses never become alternate authority scopes.

Renaming does not move repositories, change upstream identity, rewrite task input,
recreate deployments or change their serving labels. Built-in application addresses
retain their initial serving label. Previous project links and references remain
usable while the project exists. A rename during a preview, import or deployment
can refuse that in-flight operation when its observed address changes; retry the
operation after the rename completes.

## Creating a project

A creation request is scoped to the verified actor UUID and a client request UUID.
Its fingerprint includes the normalized declaration, automatic-address choice,
requested and resolved owning group, selected pack and the typed setup intent:
source, branch, credential name, bundle digest, service selections and upstream
attachment choice. Missing services and an explicitly empty selection differ;
service ordering does not. No credential values or bundle bytes are stored in the
request result.

The creation transaction reserves a UUID and address and writes the project,
initial adoption, ownership, audit receipt and original result snapshot together.
An automatic proposal collision chooses a suffix from that reserved UUID once;
an explicit address collision is refused. Failed transactions retain no claim.
A completed retry is resolved before fresh-only pack availability and default-group
selection; it keeps its original group and checks current project read permission
and active lifecycle. The response is the original committed result. Clients read
its permanent alias to obtain the current canonical address before continuing.

Repository initialization/import, applying a selected pack, selecting services,
upstream attachment and first-version creation remain separate operations in their
existing owners. The local creation receipt does not prove those effects happened
or make them safe to repeat after an unknown response. Legacy explicit creation
without a request UUID remains a single creation operation with no replay promise.
