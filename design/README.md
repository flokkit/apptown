# Forge documentation

Start with the [project README](../README.md) to build and run Forge. Then read,
in order:

1. [How the forge works](../stock/docs/guides/architecture.md), the overview
   shipped on the product's Docs page;
2. [the product model](subsystems/product-model.md);
3. [package ownership](architecture/domains.md);
4. the [code navigation guide](../internal/forge/README.md).

You should not need historical plans to understand the current system.

## Current reading path

| Question | Primary reference |
| --- | --- |
| How does the whole forge fit together? | [How the forge works](../stock/docs/guides/architecture.md) |
| What are the objects, and how do they relate? | [Product model](subsystems/product-model.md) |
| Where does work execute? | [Workers: what a worker isolates](operations/workers.md#what-a-worker-isolates-and-what-it-does-not), [execution](../internal/execution/README.md), [worker](../internal/worker/README.md) and [agent isolation limits](subsystems/agent-sandbox.md) |
| Where does code go, and what may it depend on? | [Ownership map](architecture/domains.md) and [layout convention](architecture/layout.md) |
| How do I change tables and queries? | [Schema ownership](architecture/schema-ownership.md) |
| How do I build and test? | [Repository README](../README.md), [test guidance](architecture/testing.md), Makefile and CI |
| Who may do what? | [Authentication and authority](subsystems/auth.md) |
| How does work start, finish, retry and recover? | [Workflow](subsystems/workflow-domain.md), [Jobs](subsystems/jobs.md) and [daemon lifecycle](subsystems/daemon-lifecycle.md) |
| What do chats and agent steps receive? | [Console ownership](subsystems/console.md) and [runtime environment guide](../stock/docs/guides/runtime-environments.md) |
| How are models selected and usage recorded? | [Model package](../internal/forge/model/doc.go) and [Usage package](../internal/forge/usage/doc.go) |
| How are reports about Forge handled? | [Complaint package](../internal/forge/complaint/doc.go) |
| How are model credentials contained? | [Inference security](subsystems/inference-security.md) |
| What do checks prove, and how do findings become work? | [Quality](subsystems/quality.md) |
| How do integrations and secrets reach consumers? | [Integration and secret runtime](subsystems/integration-runtime.md) |
| How do I package and deploy an app? | [Native contract](../stock/docs/guides/contract-reference.md), [toolchains](../stock/docs/guides/toolchains.md) and [deployment runtime](subsystems/deployment.md) |
| What does a deployment own and remove? | [Removing a deployment](subsystems/deployment.md#removing-a-deployment), [deployment targets](subsystems/deploy-targets.md) and [deleting a project](subsystems/project-deletion.md) |
| How do I operate and observe Forge? | [Operations](operations/README.md) and [telemetry](subsystems/telemetry.md) |


The architecture pages explain cross-package rules. Package comments (`doc.go`)
own a package's concepts, call sequence and invariants; exported API comments
state authorization, effects, failure and concurrency contracts. Package READMEs
can provide file navigation and contributor commands without repeating those
contracts. Subsystem guides explain behaviour spanning several packages. Operator
guides own commands and configuration. Remove a subsystem guide when its useful
content is fully covered by package documentation, and update links to that owner.

For example, from the repository root:

```sh
go doc ./internal/forge/model
go doc ./internal/forge/model.Scheduler.Acquire
go doc ./internal/forge/usage
go doc ./internal/forge/complaint
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
| `decisions/` | The reason for a choice and what explicitly supersedes it |
| `operations/` | Operating navigation and specific procedures |
| Package `README.md` | Contributor navigation, principal call sequences and tests |
| `stock/` | Shipped content, templates and importable artifacts with their usage instructions |


[Requirements](requirements/requirements.md), [concepts](requirements/concepts.md),
[key journeys](requirements/key-use-cases.md) and [unsupported scenarios](requirements/not-yet.md)
retain product intent. Their old “live” statements are dated observations, not
proof of today's deployment. [Architecture](architecture/architecture.md) retains
accepted direction and illustrative sketches; it is not an exported Go API list.
Where code and intent differ, document the gap rather than silently changing the
promise. Later accepted decisions name what they supersede.


Makefile and CI define executable build/test commands. Plans, reviews and
experiments are kept in the development repository and are not published
with the public tree; a guide's mention of one names that history.

## When implementing a change

Update the primary guide when a behaviour or contract changes. Update requirements
or decisions when accepted intent changes, and operating instructions when setup
or rollout changes. Append deployment evidence only when observed. Internal
changes that invalidate no documented claim need no new prose.


Write current guides in plain technical language: name the owning package,
describe the operation and its failure behaviour, and link to implementation.
Distinguish an implemented feature from an accepted requirement or a proposal.
Keep author conversations, rhetorical summaries and rollout chronology in the
dated plan or review. A checked date records a source review, not a deployment.
