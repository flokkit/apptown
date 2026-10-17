# Working on Deployment

Read the [package documentation](doc.go) for the model, authority and transaction
contracts, or run `go doc ./internal/forge/deployment` from the repository root.
The [operating guide](../../../design/subsystems/deployment.md) covers source
selection, uncertain outcomes and stale dashboard observations.

| Change you are making | Start here |
| --- | --- |
| Manual release request | [request.go](request.go) → [request_execution.go](request_execution.go) |
| Preparation, attempt admission and execution | [deploy.go](deploy.go) → [execution.go](execution.go) → [settlement.go](settlement.go) |
| Release settlement | [settlement_release.go](settlement_release.go), [settlement_inputs.go](settlement_inputs.go) |
| Interrupted execution | [startup.go](startup.go) → [recover.go](recover.go) → [recovery.go](recovery.go) |
| Selected Docker source and rebuild | [docker_runtime.go](docker_runtime.go), [jobs.go](jobs.go) |
| Reaching Docker daemons (this forge's own, attached hosts over ssh) | [hosts.go](hosts.go): `Hosts` |
| What a boot is given: secrets, needs and registered keys, registry credentials, addresses and the eval rule | [environment.go](environment.go): `Environment`; Deployment's key recorder is in [registered_needs.go](registered_needs.go) |
| Driver binding and target support | [providers.go](providers.go): `Providers` builds drivers and inventories and checks targets; [target_providers.go](target_providers.go), [target.go](target.go) |
| Dashboard observations and health enforcement | [observations.go](observations.go), [health.go](health.go) |
| Durable job adapter | [jobs.go](jobs.go); retry budgets are bound in [job_wiring.go](../job_wiring.go) |
| Release bundles | [bundles.go](bundles.go): a landing's deploy step attempt commits its bundle in Work's ownership fence, once per attempt; a digest bundle names its images by digest in one registry, its platform and where it was built (published by [internal/oci](../../oci/oci.go)) |
| Releases built on the worker | [release_publication.go](release_publication.go): `PlanRelease` names each distinct image once, as `part-<part>` or `init-<step>`; `PublishRelease` publishes the worker's exports and copies image-only declarations into the project's container registry, checking before each image that the deploy step attempt owns its step, and commits the digest bundle from the registry's read-back. Images are tagged `change-<change>-<image>`, and a release that goes live adds `release-<n>-<image>` to the same manifests (`tagRelease`): names for people and retention rules, never evidence, and a tag that fails never fails a release. Docker hosts (landing, promotion, recovery) pull a digest bundle by digest, once their daemon says it runs the bundle's platform (`refusesPlatform`, the one rule App Platform and Portainer use too), with the registry's credential in a per-pull Docker configuration and confirm its RepoDigests; a digest the registry no longer has is refused, and nothing is built. App Platform and Portainer receive a digest bundle by promotion only: App Platform's renderer copies each image by digest into DOCR (the digest kept, confirmed by DOCR) and names it in the spec; an intranet release ships the latest digest bundle with every part and init step by digest, after the endpoint names its platform. A published release's preview boots its digests where changes' previews boot (the [preview](../preview/doc.go) package) |
| Data protection's participants: a take-down's settlement, a restored copy's placement, a start from a copy | [removal_settlement.go](removal_settlement.go) (`SelectRemovalIn`, `RemoveRecordedIn`, `RemoveRestoredIn`), [placements.go](placements.go) (`PlaceRestoredCopy`, `ExtendRestoredCopyIn`), [start_from.go](start_from.go) (`CopyStarter`, the choice and `RecordStartedFrom`), [holds.go](holds.go) (the shared hold paths); copies, restores, rehearsals, restored copies and take-down orchestration are the [dataprotection](../dataprotection/doc.go) package's |
| A forge with no local Docker (`NoLocalDocker`) | [no_local_docker.go](no_local_docker.go): a landing deploys to an attached Docker host only; a bundle of image IDs names the target its images were read from and is reused there, never rebuilt; one that names none is refused; a digest bundle goes by promotion to any attached Docker host, App Platform and Portainer, never the built-in host; only a digest bundle's release is previewed (the preview package's rule) |

JobWorker decodes durable release, recovery and rebuild requests and calls
the owning operations. It does not own River's lifecycle. Backup, backup-sweep
and restored-copy-sweep jobs are data protection's; database backup and
instance inventory caching are assembled by Forge outside this package.

Tests live beside their owners. Begin with [request tests](request_test.go),
[settlement transaction tests](release_outcome_transactions_test.go),
[source/image settlement tests](source_image_settlement_test.go) and
[recovery tests](recovery_test.go) when changing those guarantees. Keep provider
protocol tests with their driver and assembled behaviour tests with Forge.
