# Forge threat register

Each entry names a threat, its attack and impact, its status, the control
that answers it and how that control would be verified. A proposed control or
verification scenario is not a claim that anything is built. Read with the
[threat model](threat-model.md), which defines scope, priorities and residual
trust. For identity and visibility behaviour, read
[authentication and authority](../subsystems/auth.md).

The **Status** column of the inventory, and the status line at the top of each
entry, rest only on a current guide (under `design/subsystems`,
`design/architecture`, `design/operations` or `stock/docs/guides`) or a test
in the code:

- **Mitigated**: a guide or test shows the proposed control, or an equivalent
  one, for the paths the entry names.
- **Partly mitigated**: part of the control is shown; the status line says
  what remains.
- **Accepted**: the remaining risk is a recorded choice; the status line says
  where it is recorded.
- **Open**: no guide or test shows the control. An entry whose current state
  cannot be shown is open.

A status is about the product, not about any installation. It is not a
penetration test, and it does not verify a deployment's own firewall, edge
proxy, provider limits or backups. Several entries are partly one status and
partly another; their status line says which part is which.

STRIDE: S = spoofing, T = tampering, R = repudiation, I = information
disclosure, D = denial of service, E = elevation of privilege. Ratings are
qualitative for known users with untrusted workloads. “High confidence” means
the enabling behavior is clear in source, not that a live exploit was run.

## Inventory

| ID | Threat | Priority | STRIDE | Evidence confidence | Status |
|---|---|---|---|---|---|
| TM-01 | Reusable model-provider keys escape runtimes | P0 | I, E | High | Mitigated |
| TM-02 | Model use has no enforceable shared spending ceiling | P0 | D | High | Open |
| TM-03 | Workloads compromise the privileged execution host | P0 | E, I, T | High boundary concern; exploit not demonstrated | Partly mitigated; root daemon accepted |
| TM-04 | Untrusted Git metadata influences host commands | P0 | E, T | Medium; reachable metadata varies by workspace | Partly mitigated |
| TM-05 | Workspace paths/symlinks cross host filesystem boundaries | P0 | I, T, E | High unsafe primitive; individual paths need tests | Partly mitigated |
| TM-06 | Shared writable cache enables cross-workload poisoning | P1 | T, E, I | High | Partly mitigated |
| TM-07 | Workloads or server requests reach private infrastructure | P1 | I, E | Medium; live reachability unverified | Open |
| TM-08 | Active attachments execute with Forge web authority | P0 | S, E, I | High source evidence; browser test outstanding | Mitigated |
| TM-09 | Cross-origin requests exercise a victim's session | P0 | S, E | High missing controls; browser/edge dependent | Mitigated |
| TM-10 | Authentication and revocation differ across doors | P1 | S, E | High mixed paths; matrix incomplete | Partly mitigated |
| TM-11 | Broad reads violate assumed project privacy | P1 decision | I | High; confidentiality intent unresolved | Partly mitigated |
| TM-12 | Bootstrap, login or proxy configuration grants authority | P1 | S, E, D | High source evidence; deployment dependent | Partly mitigated |
| TM-13 | Stolen instrument credentials or apptownd reports exceed purpose | P1 | S, T, E | High credentials; per-action bounds need review | Partly mitigated |
| TM-14 | Untrusted development/release code receives valuable secrets | P1; P0 if bindings reuse production | I, E | High delivery; live bindings unverified | Partly mitigated |
| TM-15 | Secrets persist in files, images, logs or Git | P1 | I | High | Partly mitigated |
| TM-16 | Database/backup compromise reveals secrets and authority | P1 | I, T | High | Accepted for the built-in store; backups open |
| TM-17 | Workflow/check content substitutes for trusted decisions | P1 | T, E | Medium; scenario dependent | Partly mitigated |
| TM-18 | Approved/tested identity differs from released artifact | P1 | T, E | Medium; release path dependent | Partly mitigated |
| TM-19 | Toolchain or dependency supply chain introduces malicious code | P1 | T, E | High exposure; no specific compromise alleged | Partly mitigated |
| TM-20 | Work/API traffic exhausts shared resources | P1 | D | High missing uniform limits; edge unknown | Partly mitigated |
| TM-21 | Webhook secret reuse/replay creates unwanted work | P1 | S, D | High | Partly mitigated; shared token accepted for 1.0 |
| TM-22 | Development services leak data or become infrastructure footholds | P1 | I, T, E, D | Medium; service/path dependent | Partly mitigated |
| TM-23 | Deployment integrations expose broader target authority | P1 | E, I, T | Medium; live credentials unverified | Open |
| TM-24 | Stop/restart leaves work or authority alive | P1 | D, E | High lifecycle concern; failure paths need tests | Partly mitigated |
| TM-25 | Cleanup or restore destroys unrelated/valid state | P1 | T, D | Medium; race/archive fixtures needed | Partly mitigated |
| TM-26 | Mutable/self-reported evidence misleads audit and accounting | P2 | R, T | High trust limitation | Partly mitigated |
| TM-27 | External clients exceed consent or confuse protocol identity | P1 | S, E, I | Existing strong controls; remaining gaps identified | Partly mitigated |
| TM-28 | Hosted apps inherit unintended exposure or trust | P1 | I, S, D | High routing behavior; exposure policy unresolved | Partly mitigated |

## Credentials and execution

### TM-01 — reusable model-provider keys escape runtimes

