> Superseded by [architecture.md](architecture.md); this was the working sketch during the review of 13 September 2026.

# The proposed API, as Go

A sketch to review for shape, written 13 September 2026 against the
[shape proposal](shape-proposal.md). Signatures are close to what exists,
not exact; where a method already exists under this name in the package,
it is kept. Read each block as the package's doc comment: this is what a
consumer sees and all a consumer needs.

Three conventions run through every block.

- **A subsystem is one type, built once, with its dependencies declared as
  small interfaces it owns.** `deployment.New(db, deps)` takes a
  `deployment.Dependencies` struct whose fields are interfaces defined in
  `deployment`. Nothing in a subsystem names another subsystem's package
  except through those interfaces, or where the proposal marks a direct
  import as stable and obvious. That is what makes them composable: to use
  Deployment somewhere new, satisfy four small interfaces and you have it.
- **Commands take a `Principal`, not an actor string.** Authorization
  happens inside the operation, through an `Authority` the subsystem
  declares. A caller that is not a person (startup repair, a worker) is a
  system principal.
- **Operations that must share a transaction have an `…In(tx, …)` twin.**
  Facades compose those. Everything else opens its own.

Types named in signatures but not shown are the package's existing words
(`workflow.Change`, `deployment.Release`, `access.Grant`), listed at the
end of each block only where they are new.

## Cross-cutting words every subsystem shares

```go
package principal // or wherever access exports it; small and stable

type Principal struct {
    Kind   string // user | instrument | system | anonymous
    Name   string
    Via    Delegation // the credential or delegation the request came through, if any
}

type Permission struct {
    Capability string // the catalog word: "change.approve", "deployment.deploy"
    Resource   Resource
}
type Resource struct{ Type, ID string; ProjectID int64 }

// Authority is what every subsystem declares for itself and Access satisfies.
// Require refuses or returns the principal as verified for this permission.
type Authority interface {
    Require(ctx context.Context, pr Principal, p Permission) (Principal, error)
}
```

```go
package database // exists

type DB, Tx, Queryer  // as today; Queryer is "a pool or a transaction"
func (db *DB) InTx(func(*Tx) error) error
```

```go
package audit // exists

func RecordIn(ctx, tx *database.Tx, actor string, projectID int64, kind, subject string, payload any) error
func RecordDecisionIn(ctx, tx, decisionID int64, actor string, projectID int64, kind, subject string, payload any) error
func Recent(ctx, q database.Queryer, f Filter) ([]Entry, error)
```

## Access

Identifies people and instruments, holds authority, issues credentials.
No dependencies on other subsystems.

```go
package access

type Service struct{ /* directory, grants, sessions, tokens, oauth, tickets, decisions: private */ }

type Config struct {
    Profile        string        // the authorization profile (since removed)
    SessionTTL     time.Duration
    OpenInstance   bool          // anyone on the network reads
}
func New(db *database.DB, cfg Config) *Service

// ---- authority (what every other subsystem asks of Access)
func (s *Service) Require(ctx, pr Principal, p Permission) (Principal, error)   // implements Authority
func (s *Service) Decide(ctx, pr Principal, p Permission) (Decision, error)     // the answer with its grounds, recorded
func (s *Service) Explain(ctx, pr Principal, p Permission) (Explanation, error) // the same, not recorded

// ---- who is asking
func (s *Service) Resolve(ctx, cred Credential) (Principal, error)   // cookie, token, OAuth bearer, git ticket
func (s *Service) SignIn(ctx, name, password string) (Session, error)
func (s *Service) SignOut(ctx, session string) error
func (s *Service) Bootstrap(ctx, first Account) error                // refused once any account has a password

// ---- the directory
func (s *Service) Users(ctx, pr Principal) ([]User, error)
func (s *Service) SaveUser(ctx, pr Principal, e Edit) error          // last-administrator rule inside
func (s *Service) RemoveName(ctx, pr Principal, name string) error
func (s *Service) Groups(ctx, pr Principal) ([]Group, error)
func (s *Service) SaveGroup(ctx, pr Principal, g GroupEdit) error
func (s *Service) DeleteGroup(ctx, pr Principal, name string) error
func (s *Service) PersonalGroup(ctx, user string) (string, error)

// ---- grants
func (s *Service) Grants(ctx, pr Principal, at Scope) ([]Grant, error)
func (s *Service) Grant(ctx, pr Principal, g GrantRequest) (Grant, error)
func (s *Service) Revoke(ctx, pr Principal, id int64) error

// ---- instruments: personal tokens, connected agents, git tickets
func (s *Service) IssueToken(ctx, pr Principal, name string) (plaintext string, err error)
func (s *Service) RevokeToken(ctx, pr Principal, name string) error
func (s *Service) RegisterClient(ctx, c ClientRegistration) (Client, error)
func (s *Service) Authorize(ctx, pr Principal, req ConsentRequest) (code string, err error)
func (s *Service) Exchange(ctx, code string, proof PKCE) (Connection, Pair, error)
func (s *Service) Refresh(ctx, refresh string) (Pair, error)                  // reuse ends the delegation
func (s *Service) Disconnect(ctx, pr Principal, delegationID int64) error
func (s *Service) IssueTicket(ctx, pr Principal, changeID int64, ref, expectedTip string) (plaintext string, t Ticket, err error)
func (s *Service) ConsumeTicket(ctx, id int64) (bool, error)

// ---- participants for facades
func (s *Service) EstablishOwnershipIn(tx, projectID int64, creator string) error
func (s *Service) RemoveProjectIn(tx, projectID int64) error
func (s *Service) Sweep(ctx) error   // expired credentials; the daemon's schedule calls it
```

