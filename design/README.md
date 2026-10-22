# AppTown documentation

Start with the [project README](../README.md) to build and run AppTown. Then read,
in order:

1. [How AppTown works](../stock/docs/guides/architecture.md), the overview
   shipped on the product's Docs page;
2. [the product model](subsystems/product-model.md);
3. [package ownership](architecture/domains.md);
4. the [code navigation guide](../internal/apptown/README.md).

You should not need a plan to understand the current system.

## Current reading path

| Question | Primary reference |
| --- | --- |
| How does the whole AppTown fit together? | [How AppTown works](../stock/docs/guides/architecture.md) |
| What are the objects, and how do they relate? | [Product model](subsystems/product-model.md) |
| Where does work execute? | [Workers: what a worker isolates](operations/workers.md#what-a-worker-isolates-and-what-it-does-not), [execution](../internal/execution/README.md), [worker](../internal/worker/README.md) and [agent isolation limits](subsystems/agent-sandbox.md) |
| Where does code go, and what may it depend on? | [Ownership map](architecture/domains.md) and [layout convention](architecture/layout.md) |
| How do I change tables and queries? | [Schema ownership](architecture/schema-ownership.md) |
| How do I build and test? | [Repository README](../README.md), [test guidance](architecture/testing.md), Makefile and CI |
| Who may do what? | [Authentication and authority](subsystems/auth.md) |
| How does work start, finish, retry and recover? | [Workflow](subsystems/workflow-domain.md), [Jobs](subsystems/jobs.md) and [daemon lifecycle](subsystems/daemon-lifecycle.md) |
| What do chats and agent steps receive? | [Console ownership](subsystems/console.md) and [runtime environment guide](../stock/docs/guides/runtime-environments.md) |
| How are models selected and usage recorded? | [Model package](../internal/apptown/model/doc.go) and [Usage package](../internal/apptown/usage/doc.go) |
| How are reports about AppTown handled? | [Complaint package](../internal/apptown/complaint/doc.go) |
| How are model credentials contained? | [Inference security](subsystems/inference-security.md) |
| What do checks prove, and how do findings become work? | [Quality](subsystems/quality.md) |
| How do integrations and secrets reach consumers? | [Integration and secret runtime](subsystems/integration-runtime.md) |
| How do I package and deploy an app? | [Native contract](../stock/docs/guides/contract-reference.md), [toolchains](../stock/docs/guides/toolchains.md) and [deployment runtime](subsystems/deployment.md) |
| What does a deployment own and remove? | [Removing a deployment](subsystems/deployment.md#removing-a-deployment), [deployment targets](subsystems/deploy-targets.md) and [deleting a project](subsystems/project-deletion.md) |
| How do I operate and observe AppTown? | [Operations](operations/README.md) and [telemetry](subsystems/telemetry.md) |
| How should text people read be written? | [How AppTown writes](writing.md) |

The architecture pages explain cross-package rules. Package comments (`doc.go`)
own a package's concepts, call sequence and invariants; exported API comments
state authorization, effects, failure and concurrency contracts. Package READMEs
can provide file navigation and contributor commands without repeating those
contracts. Subsystem guides explain behaviour spanning several packages. Operator
guides own commands and configuration. Remove a subsystem guide when its useful
content is fully covered by package documentation, and update links to that owner.

For example, from the repository root:

```sh
go doc ./internal/apptown/model
go doc ./internal/apptown/model.Scheduler.Acquire
go doc ./internal/apptown/usage
go doc ./internal/apptown/complaint
```

Keep the documentation proportional to the subject. Describe the normal call
sequence as well as important failure cases. A fixed page length is not a goal;
a contributor should not need a historical design to understand current rules.

## Tree and authority

| Location | What belongs here |
| --- | --- |
| `requirements/` | Product promises, use cases and vocabulary; implementation gaps stay explicit |
| `architecture/` | Cross-system rules, boundaries and ownership; adopted decisions distinguished from sketches |
| `subsystems/` | Current subsystem behaviour and explicit implementation gaps |
| `decisions/` | A choice and why it holds |
| `operations/` | Operating navigation and specific procedures |
| Package `README.md` | Contributor navigation, principal call sequences and tests |
| `stock/` | Shipped content, templates and importable artifacts with their usage instructions |

[Requirements](requirements/requirements.md), [concepts](requirements/concepts.md),
[key journeys](requirements/key-use-cases.md) and [unsupported scenarios](requirements/not-yet.md)
state product intent. Where code and intent differ, document the gap rather than
silently changing the promise.

Makefile and CI define executable build/test commands. The maintainers' plans
and reviews are kept in their own private repository, not here; a current
guide never cites one.

## When implementing a change

Update the primary guide when a behaviour or contract changes. Update requirements
or decisions when accepted intent changes, and operating instructions when setup
or rollout changes. Append deployment evidence only when observed. Internal
changes that invalidate no documented claim need no new prose.

A current guide states the current rule in the present tense. When behaviour
changes, rewrite the guide to the new rule: no dates, no "previously", no
"superseded by", no status header; git history records what changed when. A
reason that matters is written into the guide, not left in a plan. Unbuilt
work is said plainly ("not built: …") or listed in
[unsupported scenarios](requirements/not-yet.md); a proposal lives in a plan
until it is built.

Write current guides in plain technical language: name the owning package,
describe the operation and its failure behaviour, and link to implementation.
Distinguish an implemented feature from an accepted requirement. Keep author
conversations and rollout chronology in plans and reviews.