**Status: mitigated.** Provider keys stay on AppTown.
Chats, steps and their delegates get only a Forge credential for the private
inference listener, local model execution is refused, and a known provider
key passed back in through a step secret or an integration setting stops the
runtime's preparation ([inference security](../subsystems/inference-security.md);
`TestInferenceAdmissionAndCredentialBoundary` and the other tests in
`internal/forge/inference/gateway_test.go`). A provider key ever delivered into
a runtime must still be rotated. Spending through the gateway is TM-02.

**Attack and impact.** A package script, malicious tool, persuaded agent or
person with shell access reads provider keys and exports them. The attacker
can spend against the provider independently of Forge or its session lifetime.
No container escape is required.

**Proposed control.** Trusted inference gateway with reusable keys outside
the worker trust boundary, grant-only runtime configuration, no direct-key
fallback and no arbitrary upstream forwarding. Cover console, step,
worker/scout delegation, custom provider JSON and local execution. Rotate any
key ever delivered to a runtime.

**Verify.** Seed distinct fake keys for multiple providers. Inspect the env,
mounts, generated config, child processes and observable output of every
runtime class. None may contain an upstream key. Allowed inference succeeds
through the gateway; direct provider authentication and alternate model/config
paths cannot recover the old behavior.

### TM-02 — inference spending can exceed the intended allowance