New words: `Credential` (a tagged union: cookie, bearer, ticket), `Decision`
(allow or deny with reason code, grant ids, receipt id), `Scope`.

## Projects

Project identity and repository access. Owns the project row and the
generic Git and host operations. Declares what it needs from Access only.

```go
package project

type Service struct{ /* records, repositories, hosts: private */ }

type Dependencies struct {
    Authority Authority
    Hosts     map[string]Host // "gitlab", "github": the SCM host drivers, registered at construction
    DataDir   string
}
func New(db *database.DB, deps Dependencies) *Service

// ---- the record
func (s *Service) Find(ctx, ref ProjectRef) (Project, error)     // by id or slug
func (s *Service) List(ctx, pr Principal) ([]Project, error)
func (s *Service) Edit(ctx, pr Principal, id int64, d Details) (Project, error)

// ---- the repository
func (s *Service) Initialize(ctx, pr Principal, id int64, from Origin) error
func (s *Service) Tree(ctx, id int64, commit string) (Tree, error)         // read-only view of a commit
func (s *Service) PrepareWorkspace(ctx, in WorkspaceRequest) (Workspace, error) // a checkout for a run or console
func (s *Service) Inspect(ctx, id int64) (Repository, error)              // default branch, tip, remote, host kind
func (s *Service) AttachRemote(ctx, pr Principal, id int64, r Remote) error

// ---- the host driver contract this package owns
type Host interface {
    Push(ctx, repo Repository, ref, expectedTip string) error
    Archive(ctx, repo Repository) error
    MergeRequest(ctx, repo Repository, mr MergeRequest) (string, error)
}

// ---- participants
func (s *Service) CreateIn(tx, spec Spec) (int64, error)
func (s *Service) RemoveIn(tx, id int64, who string, payload map[string]any) error
func (s *Service) Contract(ctx, id int64, commit string) (Contract, error)  // the app's own declaration at a commit
```

## Integrations

The instance's connector catalog and what each project attaches. Owns a
small provider interface. Secrets are a separate broker it is given.

```go
package integration

type Service struct{ /* bindings, service kinds, attachments: private */ }

type Dependencies struct {
    Authority Authority
    Secrets   SecretResolver               // Consumer -> Delivery; never the whole vault
    Providers map[string]Provider          // "gitlab", "glitchtip", "infisical", ...: registered at construction
}
func New(db *database.DB, deps Dependencies) *Service

// ---- connectors (instance level)
func (s *Service) Connectors(ctx) ([]Binding, error)
func (s *Service) Describe(ctx, name string) (Snapshot, error)
func (s *Service) SaveConnector(ctx, pr Principal, b BindingEdit) (Binding, error)
func (s *Service) TestConnector(ctx, pr Principal, name string) (Probe, error)
func (s *Service) RemoveConnector(ctx, pr Principal, name string) error
func (s *Service) ServiceKinds(ctx) ([]ServiceKind, error)
func (s *Service) SaveServiceKind(ctx, pr Principal, k ServiceKindEdit) error

// ---- attachments (project level)
func (s *Service) Attached(ctx, projectID int64) ([]Attachment, error)
func (s *Service) Attach(ctx, pr Principal, a AttachRequest) (Attachment, error)
func (s *Service) Detach(ctx, pr Principal, projectID int64, capability string) error

// ---- what runtimes ask
func (s *Service) Resolve(ctx, projectID int64, capability string) (Selected, error)   // the attached connector, resolved
func (s *Service) Contributions(ctx, in ContributionRequest) (Contributions, error)   // env, files, MCP servers for an agent
func (s *Service) PinVersionsIn(tx, projectID int64) (map[string]int, error)          // what a run records

// ---- the provider contract this package owns
type Provider interface {
    Validate(cfg map[string]any) error
    Probe(ctx, cfg map[string]any, secrets Delivery) (Probe, error)
    Contribute(ctx, sel Selected, consumer Consumer) (Contributions, error)
}

func (s *Service) RemoveProjectIn(tx, projectID int64) error
```

## Ways of Working

The instance's catalog of guidance, packs, policies and workflows: what
can be adopted. No project appears in it. Its only dependency is
authority; it could ship as a library on its own.

```go
package wow

type Service struct{ /* library, packs, policies, workflows, stock: private */ }

type Dependencies struct {
    Authority Authority
    Stock     fs.FS   // the shipped packs, blocks, policies, workflows
}
func New(db *database.DB, deps Dependencies) *Service

// ---- blocks of guidance
func (s *Service) Blocks(ctx, f BlockFilter) ([]Block, error)
func (s *Service) Block(ctx, ref BlockRef) (Block, error)              // by name or id, latest or a version
func (s *Service) PublishBlock(ctx, pr Principal, b BlockEdit) (Block, error)

// ---- packs
func (s *Service) Packs(ctx) ([]Pack, error)
func (s *Service) Pack(ctx, name string, version int) (Pack, error)   // version 0 is the latest publication
func (s *Service) PublishPack(ctx, pr Principal, p PackEdit) (Pack, error)
func (s *Service) OfferPack(ctx, pr Principal, name string, on bool) error   // the last one on cannot go off

// ---- workflows and policies
func (s *Service) Workflows(ctx) ([]WorkflowEntry, error)
func (s *Service) Workflow(ctx, name string, version int) (*schema.Spec, int, error)
func (s *Service) PublishWorkflow(ctx, pr Principal, yaml string) (name string, version int, err error)
func (s *Service) BindSystemWorkflow(ctx, pr Principal, stock, name string) error
func (s *Service) Validate(ctx, yaml string) (*schema.Spec, error)
func (s *Service) Policies(ctx) ([]LibraryPolicy, error)
func (s *Service) PublishPolicy(ctx, pr Principal, p LibraryPolicy) error

// ---- resolving pins: the one thing Adoption needs that is not a plain read
func (s *Service) Resolve(ctx, pins []Pin) ([]ResolvedPin, error)   // names and constraints to exact versions
func (s *Service) Compile(ctx, pins []ResolvedPin) (text, hash string, err error)

func (s *Service) SyncStock(ctx) (SyncCounts, error)
```

