# Current package ownership

AppTown is assembled in [App.New](../../internal/apptown/app.go). HTTP handlers in
`httpapi` and tools in `mcpapi` translate requests into operations on concrete
owners. They do not acquire unrestricted access to App through a replacement
interface. Root retains authentication, routing, startup and cross-owner wiring.

## Where code belongs

- `internal/<name>` is a product-independent library: access policy evaluation,
  process execution, Docker/cloud drivers, the task worker, console mechanics, database and telemetry.
- `internal/apptown/<name>` is AppTown-specific, including table-owning domains,
  application composition and transport. Owning tables is not a prerequisite.
- `internal/apptown` assembles these parts. It still contains protocol handlers and
  some application composition; its presence is not evidence that an extraction
  is unfinished. Do not use a target of zero App methods.

[Layout](layout.md) owns the detailed placement and naming rules. The
[shape checker](../../scripts/shape-check.py) enforces selected import, SQL and
transport boundaries; its registrations and counts are not the full domain model.

## Owners to follow

| Question | Principal owners |
| --- | --- |
| Who may act? | `internal/access` evaluates policy; `identity` owns accounts, credentials and grants; operation owners apply domain eligibility |
| What is this project? | `project` (including where it stands: active, being imported or deleted, deleted and kept); `projectlifecycle` composes creation, initialization, settings, the deletion pass, restore and the purge, and runs a purged project's cleanup |
| Which guidance and workflow apply? | `wow` owns library versions; `adoption` records selected versions and quarantines drift found at boot (`VerifyRevision`); `workcommands` prepares admitted inputs |
| What work exists and how does it change state? | `workflow` owns Changes, Runs, Steps, claims, decisions, evidence and settlement, answers what a step attempt's token may push (`StepPush`), and owns the narrative writer's prompt, evidence layout and parser; `changeadds` reads what an approval's commit declares it adds; `changeactions` opens Changes from catalog entries, chats and due schedules (`Schedules.FireDueSchedules`) |
| Who executes a step? | `workflowexec` owns agent/review, command, shipping, repository and child-call runners; `verification` owns scan/guidance policy; `tasks` records a step attempt's task before it starts so a restart adopts it; `workflowexec.NotContinued` names the step kinds a restart ends |
| How are agent environments and conversations prepared? | `agentenv`, `conversation`, and the Pi adapter in `agentharness/pi`; generic mechanics remain in `internal/console` and `internal/execution` |
| What becomes of a change's chat's work? | `changeconsole` saves a chat on the worker after each turn and at its end, ends chats and publishes their work, rehearses their boot, chooses the chat reports a change's narrative reads, opens chats on the worker and lists a change's worker chats; `repositoryedit` performs each save; `conversation` owns opening and who may act on a chat |
| Where do model credentials and usage live? | `inference` resolves providers and serves the gateway; `secret` stores secrets; `usage` records accounting; `model` owns model slots and capacity |
| How is an application deployed? | `deployment` owns placements (a preview's placement row among them), releases, attempts and generation guards, the targets seeded at boot, and whether attached Docker hosts answer; its `Hosts`, `Environment` and `Providers` reach daemons, prepare what a boot is given and build drivers, for every owner that boots; drivers under `internal/deploy` perform external effects; `appcontract` represents the application contract |
| Is a deployment's data copied, can it come back, and may it be deleted? | `dataprotection` owns ordinary and final copies, managed copies and the backup store with its pruning and purge, in-place restore, drills, sealed-host rehearsals, restored copies behind AppTown's gate, and take-down orchestration; Deployment settles the placements and retained resources these touch through its participants, and is given data protection's fill of a deployment started from a copy at construction |
| How is a whole AppTown backed up and restored? | `instancebackup` composes them and owns no table: a backup (each built-in deployment's set from `dataprotection`, the built-in database's dump the composition root makes, the data directory, an encrypted archive), its brief hold of new jobs and steps, the control socket `apptownd backup` asks through, a restore before AppTown first starts, and what the restored records say the first start does (`deployment` holds deployments; `dataprotection` relocates sets and records fills) |
| How is a change, run or release previewed? | `preview` owns admitting one boot at a time per preview, booting it here or on the worker, what each preview serves, opening one for probing, discarding and reconciling them, clearing the worker's at start-up, and previews' AI models keys (`preview_owned`); it reads Deployment's records and collaborators and writes Deployment's tables only through their operations; `workflow.Layout` supplies ordinary change workspace locations while Preview's Sources port retains checked source resolution and pinning |
| How is work checked and repaired? | `conformance` owns observations; `verification` runs checks and tests a scanner's image; `remediation` owns repair jobs/items/attempts |
| Is a model profile or a pack version better or worse? | `evals` owns evals, their arms and the copy record (which eval and arm made each throwaway copy); it drives arms through Work and makes copies through `projectlifecycle` with Project's eval-copy purpose, which every effect owner asks Project about |
| How does a long action someone asked for run? | `operations` owns the ledger (one row per copy, drill, rehearsal, restored copy or take-down: who asked with which credential, the paths it holds, its steps and how it ended), admission with its job, the `operations` queue worker and restart settlement; owners register kinds and admit them under their own locks; `identity.RequireStill` answers whether the credential that asked still stands |
| How do background effects run? | `jobs` wraps River delivery; each effect owner handles its operation; `maintenance` owns periodic cleanup policy and what local work and execution containers this host still holds (the drain's questions) |
| How do repositories and integrations work? | `scm` owns durable Git effects; `repositoryedit` composes repository edits; `integration` owns bindings, the seed bindings and capability names, and whether a model binding's credentials resolve; `dependency` owns the dependency catalogue |
| Project addresses in ADR filters | `adrs` resolves addresses through `project.IDByAddressIn`; it owns ADR selection and preserves the existing visibility filter |
| Other recorded product facts | `audit`, `adrs` and `complaint` own their respective records |

A package can expose several focused operations or services. This map is not a
one-service-per-table prescription. For exact table ownership consult
[schema ownership](schema-ownership.md) and owner SQL, not a copied table list.

## Following an operation

Start with [the root guide](../../internal/apptown/README.md), then a package guide:
[Workflow](../../internal/apptown/workflow/README.md),
[work commands](../../internal/apptown/workcommands/README.md),
[execution](../../internal/apptown/workflowexec/README.md),
[Deployment](../../internal/apptown/deployment/README.md),
[SCM](../../internal/apptown/scm/README.md),
[Inference](../../internal/apptown/inference/README.md), or
[maintenance](../../internal/apptown/maintenance/README.md).

Operations verify caller authority before effects. Owners control their own
records and legal transitions. Named SQL queries live with the owner and generated
SQL packages are not application APIs. Cross-owner transaction participants join
only the writes that really must commit together; Git, Docker and provider calls
are not made atomic by surrounding database writes.

For tests, follow the operation in its owner. Root tests protect assembly and
cross-owner guarantees. Independent persistence assertions and narrowly justified
fault injection remain useful; moving SQL behind a fixture wrapper is not a
substitute for testing through the correct owner.

## Principles

1. **A subsystem is one type, built once, with its dependencies declared.** Its
   constructor takes a dependencies struct whose fields are small interfaces the
   subsystem defines for itself. It names no other subsystem's package except
   through those, or where a direct import is stable and obvious.
2. **Bounded acts take their inputs; things worked over time take a source.** A
   deploy or a console session gets everything it needs from its caller. Work
   acts on a repository for weeks, so it declares a source it calls back into.
3. **Objects own their rules; services load, call, save and record.** A command
   on a value checks its invariants and changes it; the service wraps that in a
   transaction with the row read for update and the receipt written.
4. **Authorization happens inside the operation.** Transports establish the
   caller; domains ask the shared authorizer, naming the capability and the full
   resource, before effects. Domain eligibility stays with its owner. A missing
   caller never becomes system authority.
5. **Receipts are part of the operation.** A consequential command writes its
   receipt in the same transaction, and a receipt that fails fails the command.
   Operations that must share a transaction have an `…In(tx, …)` form.
6. **A facade is a complete operation across subsystems.** It imports its
   participants, owns the ordering of transactions and effects, and owns tables
   only when it has durable state of its own.
7. **Cross-cutting concerns are supplied, not imported from a hub.** Database,
   receipts, jobs, execution, models, secrets, configuration and logging reach a
   subsystem as a package it imports or a value it is constructed with.
8. **Uneven by design.** The shape follows the domain rather than a template.

Project package-cache names, discovery and precise retirement belong to
`agentenv`. `projectlifecycle` calls that owner with the retained deletion's
project UUID; `maintenance` asks it for current cache names before pruning closed
work's toolchain entries. These owner-directed imports do not authorize either
consumer to infer cache ownership from a slug.