**Status: open.** The gateway limits a request to 32 MiB
and allows eight concurrent requests per runtime credential and 64 per daemon;
that is not a monetary budget, and new sessions get new capacity
([inference security](../subsystems/inference-security.md), "Provider
operations and limits"). AppTown sets no spending limit; the operating guide
says to set one with the model provider
([running an AppTown](../../stock/docs/guides/running-apptown.md), "Configure
model access").

**Attack and impact.** A user, stolen grant or looping agent runs parallel
requests, forks delegates or opens many sessions. A post-hoc cost dashboard
cannot prevent the charge. Stolen provider keys bypass it entirely.

**What exists.** Configured model allowlists, request and concurrency limits and
transport-observed usage receipts. Attributable inference is otherwise
unbounded; there is no monetary budget.

**Proposed control.** Reserve maximum request cost transactionally at the
gateway, across replicas, using parent/delegate and user/project/instance
ceilings. Restrict models, request/output size, rate and concurrency. Reconcile
trusted usage conservatively across disconnects/retries; use provider account
limits as a second layer. Policy failure must deny new spending.

**Verify.** Concurrent requests racing the last allowance cannot overspend the
reserved ceiling. New sessions/delegates do not reset it. Exercise failed
settlement, gateway restart, provider timeouts, replay and model/pricing
changes. Confirm a revoked grant cannot start another request and document
the maximum already-in-flight exposure.

### TM-03 — privileged execution compromises the control plane

**Status: partly mitigated; the root daemon is accepted.**
With a worker, steps, checks, previews, release builds and new change chats
run on a separate machine; with `-no-local-docker` AppTown's host runs no
Docker and no project process; a script step runs as a host process only with
`-host-scripts` ([agent isolation limits](../subsystems/agent-sandbox.md);
[workers](../operations/workers.md), "What a worker isolates, and what it does
not"). Remaining: private Docker is still a privileged container; without
`-no-local-docker`, project chats, continued chats and chats placed on the
AppTown still run on its host; a worker moves code off AppTown's host but
does not contain it there. Running `apptownd` as root on Linux is a documented
choice ([running an AppTown](../../stock/docs/guides/running-apptown.md),
"Prepare the host"; `deploy/systemd/apptownd.service`), so code that escapes to
AppTown's host has root there.

**Attack and impact.** Malicious workload code uses local execution directly
or attacks a privileged container/kernel boundary to reach AppTown host.
Host compromise exposes its database, model keys and deployment authority.

**Proposed control.** Separate control plane/secrets from worker hosts or
VMs; make the untrusted execution contract explicit. Do not permit local
backend selection for untrusted work. Run workloads with the least privilege
their class needs; evaluate rootless/build-service alternatives to privileged
DinD. A non-root Forge user in the host Docker group is still effectively
Docker-host privileged and is not sufficient separation.

**Verify.** Inventory runtime user, capabilities, mounts and host interfaces.
Workers cannot access control-plane files, daemon sockets, DB or secret store.
Use disposable isolation fixtures and measured network policy; retain a
separate review of the chosen kernel/VM boundary.

### TM-04 — attacker-controlled Git metadata drives host execution

**Status: partly mitigated.** Before the apptown runs Git in
a change's or run's folder that an agent or person may have changed, it checks
the metadata by reading files only (a real `.git` directory, no symbolic
links, no shared or borrowed metadata, only the config its own clone writes,
no hooks but Git's samples) and refuses the folder otherwise; Git then runs
with no system or global config, hooks and fsmonitor off
([folder.go](../../internal/gitexec/folder.go); `internal/gitexec/folder_test.go`
refuses hooks, filter drivers, diff helpers, `core.fsmonitor`,
`core.worktree`, `include.path`, a `.git` link, linked objects or refs,
`commondir` and `alternates`). Every Git command keeps to the repository's own
config and drops inherited `GIT_*` variables (`gitexec.Environ`,
`internal/gitexec/environ_test.go`). Remaining: not every host Git command
over a project's tree goes through the folder check; for example staging
(`scm.StageAll`) and build-source selection (`StagedBuildContext` in
`internal/deploy/docker/app.go`) use the repository's own config, filters
included. Whether each such tree is writable by project code was not
established in this pass.

**Attack and impact.** Code changes Git configuration/hooks or replaces the
workspace's Git indirection before Forge runs status, add, stash, archive or
commit. Git may invoke configured helpers, filters or hooks on the host.
Potential impact is host execution under the daemon identity.

**Proposed control.** Keep trusted repository metadata out of workload control;
perform untrusted Git operations in a credential-free sandbox. Centralize
sanitized environment/config/protocol/helper handling and validate worktree
identity. Disabling hooks alone does not address filters, external diff,
credential helpers and other configurable execution paths.

**Verify.** Harmless marker-command fixtures for hooks, filters, replacement
`.git`, config includes and hostile environment must never execute in the
control plane. Cover normal step finalization, preview builds and recovery,
not just project import.

### TM-05 — paths and symlinks cross the workspace boundary

**Status: partly mitigated.** Deployment builds and static
output refuse symlinks and paths that leave the source tree
(`internal/deploy/docker/source_confinement_test.go`,
`internal/deploy/docker/static_build_test.go`); image exports and runner
results refuse links out of the tree (`internal/execution/images_test.go`,
`internal/execution/runner_test.go`); the doors that serve workspace files
open them through `os.Root` (`internal/forge/httpapi/workspaces.go`); chat
upload reads are confined to the upload directory
([inference security](../subsystems/inference-security.md)). Remaining:
attachment synchronisation still writes `.apptown/attachments` into the
worktree with ordinary joined paths (`SyncAttachments`), and `secret.ReadSpec`
reads `secretspec.toml` by a joined path.

**Attack and impact.** A writable workspace supplies `..` paths or swaps a
symlink before a privileged host read/write/mount. Files outside the intended
root may be disclosed, modified or served. Race conditions can defeat a
check performed well before the actual open.

**Proposed control.** Validate declarative paths centrally and use root-bound
filesystem operations that resist symlink traversal at use time. Keep runtime
credentials outside writable source trees; stage immutable build inputs.
Reject unsafe mount sources and prevent untrusted paths selecting host roots.

**Verify.** Test relative escapes, symlinked intermediate directories,
replacement races, absolute-looking paths and archive links against disposable
canary files outside the workspace. Test host writes as well as reads and
mounts. Expected denial must not alter the outside canary.

### TM-06 — writable cache crosses workload trust boundaries

**Status: partly mitigated.** Each project's steps and
checks mount a cache volume of their own, `forge-cache-<project>`, and each
chat has its own ([running an AppTown](../../stock/docs/guides/running-apptown.md),
"Workers and running without local Docker"; `TestAStepsCacheIsItsProjectsAlone`
in `internal/forge/agentenv`). Remaining: a worker's mise download cache is
one folder writable by every container on that worker, so one project's
container can leave files another project's reads
([workers](../operations/workers.md), "The tool cache").

**Attack and impact.** One project edits another workspace's tool installation
or shared executable/cache configuration. A later workload consumes it and
executes attacker code with its own credentials.

**Proposed control.** Mount only a workload/project-owned cache root, selected
according to the confidentiality policy. Shared artifacts should be immutable
and verified, with a trusted publisher. Separate caches for more privileged
release consumers. Clear suspect caches during incident recovery.

**Verify.** Project A cannot read/write B's cache from its mount namespace.
Poisoned-cache fixtures cannot influence B's executable resolution. Confirm
performance optimizations do not reintroduce a shared writable parent.

### TM-07 — network access permits private-infrastructure pivots and SSRF

**Status: open.** The inference listener admits only
configured worker networks, and a worker can check that the cloud metadata
address is blocked (`apptown-worker -check-metadata`), but the block itself is
the operator's firewall rule ([running an AppTown](../../stock/docs/guides/running-apptown.md)).
No guide or test shows an enforced egress policy for workloads (the
[agent isolation limits](../subsystems/agent-sandbox.md) list it as not
implemented) or server-side validation of integration destinations and
redirects.

**Attack and impact.** Workload code contacts host/LAN services directly; or
an authorized configuration field persuades Forge to make a credential-bearing
HTTP request to an unintended destination. Redirects and DNS changes can
defeat naive URL checks. Consequences include credential disclosure and
access to metadata, management or other project services.

**Proposed control.** Separate worker networks and enforce allowed control
endpoints plus purpose-specific egress. Validate server-side destinations,
redirects and resolved addresses, including IPv6; keep upstream credentials
bound to approved endpoints. Block cloud metadata and management networks
unless a narrowly defined integration explicitly needs them. Controlled
egress reduces exfiltration routes but does not make secrets safe in a runtime.

**Verify.** From each runtime class, measure access to another project, both
API ports, host Docker, DB, secret store and metadata canaries. Test URL
redirects/DNS changes with local fake servers, checking that auth headers never
reach the disallowed destination.

## Browser and authorization

### TM-08 — active attachments execute at AppTown origin

**Status: mitigated.** Change attachments, chat uploads
and chat history files are served as downloads (`application/octet-stream`,
`Content-Disposition: attachment`) with `nosniff` and a `sandbox` CSP; only
content that is really a picture is shown inline
([untrusted_file.go](../../internal/forge/httpapi/untrusted_file.go);
`TestUntrustedFileResponses` in `internal/forge/browser_security_test.go`).
The optional browser tier (`TestBrowserSecurityChromium`, build tag `browser`)
uploads a script disguised as a picture and checks in Chromium that it does
not act at AppTown origin. Other raw-content routes were not reviewed in
this pass.

**Attack and impact.** A contributor uploads HTML or active SVG and induces a
more privileged person to open it. If rendered at AppTown origin, script
can make authenticated requests as that viewer. HttpOnly prevents reading
the cookie, not using ambient session authority.

**Proposed control.** Serve arbitrary attachments as downloads with safe
Content-Disposition and `nosniff`, or from a separate untrusted-content
origin. Allow inline display only for deliberately supported inert formats;
active formats need isolation. Apply the same policy to artifacts and reports.

**Verify.** Browser fixture uploads active HTML/SVG and opens each rendering
route; no script gains Forge-origin API authority. Test misleading extensions,
MIME sniffing, redirects and download filenames. Preserve ordinary image/PDF
workflows through safe rendering.

### TM-09 — cross-origin requests borrow a victim's authority

**Status: mitigated.** Unsafe methods require the exact
configured origin when a browser sends one; sibling application domains are
not trusted; the console WebSocket requires the origin; HTTPS sign-in uses a
`__Host-` cookie with `Secure`; OAuth consent is a short-lived server record
bound to person, browser and request and consumed once
([browser security](../subsystems/browser-security.md);
`TestBrowserMutationOrigins`, `TestBrowserForgeryDoesNotMutate`,
`TestTerminalRejectsOriginBeforeAttachment`, `TestProductionBrowserCookie`,
and the Chromium test against a same-site sibling and an unrelated origin).
Putting applications on a different registrable domain from Forge remains an
operator's choice (TM-28).

**Attack and impact.** A hostile site, especially a sibling preview on the
same registrable domain, sends browser-authenticated requests or opens a
terminal WebSocket. A apptownd OAuth consent POST can authorize an attacker-owned
client whose PKCE challenge the attacker chose.

**Proposed control.** Exact trusted-origin checks for WebSocket handshakes;
CSRF protection for browser mutations and session-bound, expiring, one-use
consent state. Production `Secure` host-only cookies, preferably `__Host-`
names with a deliberate local-HTTP development mode. Frame protection for
consent/admin pages. Separate app and Forge registrable domains where possible.

**Verify.** Browser tests from unrelated and same-site sibling origins must
deny credentialed terminal connections, apptownd consent and state-changing
requests. Valid UI/API/OAuth clients still work. Test absent/null origins
according to the actual client contract; do not indiscriminately exempt them.

### TM-10 — authority and revocation differ by authentication path

**Status: partly mitigated.** Disabling an account ends its
sessions and stops sign-in, and wherever authority is decided a name without
an enabled account is refused ([authentication and authority](../subsystems/auth.md);
`TestDisabledAccountCannotSignInOrUseSession`). A chat terminal opened to
type asks again before each keystroke or resize, so a person made a viewer
while their page is open only watches ([console](../subsystems/console.md);
`TestATerminalStopsTakingInputWhenItsTypistLosesTheRole`). Remaining: the complete
transport-by-principal matrix has not been shown; verifying a credential at
the transport does not cancel a command it already admitted; personal tokens
have no expiry (auth guide).

**Attack and impact.** A disabled/demoted user's old cookie, console or open
terminal retains access that a fresh personal-token request would lose.
Different route guards give different answers for equivalent actions.

**Proposed control.** Normalize principals and check current account/delegation
state before every authorization path. Make legacy and kernel routes converge;
route inventory is coverage, not proof of enforcement. On revocation close
active terminals and invalidate instruments, using bounded background cleanup.

**Verify.** Disable/demote/revoke during an active cookie, personal-token,
console, run and OAuth session. Each permitted read/write follows the same
current policy; established PTYs lose input authority. Test both listeners
and delegated-person conversion, not only direct user principals.

### TM-11 — project privacy is assumed where reads are broad

**Status: partly mitigated.** The confidentiality decision
is made: reads follow the caller's current read grants on each project;
aggregate reads select readable projects before limits and counts; reads
about the instance keep an instance capability; a refusal does not name the
project ([authorization decisions](../subsystems/authorization-capabilities.md);
`checkMemberScopedReads` in `internal/forge/member_views_http_test.go`). An
instance-wide read grant (the Auditor role) reads every project by design.
Remaining: this pass did not show the same filtering on every representation
the entry lists (Git, MCP, events, error metadata).

**Attack and impact.** A member or instrument reads other projects' source,
prompts, transcripts, attachments, evidence or operational metadata. The loss
is disclosure if people believed rosters made those resources private.

**Proposed control.** Decide confidentiality by data category and document it
in the UI. If private projects are promised, authorize list and object reads,
search, downloads, events, Git, MCP and error metadata consistently. Instruments
should receive only the information their purpose needs.

**Verify.** Cross-project tests cover every representation and transport,
including identifier guessing and list filtering. For intentional shared
reads, test that secrets and security-sensitive runtime data remain excluded.

### TM-12 — bootstrap, login and proxy trust are attack surfaces

**Status: partly mitigated.** Under internal sign-in the
first account needs the setup token AppTown writes to its data directory,
and concurrent bootstrap has one winner (`TestTheFirstAccountNeedsTheSetupToken`,
`TestBootstrapAdministratorRollsBackAndHasOneWinner`); the doors that take a
secret share a per-client rate limit; open sign-in refuses a listener other
machines can reach unless `-open-on-network` is given
([authentication and authority](../subsystems/auth.md); [running a
forge](../../stock/docs/guides/running-apptown.md)). Remaining: an empty
administrator-network list still means anywhere (`internal/forge/adminnet.go`);
stripping identity and forwarding headers at the edge is the operator's job;
there is no second factor.

**Attack and impact.** An exposed uninitialized instance is claimed by an
attacker; concurrent bootstrap requests create extra administrators; password
guessing consumes bcrypt CPU; proxy/header mistakes permit identity spoofing.

**Proposed control.** One-use out-of-band/bootstrap-local authority plus atomic
owner creation; explicit production auth configuration. Login/registration
limits and administrative reauthentication or MFA through the chosen identity
system. Strip client identity headers at the edge and restrict direct access
to proxy-trusted listeners. Empty admin-network configuration should be clear.

**Verify.** Race two initial owners, exercise already-bootstrapped restarts,
spoof forwarding headers from trusted/untrusted peers, and bound login CPU.
Test direct-listener access rather than only the public proxy route.

### TM-13 — runtime tokens or reports are used outside their purpose

**Status: partly mitigated.** A run credential is accepted
only while its run is running and its attempt is the step's live attempt
([inference security](../subsystems/inference-security.md); the credential
table in the [threat model](threat-model.md)); a chat's credential reaches its
own chat and no other ([console](../subsystems/console.md);
`TestAChatsCredentialActsOnlyOnItsOwnChat`); agents, consoles and step runs
cannot approve or land ([authorization decisions](../subsystems/authorization-capabilities.md)).
Remaining: every process in a runtime holds the same bearer; run credentials
have no independent expiry, and whoever holds AppTown's `runtime-token-key`
can make them; a runtime's report is still a claim unless a check witnessed
the result.

**Attack and impact.** Code steals its console/run token and uses the main
listener or another transport, submits a apptownd completion report, or keeps
acting after the intended turn. Its legitimate token must not make arbitrary
claims into trusted outcomes.

**Proposed control.** Narrow grants to resource, action, audience, expiry and
current lease. Treat reports as claims until correlated with trusted runner
state and immutable inputs. Enforce bounds on every listener; separate the
trusted agent/control reporter from project child processes where needed.

**Verify.** Use A's token against B's resource, the main port, PTY, privileged
approval and secret routes. Replay completion after cancellation/restart;
it must not advance an unrelated/current attempt. Expiry must work even if
resource cleanup fails.

## Application secrets, evidence and delivery

### TM-14 — development or changed code receives production authority

**Status: partly mitigated.** Values are bound per
deployment; with scopes, each part of an app receives only the secrets its
scope lists; a preview uses the `preview` profile when one is declared; a
restore rehearsal gets only what a non-inheriting `rehearsal` profile names
([secrets](../../stock/docs/guides/secrets.md)); a tests step gets throwaway
generated secrets and no credentials
(`TestATestsStepGetsThrowawayGeneratedSecretsAndNoCredentials`); each preview
gets its own need credentials and empty data
([services and data](../../stock/docs/guides/services-and-data.md)).
Remaining: no secret classification keeps production-class values out of
previews and chats by policy; what a preview or step receives still depends on
the project's bindings and explicit step secrets.

**Attack and impact.** A preview or step gets a real application/SCM/registry
credential through a reused binding, broad integration or secret profile.
Changed code sends it away or uses it against production.

**Proposed control.** Classify secrets as control-plane, production-runtime or
development; deny high-value classes in chats/tests/previews by policy. Use
unique sandbox accounts and data. A repository declaration may request a
secret name, but must not grant itself a binding or broaden its class.
Require explicit governed exceptions for cross-environment sharing.

**Verify.** Canary production secrets never appear in preview, review or
development processes. Missing preview profiles/bindings fail according to
declared policy. Test changed consumer declarations and integration attachments,
including unapproved init/migration code.

### TM-15 — secrets escape through retained output and build artifacts

**Status: partly mitigated.** Recorded step output,
evidence and chat messages replace current secret-store values with `[secret]`
([secrets](../../stock/docs/guides/secrets.md), "What is masked, and what is
not"); build-source selection now refuses rather than falling back to the live
worktree, and excludes ignored and untracked files
(`StagedBuildContext`; `internal/deploy/docker/staged_build_test.go`);
landings get a history scan ([workers](../operations/workers.md)). Remaining:
masking misses encoded or split values; the agent's own session files and a
chat's live terminal keep what it saw (secrets guide); deliberate copying is
not prevented.

**Attack and impact.** A tool prints env, a model copies a config file, or a
build includes runtime state. Values persist in transcripts, images, downloads,
Git history or backups after the runtime ends.

**Proposed control.** Remove valuable secrets from workloads first. Fail closed
when constructing protected build contexts; keep runtime state outside source.
Redact known credentials at logging boundaries, use standard secret scanning,
restrict raw transcript access, and define retention/deletion across stores.
Redaction is defense in depth, not arbitrary exfiltration prevention.

**Verify.** Canary secrets in ignored/runtime files do not enter build context,
Git staging, image layers or normal logs, including staging failures. Exercise
intentional copying to document what scanning can detect and what isolation
must prevent. Validate deletion and upstream rotation independently.

### TM-16 — database and backup access reveals instance credentials

**Status: accepted for the built-in secret store; backups
open.** The built-in store keeps its key in the same database and is
documented as not a vault: anyone with a dump or backup can read every value,
and the guide says to use Infisical for anything that matters
([secrets](../../stock/docs/guides/secrets.md); [integration
runtime](../subsystems/integration-runtime.md)). Need credentials are derived
from the same key ([services and data](../../stock/docs/guides/services-and-data.md)).
Open: AppTown's nightly dumps and its data directory (which holds
`secrets.env`, `runtime-token-key` and attached hosts' SSH keys) are not
encrypted by Forge, and protecting them is left to the operator
([running an AppTown](../../stock/docs/guides/running-apptown.md), "AppTown's
own backups"); no guide describes invalidating sessions or tokens restored
from a backup.

**Attack and impact.** A leaked DB dump, backup or machine identity exposes
application secrets and the ability to AppTown grants/history. A restored backup
can also revive credentials or vulnerable configuration.

**Proposed control.** Separate encryption/unlock authority from backup data,
restrict DB credentials/network, encrypt and inventory backups, and keep
recoverable off-host copies under different access authority. Define retention
and key recovery. Invalidate restored session/grant state before reopening.

**Verify.** A stolen backup alone cannot decrypt protected secret values under
the selected design. Perform an isolated restore with key-loss, key-rotation
and revoked-session scenarios; verify recovery without silently restoring old
credentials. Record recovery objectives and measured results.

### TM-17 — editable workflows and checks become a confused deputy

**Status: partly mitigated.** Merging into the default
branch and deploying need a person's approval or a run admitted by someone
holding `change:land`, checked at the effect itself; agents, consoles, step
runs and AppTown's triggers cannot approve or land
([authorization decisions](../subsystems/authorization-capabilities.md);
`internal/forge/workflow/assent.go`). An optional check worker keeps checks
off the machine agents use, and a tests step answers only from a witnessed
exit ([agent isolation limits](../subsystems/agent-sandbox.md);
[workers](../operations/workers.md)). Remaining: project-controlled tests and
checks still decide what they test; a root administrator may approve their
own change.

**Attack and impact.** Someone changes a workflow, check definition, repository
test or agent report so dangerous work appears approved. Prompt injection
persuades an agent to call a privileged tool. A check that tests itself can be
green without demonstrating the intended property.

**Proposed control.** Keep policy authorship/adoption and consequential
approval distinct from producing code/evidence. Bind checks to immutable
definitions and inputs; label self-reported versus runner-observed evidence.
Prompt guidance explains intent; deterministic gates enforce authority and
required evidence, not mandatory prose formatting.

**Verify.** A contributor cannot relax its governing requirement and then
self-certify a release. Replay stale approval/report after source/policy
change; required re-evaluation happens. A malicious agent tool call receives
the same denial as a direct API caller.

### TM-18 — released bits differ from reviewed or tested bits

**Status: partly mitigated.** On an AppTown with a worker,
a landing's images are built once from the landed commit and recorded by
digest in a bundle that never changes; every later release, preview and
promotion uses those digests and builds nothing
([deployment](../subsystems/deployment.md), "Release bundles"). Remaining:
without a worker, bundles record image IDs built on the target; dependency
resolution at build time is not pinned; this pass did not show that the
commit the checks ran against is bound to the digests released.

**Attack and impact.** Mutable worktrees, image tags or build inputs change
between evidence collection, approval and deployment. A malicious or simply
different artifact reaches a standing environment under an older green result.

**Proposed control.** Build from a fixed source snapshot, record artifact
digests and provenance, bind approval/evidence to them, and deploy the same
artifact by digest. Invalidate evidence when governing inputs change. Keep
untrusted builders unable to overwrite approved release artifacts.

**Verify.** Change worktree, tag, Dockerfile, dependency resolution or contract
between check and release. Deployment must either use the approved digest or
require new evidence. Check rollback identity as carefully as forward release.

### TM-19 — supply-chain content becomes executable authority

**Status: partly mitigated.** The agent image recipe pins
every archive it fetches by version and SHA-256
([running an AppTown](../../stock/docs/guides/running-apptown.md)); source and
image scans run as checks ([workers](../operations/workers.md)); a Forge
release is built from a tagged checkout into archives with `SHA256SUMS`
([releasing](../operations/releasing.md)). Remaining: apt packages and package
managers' dependencies resolve when the image is built; project installs run
project-selected code with whatever the container holds.

**Attack and impact.** A compromised image, installer, package, mise backend
or agent extension runs during a legitimate task or Forge deployment. Its
privilege is whatever the build/agent process already has.

**Proposed control.** Pin/verify critical artifacts by digest or trusted
signature/checksum; update deliberately. Use standard vulnerability and secret
scanners, dependency review and provenance. Build Forge in a credential-poor
build environment and deploy an identified artifact. Sandbox project installs
even when dependencies are locked: pinning can pin malicious code too.

**Verify.** Artifact integrity failures stop installation; project install
scripts cannot see control-plane keys. Reproduce the release input/digest
chain. Test mirror failure and updates without bypassing verification.

## Availability, services and operations

### TM-20 — shared resources are exhausted

**Status: partly mitigated.** Every container AppTown
starts, on its host or a worker, has CPU, memory and PID limits
([running an AppTown](../../stock/docs/guides/running-apptown.md)); services
beside tests and checks have their own limits ([workers](../operations/workers.md),
"Limits on services"). The HTTP servers set header timeouts
(`cmd/apptownd/main.go`) and the terminal WebSocket limits a frame to 1 MiB
(`internal/forge/console_pty.go`). Remaining: no per-person, per-project or
instance admission quotas; no disk, image or log quotas.

**Attack and impact.** Jobs fork processes, fill disk/cache/images, emit logs,
hold terminals or open many sessions; public clients send expensive requests.
The result is lost availability for every project and difficult recovery.

**Proposed control.** Admission limits per person/project/instance; workload
CPU/memory/PID, storage/image, network and lifetime quotas, including nested
Docker. Bound request bodies before parsing, connection/frame sizes, expensive
public endpoints and retained logs/sessions. Preserve headroom for recovery.

**Verify.** Disposable fork/disk/log/connection floods remain within assigned
limits and do not prevent control-plane health or cancellation. Test aggregate
limits across many small sessions and slow request headers, not only one large
request. Legitimate long-running jobs retain bounded resource leases.

### TM-21 — webhook reuse or replay starts unwanted paid work

**Status: partly mitigated; the shared token is accepted
for 1.0.** The token is accepted only in the `X-AppTown-Token` header and
compared in constant time; a token in the URL is refused. That one
instance-wide token can fire any webhook workflow in any project, which is
recorded as accepted for 1.0 ([triggers](../subsystems/triggers.md)). Open:
there is no per-event replay protection.

**Attack and impact.** A shared webhook token leaks from a URL/log or sender;
an attacker replays requests or targets another enabled workflow. Consequences
include attribution confusion, unwanted changes and model/service spending.

**Proposed control.** Separate credentials by project/workflow/sender, prefer
signed timestamped payloads or scoped bearer headers, reject duplicate event
IDs, and enforce queue/spend limits. Do not carry long-lived secrets in URLs.

**Verify.** Another workflow/project rejects the credential; old/repeated
events do not duplicate work. Rotation is bounded and observable; retries of
a legitimate event return the established result.

### TM-22 — development services become data or network footholds

**Status: partly mitigated.** Each deployment and each
preview gets its own credentials for its databases and stores, and a preview
gets its own empty data ([services and data](../../stock/docs/guides/services-and-data.md));
services beside tests and checks are bounded ([workers](../operations/workers.md)).
Remaining: the servers started beside AppTown's own steps use the
catalogue's fixed credentials; services a step or chat starts in its private
Docker follow no policy; containers on a worker use Docker's default bridge
network ([agent isolation limits](../subsystems/agent-sandbox.md)).

**Attack and impact.** A project reaches another service instance, receives
production-like data/credentials, or leaves a reachable database alive after
work ends. Manually published Docker ports may widen access unexpectedly.

**Proposed control.** Typed, policy-approved service requests with unique
instances/credentials, synthetic data, explicit client attachment, quotas and
leases. Read-only/review steps receive none by default; tests use fresh state;
preview services remain separate from chat services. Record actual exposed
ports and provide reset/stop without exposing provider credentials.

**Verify.** Cross-project connection fails; restart does not reuse another
owner's instance. Data reset works, production canaries are absent, public
reachability matches declared policy, and credential/service expiry survives
abandoned chats and failed cleanup.

### TM-23 — deployment adapters exercise excessive target authority

**Status: open.** Targets are explicit and each credential
is a named apptown secret resolved on AppTown
([running an AppTown](../../stock/docs/guides/running-apptown.md), "Deployment
targets"; [deploy targets](../subsystems/deploy-targets.md)), but what each
credential may do is decided at the provider, a Docker host's SSH key is
host-wide, and SSH host keys are accepted on first use
(`StrictHostKeyChecking accept-new` in `internal/forge/deployment/hosts.go`).

**Attack and impact.** A compromised control plane or insufficiently bounded
deployment request uses registry, SSH, Docker or cloud credentials to affect
unrelated applications, accounts or environments.

**Proposed control.** Separate identities per target/environment, least-privilege
provider policies, fixed resource namespaces, authenticated target identity
and narrowly typed adapter operations. Resolve credentials only on the trusted
side. Preview/build privileges must not imply standing-environment release
or permission to mutate target definitions.

**Verify.** A project cannot name arbitrary provider resources or substitute
another target/environment. Fake-provider tests reject out-of-scope requests;
deployment evidence includes target identity, artifact digest and authorization.
Verify SSH/TLS identity and provider policy in deployment configuration.

### TM-24 — cancellation and recovery leave authority alive

**Status: partly mitigated.** At startup a recorded task
whose step has moved on is stopped and none is started twice
([daemon lifecycle](../subsystems/daemon-lifecycle.md)); a worker stops a task
whose admission expired ([container lifecycle](../subsystems/container-lifecycle.md));
a run credential stops working when its attempt is no longer live and a
chat's ends with the chat; shutdown keeps a shared bound
([inference security](../subsystems/inference-security.md)). Remaining: a chat
credential lasts as long as the chat, with no expiry; cancelling a request
does not prove the provider stopped billing for it.

**Attack and impact.** A stopped job continues in a nested daemon/provider;
cleanup fails but its credential remains usable; restart resumes stale work
or grants it new authority. Waiting indefinitely is itself a denial of service.

**Proposed control.** Durable owner/attempt identities and independently expiring
grants/resources. Recheck current account, policy and scope before resume;
revoke spending/action admission first, then stop work with bounded cleanup.
Provide an external worker termination path. Do not silently downgrade a
required isolation/service policy when its setup fails.

**Verify.** Restart during a long job, revoke its owner, fail Docker stop and
lose gateway responses. Shutdown still meets its bound; stale attempts cannot
act/report; orphan work expires or is quarantined. Record any in-flight cost
that cannot be cancelled rather than claiming it stopped.

### TM-25 — destructive maintenance affects the wrong state

**Status: partly mitigated.** Cleanup selects resources by
labels and observed IDs, and an unknown owner or a failed read keeps the
resource ([container lifecycle](../subsystems/container-lifecycle.md)). A
deployment restore stops the app's parts first, checks what it copied back and
says "restored" only when every item is verified; a rehearsal restores into a
new place ([running an AppTown](../../stock/docs/guides/running-apptown.md),
"Deployment backups"). Remaining: a restore writes over the deployment's own
volumes in place; safe extraction of a hostile archive and binding a manifest
to its destination were not shown in this pass.

**Attack and impact.** A stale name, changed ownership, tampered backup manifest,
archive path or cleanup race causes deletion/restore into a live or unrelated
resource. A technically successful restore may combine inconsistent DB,
repository and application-volume snapshots.

**Proposed control.** Validate immutable owner/resource identity immediately
before mutation; bind manifests to project/environment/volume identity; safe
archive extraction with size/path/link limits. Restore into isolated new
resources, validate, then switch. Establish coordinated snapshot/restore
semantics and retain a rollback point for destructive operations.

**Verify.** Disposable fixtures for renamed/replaced containers, concurrent
creation, traversal archives, oversized archives, wrong-volume manifests and
interrupted restores must preserve unrelated canaries. Recovery must report
partial failure accurately and be safely repeatable.

### TM-26 — mutable evidence gives misleading assurance

**Status: partly mitigated.** Inference receipts are
written by the gateway, before dispatch and from the response transport
([inference security](../subsystems/inference-security.md), "Receipts"); a
tests step's answer comes only from a witnessed exit
([workers](../operations/workers.md)). Remaining: audit records and receipts
are rows in AppTown's own database and share its administrator's trust;
there is no export to separately controlled storage; Pi-derived usage stays
advisory.

**Attack and impact.** A workload edits its usage/transcript/report, or a
privileged DB actor rewrites decisions. People mistake an attributable claim
for independently observed execution or tamper-resistant history.

**Proposed control.** Label evidence provenance; capture exits/artifact identity
from trusted runners and usage from the gateway. Export consequential events
to access-separated storage with retention/integrity controls. Record actor,
delegation, resource, policy version, source/artifact and outcome; never token
values. Define behavior when recording fails for consequential operations.

**Verify.** Editing runtime reports cannot fabricate trusted approval or cost
allowance. Reconcile trusted usage and execution receipts; missing events are
detectable. An isolated audit-store outage must produce the specified denial
or durable retry, not an unrecorded successful action.

### TM-27 — external clients exceed consent or confuse identities

**Status: partly mitigated.** OAuth consent is bound and
consumed once (TM-09); an MCP session belongs to the user whose credential
opened it and idle sessions close after a day ([MCP](../subsystems/mcp.md));
an agent token is bounded like an OAuth connection holding every scope and
cannot approve, land or administer ([authentication and authority](../subsystems/auth.md)).
Remaining: no reviewed cap on the number of MCP sessions; Origin validation on
the MCP endpoint was not checked in this pass.

**Attack and impact.** A malicious MCP client acquires excess scopes, reuses
another user's session, exchanges/replays tokens or Git tickets, or persuades
a user that its requested authority is narrower than it is. Tool results can
also carry prompt injection into the external agent.

**Proposed control.** Preserve the existing protocol constraints; add consent
binding, appropriate Origin validation, session quotas/expiry and uniform
current-authority checks. Prefer bounded OAuth to broad personal tokens.
Treat returned repo/tool content as data, not a new grant of tool authority.

**Verify.** Wrong audience, scopes, user/session, redirect, expired/rotated
token and simultaneous ticket consumption fail as intended. Include access
after owner disable, changed grants and source-tip change. Exercise client
disconnect/restart without widening authorization or leaking bearer values.

### TM-28 — application hosting has unintended exposure and browser trust

**Status: partly mitigated.** Application origins are not
trusted for Forge mutations, and Forge's `__Host-` cookie cannot be set by a
sibling application ([browser security](../subsystems/browser-security.md),
[application ingress](../subsystems/ingress.md);
`TestBrowserPolicyLeavesHostedAppPolicy`, `TestBrowserSecurityChromium`).
Remaining: no guide describes a viewer gate for previews or deployments, so
whoever reaches an application's address reaches the application; separating
application domains from Forge's registrable domain is the operator's choice.

**Attack and impact.** A sensitive preview is reachable without Forge login;
a hostile application phishes an owner, attacks same-site browser assumptions,
exposes internal data, or uses Forge hosting/network capacity for abuse.

**Proposed control.** Decide preview visibility explicitly; add viewer gates
where required. Isolate untrusted app domains from Forge and keep application
cookie/origin policy narrow. Strip/control forwarding headers at trusted hops,
enforce exposure/resource policy and provide fast application quarantine.
Avoid injecting broadly trusted sibling origins as an unexplained default.

**Verify.** Anonymous access matches each preview/environment's declared
visibility. Host-header variants cannot route to arbitrary containers. A
malicious sibling app cannot set Forge's host-bound cookie or exercise its
session; forwarded-header spoofing cannot change authority. Quarantining one
app preserves the control plane and unrelated projects.