## Adoption

What a project has taken on from the catalog, with its history. This is
where a project and the ways of working meet, and it is its own
subsystem with its own tables (adoptions, imports, installed policies,
lanes, extensions), not a facade. Every runtime asks Adoption what a
project's procedure is; none of them asks the catalog directly.

```go
package adoption

type Service struct{ /* adoptions, imports, policies, lanes, extensions: private */ }

type Dependencies struct {
    Authority    Authority
    Ways         Catalog            // wow: Pack, Block, Workflow, Resolve, Compile
    Capabilities CapabilityLocker   // one method, Integrations satisfies it: a pack's required capabilities are attached
    Uplift       UpliftOpener       // one method, Work satisfies it: OpenUpliftIn(tx, projectID, Candidate); nil means adopt without a change
}
func New(db *database.DB, deps Dependencies) *Service

// ---- adopting
func (s *Service) Prepare(ctx, in Request) (Candidate, error)                     // what adopting would mean; no writes
func (s *Service) Adopt(ctx, pr Principal, c Candidate, expected int64) (Adoption, error) // atomic: the record, the imports, the uplift if any
func (s *Service) AdoptIn(tx, pr Principal, c Candidate, expected int64) (Adoption, error)

// ---- what a project stands on
func (s *Service) Current(ctx, projectID int64) (Adoption, error)     // pack, version, revision, drift
func (s *Service) History(ctx, projectID int64, limit int) ([]Adoption, error)
func (s *Service) Resolve(ctx, projectID int64) (Resolved, error)     // compiled guidance, pinned blocks, policies, lanes, extensions
func (s *Service) Workflow(ctx, projectID int64, name string) (*schema.Spec, int, error)   // the pinned version
func (s *Service) Checks(ctx, projectID int64) ([]DeclaredCheck, error)                   // what Conformance evaluates against

// ---- per-project settings that live with the adoption
func (s *Service) SetLane(ctx, pr Principal, projectID int64, name string, on bool) error
func (s *Service) SetExtension(ctx, pr Principal, projectID int64, name string, on bool) error
func (s *Service) InstallPolicy(ctx, pr Principal, projectID int64, p PolicyInstall) error
func (s *Service) RemovePolicy(ctx, pr Principal, id int64) error
func (s *Service) NoteDrift(ctx, projectID int64, note string) error

func (s *Service) RemoveProjectIn(tx, projectID int64) error
```

Adoption does not depend on Projects. A project is an identity it is
handed, never a row it reads. The current coupling is three columns
adoption writes onto the projects table (`adoption_revision`,
`drift_note`, `pack`); one migration moves them onto the adoption record,
where the unique revision per project already lives, and the `Project`
word stops carrying fields that are not its own.

## Deployment

Runs selected versions of applications on targets and keeps their
retained data. Owns targets, deployments, attempts, releases, bundles,
backups, inventory, and the driver contract.

