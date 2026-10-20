# What chats and workflow steps are provided

This is the runtime contract for Forge's own
helpers and workflow execution, supporting requirements C2–C6 and C26.
It describes the behaviour to preserve when changing the implementation.
The [runtime guide](../../stock/docs/guides/runtime-environments.md) holds the
capability table, paths, commands and current exceptions. The application's
own deployment contract is a separate concern.

## Supported guarantees

**RE1 — Workspace authority follows the task.** A project chat receives a
read-only snapshot and writable session scratch space. A change chat receives
that change's writable worktree. A workflow agent receives the workspace chosen
for its run. Giving a helper tools does not grant approval, administration or
permission to bypass the landing workflow. An external agent connected through
MCP uses its own runtime; connecting does not provision a Forge console for it.

**RE2 — The standard container supplies general development tools.** Forge's
standard agent image supports shell work, Git, code search, browser checks,
package management and installation of project-declared toolchains. Projects
declare required tool versions in their repository. An installed Docker client
does not imply a daemon, and a game project does not imply that its engine is
already installed. Custom step images and local execution have their own tool
availability; they do not inherit the standard image's inventory.

**RE3 — Agent configuration is explicit.** Containerized helpers receive
generated model configuration and project steering, without copying the
operator's personal agent home. Project skills and approved extensions load
through the configured trust policy. Workflow agent steering and execution
evidence are recorded; chat conversations are retained. This does not promise
a complete, immutable inventory of every executable installed during a session.

**RE4 — Private Docker has an explicit eligibility and failure policy.** With
private Docker enabled, writable change chats receive a daemon belonging to
their session. Docker-executed agent steps requesting the implementation skill
are eligible for a temporary daemon. Read-only project chats and other step
types receive no automatic daemon. The administrator can disable this feature.
A new or upgraded change console must prepare its daemon and builder before
starting with Docker access. Step provisioning currently permits degraded
execution and records a Docker or builder setup failure in step progress.

**RE5 — Services and credentials are supplied by purpose.** Declared step
services and requested application needs are provisioned for Docker steps;
previews and deployments provision their own contract-declared needs. Chats do
not receive those services automatically. Helpers receive their Forge identity,
model access and the additional credentials granted to their execution path.
Private Docker alone grants neither production secrets nor registry push access.
The guide must explain the actual grants, including differences between chats
and steps, instead of describing a shared, unrestricted environment.

**RE6 — Nested execution has a usable, documented connection model.** When
private Docker is available, the helper and daemon see the same workspace path
for bind mounts. Published ports have a documented address reachable from the
helper. Forge configures the daemon's DNS and bound image mirror. Nested builds
and containers require explicit environment inputs; they do not automatically
inherit the helper's service connections or proxy settings. Package mirrors
route traffic; they are not an enforced network allowlist.

**RE7 — Persistence is described by resource.** Chat records and change files
are distinct from disposable processes, container filesystems and nested Docker
state. A Forge restart preserves console containers and their private daemons.
Idle stopping retains their files and Docker state; session end or idle reaping
can remove that runtime state. Reopening reconstructs missing resources from
the session record. Step containers and step daemons are temporary. A cached
tool or image is an optimisation, never the only copy of a required input.

**RE8 — Recovery respects active work and bounded shutdown.** An existing
console acquires changed Docker access when stopped or after its agent hands
back the turn. Recovery validates sidecar ownership and the mounted workspace
before adopting it. Console Docker control operations have deadlines and
observe Forge shutdown cancellation; shutdown does not wait for the session's
jobs to finish. The shared daemon shutdown budget remains bounded. This is not
a guarantee that every older external operation is individually canceled.

**RE9 — Local success is evidence, not deployment authority.** A helper may
install a tool, build an image or run a local experiment within its workspace
permissions. A preview still builds and boots from the application's declared
recipe independently. Landing and deployment retain their own checks and
permissions; warm session state is not a deployable dependency.

## Verification and remaining gaps