```go
package deployment

type Service struct{ /* targets, deployments, releases, outcomes, backups, bundles, inventory, retained: private */ }

type Dependencies struct {
    Authority Authority
    Drivers   map[Kind]DriverFactory
    Secrets   SecretResolver   // references to values at execution time; values are never stored or passed in
    Jobs      Followups        // EnqueueIn(tx, kind, key, values): settlement's after-commit work
    DataDir   string
}
func New(db *database.DB, deps Dependencies) *Service

// DeployRequest carries everything one deploy needs, resolved by the
// caller and recorded immutably with the attempt at admission. Recovery
// after a restart reads that record; nothing is resolved twice.
type DeployRequest struct {
    Application Application    // slug; the tables keep a project id for now, the API does not need one
    Deployment  string         // which standing deployment of it
    Source      Source         // a committed tree (path + commit) or a bundle id
    Contract    Contract       // the app's own declaration at that commit
    Connectors  []Connector    // the selected cloud or registry connectors: configuration with secret references
    Needs       []NeedSpec     // what the contract declares it needs, already looked up in the catalog
    Notes       string         // release notes, as text
}

// ---- targets and offers
func (s *Service) Targets(ctx, pr Principal) ([]Target, error)
func (s *Service) Attach(ctx, pr Principal, t TargetSpec) (Target, error)
func (s *Service) Check(ctx, pr Principal, name string) (TargetState, error)
func (s *Service) Retire(ctx, pr Principal, name string) error
func (s *Service) Offer(ctx, pr Principal, target, group string, caps OfferCaps) error

// ---- deployments (the standing things)
func (s *Service) Deployments(ctx, projectID int64) ([]Deployment, error)
func (s *Service) Bind(ctx, pr Principal, in BindRequest) (Deployment, error)   // a project gets a place on a target
func (s *Service) Unbind(ctx, pr Principal, id int64) (RetainedResources, error) // teardown; data-bearing resources kept
func (s *Service) History(ctx, id int64) ([]Release, error)

// ---- the act of deploying
func (s *Service) Plan(ctx, in DeployRequest) (Plan, error)                 // what would happen; no writes
func (s *Service) Deploy(ctx, pr Principal, in DeployRequest) (Release, error) // admit, execute, settle; one attempt
func (s *Service) DeployIn(tx, pr Principal, in DeployRequest) (Admitted, error) // admission only, for Shipping
func (s *Service) Promote(ctx, pr Principal, releaseID int64, to string) (Release, error)
func (s *Service) Observe(ctx, attemptID int64) (Observation, error)
func (s *Service) Recover(ctx) (RecoveryResult, error)                     // interrupted attempts, at startup

// ---- previews, backups, inventory
func (s *Service) StartPreview(ctx, in PreviewRequest) (Preview, error)
func (s *Service) StopPreview(ctx, changeID int64) error
func (s *Service) Backups(ctx, projectID int64, deployment string) ([]Backup, error)
func (s *Service) TakeBackup(ctx, pr Principal, id int64) (Backup, error)
func (s *Service) Restore(ctx, pr Principal, backupID int64) (Release, error)
func (s *Service) Inventory(ctx, targetID int64) ([]Resource, error)
func (s *Service) Retained(ctx, projectID int64) ([]RetainedResource, error)
func (s *Service) Settle(ctx, pr Principal, retainedID int64, to RetentionState, note string) error

// ---- the driver contract this package owns; drivers implement it directly
type Driver interface {
    Reconcile(ctx, d *Deployment, generation int64, art Artifact, key string, log Logf, checkpoint Checkpoint) (Observation, error)
    Resume(ctx, d *Deployment, generation int64, noted map[string]any, log Logf) (Observation, error)
    Observe(ctx, d *Deployment) (Observation, error)
    Teardown(ctx, d *Deployment, generation int64) error
    Controls() Controls
}
type DriverFactory func(t *Target, cfg TargetConfig) (Driver, error)

func (s *Service) RemoveProjectIn(tx, projectID int64) error
```

**Deployment must stand alone.** The test of this block is that
`deployment` links into a command-line tool or a small HTTP service with
no Work, Console, Conformance or web in the binary. With the request
carrying its inputs, what remains is small: `deployment.SingleOperator`
as a permissive authority, `jobs.Inline()` for follow-ups, an
environment-backed secret resolver, and drivers built from flags. Nothing
in the package imports `internal/forge`, `internal/workflow`,
`internal/project` or `internal/conformance`. Today one query in
`releasesql` joins Work's changes table; that join goes, which is why
`Notes` is text in the request.

`cmd/forge-deploy` is then: parse flags, read the contract from the tree,
build `Drivers`, call `deployment.New`, call `Deploy`, print the release.
The same constructor behind a mux is a standalone deployment API. Forge's
own deploy handler does the same with inputs resolved from Projects,
Integrations and the dependency catalog first.

Drivers: `deployment/docker.New(names HostNames, runtime Docker) Driver`,
`deployment/portainer.New(cfg Config) Driver`,
`deployment/digitalocean.New(client Client, cfg Config) Driver`. Each has
`var _ deployment.Driver = (*Driver)(nil)`.

## Work

Creates changes, runs pinned procedures, records decisions and evidence,
lands approved source. `workflow.Service` already has most of this
surface; the change is what it is constructed with.

```go
package workflow

type Service struct{ /* changes, runs, steps, attempts, landing, workspaces, dispatcher, scm: private */ }

type Dependencies struct {
    Authority    Authority
    Projects     ProjectSource      // Find, PrepareWorkspace, Tree, Hosts (SCM is built on these)
    Procedures   ProcedureSource    // Adoption: Workflow(projectID, name), Resolve(projectID), Current(projectID); pinned at run start
    Integrations ContributionSource // Contributions, PinVersionsIn
    Runners      Runners            // the registered step kinds: agent, check, deploy, script (see below)
    Completion   CompletionRecorder // Shipping's participant: RecordCompletionIn(tx, CompletedRun)
    Jobs         Followups
    DataDir      string
}
func New(db *database.DB, deps Dependencies) *Service

// ---- changes (as today)
func (s *Service) Create(ctx, in ChangeDraft) (*Change, error)
func (s *Service) Edit(ctx, in ChangeEdit) (*Change, error)
func (s *Service) Import(ctx, in ImportBranch) (*Change, error)
func (s *Service) Uplift(ctx, in Uplift) (*Change, error)
func (s *Service) Change(ctx, id int64) (*Change, error)
func (s *Service) Changes(ctx, project int64, in ChangeList) ([]ChangeSummary, error)
func (s *Service) Abandon(ctx, in StopWork) (*Abandoned, error)

// ---- runs and decisions (as today)
func (s *Service) StartRun(ctx, in StartChangeRun) (*Run, error)
func (s *Service) Advance(ctx, id int64) (*AdvanceResult, error)
func (s *Service) Approve(ctx, in Approval) (*Step, error)
func (s *Service) Override(ctx, in Override) (*Step, error)
func (s *Service) RetryStep(ctx, in RetryCommand) (Retried, error)
func (s *Service) SendBack(ctx, in SendBack) (Retried, error)
func (s *Service) RequestLanding(ctx, changeID int64) (*Run, error)
func (s *Service) Run(ctx, id int64) (*Run, error)
func (s *Service) Snapshot(ctx, id int64, view ReadView) (*RunSnapshot, error)
func (s *Service) Runs(ctx, in RunQuery) ([]RunSummary, error)
func (s *Service) Artifact(ctx, id int64) (*ArtifactBody, error)
func (s *Service) ChatContinuation(ctx, changeID, stepID int64) (ChatContinuation, error)

// ---- the runner contract this package owns; forge registers implementations
type Runner interface {
    Run(ctx, at Attempt, in StepInput) (StepEvidence, error)   // one bounded attempt of one step
    Stop(ctx, at Attempt) error
}
type Runners map[string]Runner // "agent/pi", "check", "deploy", "script"

// ---- lifetime and participants
func (s *Service) Start(ctx) error      // dispatcher, schedules
func (s *Service) Drain(ctx) error
func (s *Service) Repair(ctx) error     // orphan steps after a restart
func (s *Service) RemoveProjectIn(tx, projectID int64) error   // refuses while a run is running
```

Note what left: the `deploy` runner is an adapter forge builds over
`deployment.Service`, so Work never imports Deployment; the `check` runner
likewise over Conformance. SCM stays `internal/scm`, constructed by Work
with the host operations Projects supplies.

## Console

Interactive conversations with an agent: open, talk, watch, attach a
terminal, end. Disk records, no tables. A session lives in one project's
checkout, but Console never looks a project up: everything a session
needs is handed in when it is opened. Its dependencies are an agent
runtime and model access, nothing else.

```go
package console

type Service struct{ /* registry, credentials, files, tmux, steering, dind: private */ }

type Dependencies struct {
    Authority Authority
    Agents    AgentRuntime   // start/stop a bounded agent process or container; shared with Work's agent runner
    Models    ModelAccess    // the model slot and inference endpoint a session is given
    Config    Config         // images, networks, idle timeout, data dir
}
func New(deps Dependencies) *Service

// OpenRequest is everything one session needs, resolved by the caller.
type OpenRequest struct {
    Scope         Scope          // what the session belongs to, for listing and authority: a project id, optionally a change id
    Workspace     Workspace      // the checkout: path, repository identity, branch
    Steering      string         // the compiled guidance for the system prompt
    Contributions Contributions  // env, files, MCP servers the agent is given
    Continue      *Continuation  // resume a lane agent's own session rather than start fresh
}

func (s *Service) Open(ctx, pr Principal, in OpenRequest) (Session, error)
func (s *Service) Get(ctx, pr Principal, id string) (Session, error)
func (s *Service) List(ctx, pr Principal, scope Scope) ([]Session, error)
func (s *Service) Messages(ctx, id string, c Cursor) (MessagePage, error)
func (s *Service) Send(ctx, pr Principal, id string, m Message) (Receipt, error)   // verified delivery, not fire-and-forget
func (s *Service) Interrupt(ctx, pr Principal, id string) error
func (s *Service) End(ctx, pr Principal, id string) error
func (s *Service) Attach(ctx, pr Principal, id string, size Size) (Terminal, error) // a PTY over the agent's tmux
func (s *Service) Upload(ctx, pr Principal, id string, f File) (Attachment, error)
func (s *Service) Identity(ctx, id string) (RuntimeIdentity, bool)   // who is running, for inference admission
func (s *Service) Busy(ctx, scope Scope) (bool, error)               // any live session in this scope

// ---- lifetime
func (s *Service) Resume(ctx) error        // adopt sessions that survived a restart
func (s *Service) Shutdown(ctx) error      // bounded; never destroys durable chats
```

A console does not start changes, deploy previews, or resolve a project's
inputs. ChangeConsole, below, does the resolving and the composing: it
asks Projects for the workspace, Adoption for the steering, Integrations
for the contributions, and opens the console with them. The purge asks
`Busy` before removing a project.

## Conformance

Evaluates committed software against versioned checks and records
findings separately from failures to evaluate. Owns evaluation
strategies and review execution.

```go
package conformance

type Service struct{ /* reviews, verdicts, adjudications, exceptions, fixes, evaluators: private */ }

type Dependencies struct {
    Authority  Authority
    Projects   SourceStager        // stage a commit's tree for tools
    Procedures CheckSource         // Adoption: Checks(projectID), what to evaluate against
    Serving    ServingObserver     // Deployment: Observe the serving release, for runtime checks; nil skips them with a stated reason
    Agents     AgentRuntime        // for model-judged checks
    Tools      map[string]Tool     // semgrep, django-check-deploy, ...: registered at construction
    Budget     Budget
}
func New(db *database.DB, deps Dependencies) *Service

func (s *Service) Review(ctx, pr Principal, in ReviewRequest) (*Review, error)       // start and run to completion
func (s *Service) Evaluate(ctx, in Assessment) ([]Verdict, error)                    // one source against one check set, no review row
func (s *Service) Reviews(ctx, limit int) ([]ReviewSummary, error)
func (s *Service) View(ctx, reviewID int64) (*View, error)
func (s *Service) Standing(ctx, projectID int64) (*RecordedStanding, error)
func (s *Service) History(ctx, projectID int64, limit int) ([]HistoryPoint, error)
func (s *Service) Fixes(ctx, projectID int64, limit int) ([]Fix, error)
func (s *Service) Confirm(ctx, pr Principal, in Confirmation) (*Adjudication, error)
func (s *Service) Except(ctx, pr Principal, in ExceptionRequest) (*Disposition, error)
func (s *Service) RevokeException(ctx, pr Principal, id int64) (*Revocation, error)
func (s *Service) ClearHistory(ctx, pr Principal) (*HistoryRemoval, error)

// ---- the evaluator contract this package owns
type Tool interface {
    Evaluate(ctx, staged Source, c DeclaredCheck) ([]Verdict, error)
}

func (s *Service) Repair(ctx) error
func (s *Service) RemoveProjectIn(tx, projectID int64) error
```