Verify these guarantees through behaviour: denied writes to a read-only
workspace, a real image build and run, a shared-file bind mount, a reachable
published port, restart recovery, cleanup, cancellation and credential scope.
Use the ordinary tool to verify a toolchain (`godot --version`, a headless test,
or the repository's documented build). Do not substitute checks for particular
headings or phrases for evidence that the environment works.

Existing evidence includes the console DinD unit and Docker integration tests,
console steering/scope tests, process-runner tests, agent configuration tests
and shutdown tests. This is a verification map, not a claim that all toolchains
or all runtime combinations are covered.

There is no unified capability/readiness receipt today. Toolchain preparation
is best-effort, the Forge CLI can be unavailable when its packaged artifact is missing or invalid, and
step Docker setup can degrade after steering was generated. A future change
should expose those facts to the helper consistently and decide which missing
capabilities must block execution. Those are improvements to design and
implement, not guarantees this document declares complete. Strong egress
containment also remains [future work](../subsystems/agent-sandbox.md).

## Proposed extension: development services for working sessions

Proposal, not an implemented guarantee. Change chats and workflow steps that
build or exercise the application should be able to request its declared
development services through one consistent provisioning mechanism.

| Consumer | Proposed default |
|---|---|
| Change chat | Provision on demand from the app's `needs`; retain the development instance through the working session |
| Implementation or integration-test step | Provision requested needs before execution; fresh data by default |
| Project chat or review-only step | No services unless a task explicitly requires and is permitted to use them |
| Preview or boot gate | Its own fresh instances, independent of chat and step state |

The application contract and dependency catalog remain the source of service
definitions. A step declares whether it needs services; a label such as
"implementation" should not silently start every possible database. Existing
`needs: true` and configured step sidecars supply much of the step mechanism.
The missing part is an equivalent, managed lifecycle for change chats and a
consistent way to report availability to every consumer.

Each consumer owns its instances and data, with development credentials and
connection settings delivered only after readiness is established. Share the
provisioning implementation, not a live database across chats, steps and
previews. Production credentials are a separate grant. Failure must say which
required service is unavailable and prevent the dependent operation from
starting; an optional service must be identified as optional.

For chats, provide start, status, explicit reset and stop operations. Reuse data
within the session, stop idle services, and tie eventual cleanup to a documented
session policy. Starting, reconnecting or changing a service declaration must
not silently reset data. A changed declaration needs an explicit reconciliation
outcome: compatible reuse, migration, or a reset/recreation decision. Services
must also participate in quotas, orphan recovery and bounded shutdown.

Verification should run the same small app against services in a change chat
and an eligible step, confirm that connection settings work, prove their data
is separate from each other and from a preview, and exercise readiness failure,
reset, idle resume, restart recovery and cleanup. Stronger provisioning should
reduce setup work without allowing warm chat state to satisfy a cold-boot gate.

## Implementation references

- [Agent image](../../deploy/apptown-agent/Dockerfile),
  [toolchain and mirror setup](../../internal/forge/agentenv/registries.go).
- [Chat provisioning](../../internal/console/pi/provision.go),
  [console DinD](../../internal/console/pi/dind.go),
  [console lifecycle](../subsystems/console.md).
- [Agent eligibility, credentials and services](../../internal/forge/workflowexec/environment.go),
  [step DinD](../../internal/forge/agentenv/workshop.go),
  [process execution](../../internal/execution/runner.go).
- [Console permissions](../../internal/forge/console.go),
  [step secrets](../../internal/forge/secret/runtime.go) (`StepEnv`),
  [bounded shutdown](../subsystems/daemon-lifecycle.md).

## RE10. Model access without provider credentials

Model agents and their delegates must receive Forge session credentials rather
than reusable upstream provider keys. Chat credentials last with the chat,
including idle/restart; step credentials last with their current attempt. A
continued chat has its own credential and retains run attribution. Inference
requires an explicitly allowed worker-network peer as well as the credential.
Source checks and token validation fail closed. The server chooses endpoints,
permitted models and trusted usage attribution. Local model execution that can
read the server's credentials is not a supported isolation boundary.

This first batch does not require hard monetary budgets or short-lived renewable
leases. Attributable inference within configured models is the accepted minimum;
budget enforcement remains follow-up security work. The implemented limits,
residual risks and rollout requirements are in
[inference security](../subsystems/inference-security.md).

## RE11. Work on a separate machine

A change's chat runs on the worker whenever one is configured, as do script,
agent, review and tests steps, checks, previews and release builds; the
[workers guide](../operations/workers.md) says which work runs where.

**The model.** A worker is another machine that runs a project's code so that
it does not run on the forge's host. It shares no files with the forge. Work
reaches the forge only as commits, which the forge checks as it checks any
other publication; a chat's work reaches its change when it is **saved**,
meaning the forge has put the commit on the change's branch. If a worker is
lost, what it had not saved is lost with it.

A person should be able to rely on that model without knowing the mechanism.
The guarantees below hold for work on a worker as for work on the forge:

- **One holder per change.** Only one chat or step writes a change at a time,
  wherever it runs.
- **Only checked commits reach the branch.** A worker never writes a change's
  branch; the forge collects its commits and they pass the same publication
  checks as a desktop push, and a refused save is shown with its reason and, for a chat that ends,
  kept as a download rather than published.
- **Release only on proof.** A change held by work on a worker is released
  only when the worker confirms that work is gone. A worker that cannot be
  reached keeps holding.
- **Unknown is not nothing.** Work whose state cannot be read is shown as
  unavailable, never as stopped, finished or clean. Unsaved work is shown as
  not saved.

**Accepted failures.** These are stated so people can plan around them, not
defects to engineer away:

- a worker whose disk is lost loses everything since its last save;
- a message to a chat whose delivery cannot be confirmed may or may not have
  arrived, and the person decides whether to send it again;
- a worker that is gone for good holds its changes until retiring workers is
  designed; a person can still abandon a change;
- if the forge loses a chat's own record, the chat's leftovers on the worker
  are removed when the forge next starts: the forge asks the worker to remove
  any chat it has no record of, and the worker removes what that chat left.

**Not promised.** A worker does not make the forge safe for untrusted projects
while other kinds of work still run on the forge's host; which work runs where
is in the [workers guide](../operations/workers.md). Worker chats do not yet
have previews, automatic rehearsal or `apptown check`; these are planned to run
from the saved commit. A chat that needs a registry the worker cannot reach
is refused when it opens, with the registry named.