## Models

Model selection, capacity and inference admission, with accounting. A
cross-cutting service, not a subsystem with a domain, but it has a
surface everything agentic uses.

```go
package model

type Service struct{ /* slots, usage, providers, pools: private */ }

type Dependencies struct {
    Authority    Authority
    Integrations ProviderResolver   // the model provider connectors
    Secrets      SecretResolver
    Identity     RuntimeIdentity    // who is behind an inference request: Work attempt, console session, account
    Config       Config             // trusted networks, default model
}
func New(db *database.DB, deps Dependencies) *Service

func (s *Service) Select(ctx, in SelectionRequest) (Selection, error)   // slot, pattern, profile for this project
func (s *Service) Catalog(ctx) ([]Model, error)
func (s *Service) ConfigureSlot(ctx, pr Principal, in SlotEdit) error
func (s *Service) Acquire(ctx, binding string, holder Holder) (Permit, error)   // capacity; Permit.Release()
func (s *Service) PublicConfig(ctx, projectID int64) (PublicConfig, error)      // what an agent runtime is handed
func (s *Service) Handler() http.Handler                                        // the inference endpoint agents call
func (s *Service) Usage(ctx, w UsageWindow) (UsageReport, error)
```

## The objects

The blocks above are the services. Underneath each one sit a few objects
that carry the subsystem's rules, and the service's job is to load one,
call it, save it and write the receipt. This is the level the sketch has
to get right for the subsystems to be reusable: the rules travel with the
object, not with the SQL.

The shape in Go. An object is a value with methods. It holds no database
handle and never writes. A command on it checks its own invariants,
changes the value, and returns what the world must be told: the receipt
to record, the follow-up to enqueue. The service wraps that in one
transaction: read the row `FOR UPDATE`, build the object, call the
method, write the row back, record the receipt. With the record-store
style that is one whole-row read and one whole-row write around a method
call; the state words with `Transition` methods that already exist
become methods on the objects that own them.

```go
// One pattern, everywhere.
func (s *Service) Approve(ctx, in Approval) (*Step, error) {
    pr, err := s.authority.Require(ctx, in.By, Permission{"change.approve", changeResource(in.ChangeID)})
    ...
    committed, err := database.Transact(ctx, s.db, func(tx *database.Tx) (decisionCommit, error) {
        run, err := s.runs.LoadForUpdate(tx, in.RunID)          // whole row -> *Run
        ev, err := run.Approve(pr, in.Step, in.Note, s.now())  // the rule; mutates run; returns the event
        if err := s.runs.Save(tx, run); err != nil { ... }       // whole row back
        if err := audit.RecordIn(ctx, tx, ...ev...); err != nil { ... }
        return ev.Result(), nil // private step plus rework intent
    })
    if err != nil { return nil, err }
    s.dispatchRework(committed.rework) // only after commit, owned by Work
    return committed.step, nil
}
```

What each subsystem's objects are, and what they know.

**Projects**

```go
type Project struct { ID int64; Slug, Name, DefaultBranch, Namespace string; OwnerGroup string; ... }
func (p *Project) Rename(slug, name string) error            // slug rules; a rename is a receipt
func (p *Project) Repository() Repository                   // where its source is
func (p Project) CanBeRemovedBy(pr Principal, live Activity) error   // the purge's refusal, stated once
```

**Work**

```go
type Change struct { ID, ProjectID int64; State ChangeState; Title Title; Branch string; ExpectedRevision int64; ... }
func (c *Change) Begin(by Principal) (Event, error)          // open -> in progress; refuses when closed
func (c *Change) Abandon(by Principal, why string) (Event, error)
func (c *Change) RequestLanding(adopted Adoption) (LandingRequest, error)   // checks the expected revision here
func (c *Change) Retitle(by Principal, t Title) (Event, error)               // human vs generated ownership rule

type Run struct { ID int64; Change *Change; Definition *schema.Spec; Stage string; Steps []*Step; State RunState; ... }
func (r *Run) Advance(now time.Time) (Advanced, error)      // gate passed? next stage, steps to dispatch
func (r *Run) Approve(pr Principal, stepID int64, note string, now time.Time) (Event, error)
func (r *Run) Override(pr Principal, stepID int64, why string) (Event, error)
func (r *Run) Retry(stepID int64, mode RetryMode) (Retried, error)
func (r *Run) Complete() (Completion, error)                // what Shipping records; refuses on an open step
func (r *Run) Interrupt() Event                             // restart repair

type Step struct { ... }
func (s *Step) Attempt(n int, evidence Evidence) (Outcome, error)
func (s *Step) Gate() Gate                                  // passed / permits merge
```

**Deployment**

```go
type Deployment struct { ID int64; Application Application; Name string; Target *Target; Attributes Attributes; Generation int64; Standing bool; ... }
func (d *Deployment) Admit(req DeployRequest, now time.Time) (*Attempt, error)   // one open attempt at a time; generation fence
func (d *Deployment) Removable() error                     // protected data, open attempts
func (d *Deployment) DeployableByHand() error
func (d *Deployment) Retire() (RetainedResources, Event)

type Attempt struct { ID int64; Deployment *Deployment; Generation int64; Inputs Inputs; State AttemptState; Checkpoint Checkpoint; ... }
func (a *Attempt) Note(cp Checkpoint) error                 // provider identifiers as soon as they exist
func (a *Attempt) Complete(obs Observation) (Outcome, error)
func (a *Attempt) Fail(cause error) Outcome
func (a *Attempt) Uncertain(cause error) Outcome            // honest unknown, for recovery
func (a *Attempt) Resumable() bool

type Target struct { ... }
func (t *Target) Offer(group string, caps OfferCaps) (Offer, error)
func (t *Target) Retirable(open []RetainedResource) error   // never retire the last cloud connector with open resources
type Offer struct{ ... }
func (o *Offer) Permits(t *Target, app Application, attrs Attributes, standing int) error   // exists today

type Release struct { ... }
func (r *Release) Promote(to string) (Event, error)
type Backup struct { ... }
func (b *Backup) Restorable() error                         // exists today as BackupState.Restorable
```

**Access**

```go
type Account struct { Name, Role string; HasPassword, Disabled bool; ... }
func (a *Account) Disable(others []Account) error           // the last-administrator rule, stated once
func (a *Account) Demote(others []Account) error
func (a *Account) SetPassword(hash string) Event            // ends sessions: returned as a follow-up

type Grant struct { ... }
func (g Grant) Applies(c Capability, r Resource) bool       // exists today as GrantApplies
func (g *Grant) Revoke(by Principal, now time.Time) (Event, error)
func (g Grant) Live(at time.Time) bool

type Delegation struct { ... }                              // an OAuth connection
func (d *Delegation) Rotate(presented string) (Pair, error) // reuse is a leak: returns ErrLeaked, the service ends it
func (d Delegation) Bounds(pr Principal) Principal          // narrows a person's authority to what was delegated

type Ticket struct { ... }
func (t *Ticket) Consume(now time.Time) error               // once
```

**Adoption**

```go
type Adoption struct { ProjectID int64; Pack string; Version, Revision int64; Imports []ResolvedPin; Drift string; ... }
func (a *Adoption) Prepare(c Candidate, attached Capabilities) (Plan, error)   // what changes; refuses a missing capability
func (a *Adoption) Advance(p Plan, expected int64) (*Adoption, Event, error)   // the revision check is here, not in SQL
func (a Adoption) Resolve(cat Catalog) (Resolved, error)     // compile the guidance from the pins
func (a *Adoption) NoteDrift(note string) Event
```

**Conformance**

```go
type Review struct { ID int64; Source SourceHead; Checks []DeclaredCheck; State ReviewState; Verdicts []Verdict; ... }
func (r *Review) Record(v Verdict) error                    // a verdict for a check not in the set is refused
func (r *Review) Finish(c Completion) (Standing, error)
func (r *Review) Delta(prev *Review) ReviewDelta            // exists today as CompareReviews
type Verdict struct{ ... }
func (v Verdict) FixedBy(next Verdict) bool                 // exists today
func (v Verdict) Excepted(ds []Disposition, now time.Time) (*Disposition, bool)
type Disposition struct{ ... }
func (d *Disposition) Revoke(by Principal) (Event, error)
```

**Console**

```go
type Session struct { ID string; Scope Scope; Workspace Workspace; State SessionState; Transcript *Transcript; ... }
func (s *Session) Accept(m Message) (Delivery, error)       // refused while the agent's turn is open
func (s *Session) HandedBack() bool                         // exists today on the transcript
func (s *Session) Idle(now time.Time, limit time.Duration) bool
func (s *Session) End() Event
```

Two rules for deciding what goes on an object and what stays in the
service. A rule that needs only the object and what the caller hands it
goes on the object: the last-administrator check takes the other
administrators as an argument rather than reaching for the database. A
rule that is about uniqueness or about another object's current state
stays in the service and the schema: the live-grant identity is a unique
index, and "no open attempt on this deployment" is a locked read followed
by `Deployment.Admit`. The object is the whole rule for what it holds;
the store is the whole rule for what is between rows.

The payoff for composition is that the objects are the same everywhere.
A command-line deploy tool and forge's deploy handler both end up calling
`Deployment.Admit` and `Attempt.Complete`; the difference between them is
who resolved the request and which authority answered.

## Facades

Each is a package of its own, imports its participants directly, owns no
tables except where noted, and exposes complete operations.

```go
package shipping // Work's completion + Deployment's execution, one transaction

type Service struct{ work *workflow.Service; deploy *deployment.Service; jobs Followups }
func New(work *workflow.Service, deploy *deployment.Service, jobs Followups) *Service

func (s *Service) Completion() workflow.CompletionRecorder   // what Work is constructed with
func (s *Service) Ship(ctx, pr Principal, in ShipRequest) (Release, error)   // a landed change to a deployment
func (s *Service) Preview(ctx, pr Principal, changeID int64) (Preview, error)
```

```go
package changeconsole // a console that works a change

type Service struct{ work *workflow.Service; chats *console.Service; ship *shipping.Service; inputs SessionInputs }
func New(work *workflow.Service, chats *console.Service, ship *shipping.Service, inputs SessionInputs) *Service
// SessionInputs resolves one project's workspace, steering and contributions: forge builds it over Projects, Adoption and Integrations.

func (s *Service) Open(ctx, pr Principal, changeID int64) (console.Session, error)   // resolves the inputs, continues the lane's session
func (s *Service) Land(ctx, pr Principal, sessionID string) (*workflow.Run, error)
func (s *Service) Preview(ctx, pr Principal, sessionID string) (Preview, error)
func (s *Service) Recheck(ctx, pr Principal, sessionID string) (*workflow.Run, error)
func (s *Service) Stop(ctx, pr Principal, sessionID string) error
```

```go
package remediation // Conformance findings become Work changes; owns items, attempts and its worker

type Service struct{ /* items, attempts, schedule: its own tables */ }
func New(db *database.DB, checks *conformance.Service, work *workflow.Service, chats console.ActivityObserver, jobs Followups) *Service

func (s *Service) Admit(ctx, pr Principal, in FixRequest) (Item, error)   // creates the Work change atomically
func (s *Service) Items(ctx, projectID int64) ([]Item, error)
func (s *Service) Retry(ctx, pr Principal, itemID int64) (Attempt, error)
func (s *Service) Start(ctx) error; Drain(ctx) error; Repair(ctx) error
```

```go
package integrationchange // attach a service by opening a governed change

func New(integrations *integration.Service, work *workflow.Service) *Service
func (s *Service) Attach(ctx, pr Principal, in AttachRequest) (*workflow.Change, error)
```

```go
package projectlifecycle // create with ownership and adoption; purge every owner's records

func New(db *database.DB, p Participants) *Service   // Participants lists every subsystem's In-participants by name
func (s *Service) Create(ctx, pr Principal, in CreateRequest) (project.Project, error)
func (s *Service) Remove(ctx, pr Principal, id int64) (Removed, error)   // refuses live work or chats; external cleanup after commit
```

```go
package runtimeidentity // who is behind an inference request

func New(work workflow.AttemptObserver, chats console.IdentitySource, auth access.AccountReader) model.RuntimeIdentity
```

```go
package startuprepair

func New(work, deploy, checks Repairer) *Service
func (s *Service) Run(ctx) error   // the daemon calls it before admitting anything
```

## The App

```go
package forge

type App struct {
    Access       *access.Service
    Projects     *project.Service
    Integrations *integration.Service
    Ways         *wow.Service
    Adoption     *adoption.Service
    Deployment   *deployment.Service
    Work         *workflow.Service
    Console      *console.Service
    Conformance  *conformance.Service
    Models       *model.Service

    Shipping     *shipping.Service
    Changes      *changeconsole.Service
    Fixes        *remediation.Service
    Lifecycle    *projectlifecycle.Service

    Web          http.Handler   // the routes; every handler is parse, call, respond
}

func New(db *database.DB, cfg Config, drivers Drivers, queue jobs.Admitter) (*App, Lifecycle, error)
```

`New` is the forty lines in the shape proposal. `Drivers` is what only the
daemon knows: the Docker socket, the DigitalOcean token source, the SCM
hosts, the secret stores, the process runner. `Lifecycle` is
`Start(ctx)`, `Drain(ctx)`, one deadline for all owners.

## Composing something new

Two examples of what the shape buys.

**A deploy-only tool.** A command that takes a bundle and puts it on a
target, with no changes, runs or web:

```go
deploy := deployment.New(db, deployment.Dependencies{Authority: deployment.SingleOperator, Secrets: envSecrets, Drivers: drivers, Jobs: jobs.Inline()})
rel, err := deploy.Deploy(ctx, operator, deployment.DeployRequest{
    Application: app, Deployment: "prod", Source: deployment.BundleSource(bundleID), Contract: contract, Connectors: connectors})
```

Nothing about Work, Console or Conformance is constructed. `jobs.Inline()`
runs follow-ups in-process.

**A bot that fixes what a review found.** Conformance, Work and a console,
no Deployment:

```go
checks := conformance.New(db, conformance.Dependencies{Authority: auth, Projects: projects, Procedures: adopted, Tools: tools})
work   := workflow.New(db, workflow.Dependencies{Authority: auth, Projects: projects, Procedures: adopted,
            Integrations: integrations, Runners: workflow.Runners{"agent/pi": agents}, Completion: shipping.None()})
chats  := console.New(console.Dependencies{Authority: auth, Agents: agents, Models: models})
fixes  := remediation.New(db, checks, work, chats, jobs.Inline())
item, err := fixes.Admit(ctx, bot, remediation.FixRequest{ProjectID: id, Finding: findingID})
```

The Deployment dependency of Conformance is optional here: runtime checks
that need a serving release are skipped with a stated reason when
`ServingObserver` is nil. That kind of optionality is worth designing in
deliberately, per dependency, and noting in the constructor's comment.

## What to review

- Is each subsystem's surface the right size? Fifteen to thirty methods
  each here; anything a consumer never calls should move inside.
- Are the declared dependencies the right ones and small enough? Each
  `Dependencies` field is an interface of one to four methods.
- Are the facades the right cuts? Shipping and ChangeConsole are the two
  that carry the most today.
- Is `Principal` on every command acceptable, with the actor string gone?
- Which existing names should survive that this sketch renamed?
