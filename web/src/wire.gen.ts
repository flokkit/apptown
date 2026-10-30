// Generated from the Go response types by internal/apptown/httpapi's
// TestWebTypesAreCurrent. Do not edit: change the Go type and run
//   go test ./internal/apptown/httpapi -run TestWebTypesAreCurrent -update-wire

export type ADRChangeSummary = {
  id: string | null
  number: number
  title: string
  status: ADRState
  tags: string
}

export type ADRRecord = {
  id: string | null
  projectId: string | null
  projectSlug?: string
  projectName?: string
  number: number
  title: string
  status: ADRState
  bodyMd: string
  tags: string
  supersedesId: string | null
  changeId: string | null
  changeNumber: number
  createdAt: string
  updatedAt: string
}

export type ADRState = "draft" | "proposed" | "accepted" | "rejected" | "deprecated" | "superseded"

export type AddressSnapshot = {
  deploymentId: string | null
  targetId: string | null
  revision: number
  ingress: Ingress
}

export type AdministratorNetworksView = {
  networks: string[]
  you: string
  youInside: boolean
}

export type AdoptionApplied = {
  revision: number
  candidate: Candidate
}

export type AdoptionUpliftOpened = {
  changeId: string | null
  changeNumber: number
  candidate: Candidate
  runId?: string | null
  runNumber?: number
  workflow?: string
  runError?: string
  notStarted?: string
}

export type AdvanceResult = {
  advanced: boolean
  runStatus: RunState
  stage: string
  blocking: (Step | null)[]
  required: string[]
  missing: string[]
  followUpsPending: boolean
}

export type AllBackups = {
  backups: ProjectBackup[]
  keep: number
  where: string
}

export type ApprovalBrief = {
  brief: string
}

export type ApprovalPolicy = {
  requireIndependentApproval: boolean
}

export type ApprovalQuestion = {
  step: string
  status: string
  means: string
  instructions: string
  landingMeans: string[]
  adds?: ChangeAdds | null
}

export type ArtifactAttached = {
  id: string | null
}

export type AttachableList = {
  attachables: Attachable[]
}

export type AttachmentAdded = {
  name: string
  bytes: number
  sha256: string
}

export type AttachmentEntered = {
  ok: boolean
  capability: string
  connector: string
  provenance: string
}

export type AttachmentList = {
  attachments: AttachmentView[]
}

export type AttentionCoverage = {
  key: string
  name: string
  observedAt: string
  unknown: boolean
  stale: boolean
}

export type AttentionItem = {
  severity: string
  key: string
  title: string
  detail: string
  href: string
  observedAt: string
  stale: boolean
}

export type AttentionView = {
  items: AttentionItem[]
  sources: AttentionCoverage[]
}

export type AuthMe = {
  mode: string
  external?: ExternalProviderSummary | null
  needsBootstrap?: boolean | null
  user?: string
  role?: string
  via?: string
  from?: string
}

export type AuthzExplanation = {
  user: string
  capability: string
  decision: Decision
  whoCan: string[]
}

export type AuthzHoldings = {
  user: string
  actorId?: string | null | null
  instance: Record<string, boolean>
  createIn?: string[] | null
  defaultGroup?: string | null
  project?: Record<string, boolean>
}

export type BackupStoreState = {
  store: BackupStore | null
}

export type BackupTaken = {
  ok: boolean
  backup: Backup | null
}

export type BrowserErrorReporting = {
  dsn?: string
  release?: string
}

export type CapabilitySaved = {
  ok: boolean
  capability: string
}

export type CatalogApplied = {
  change: Change
  run: Run
  catalog: string
}

export type CatalogChangeDetail = {
  change: CatalogChange
  definition: CatalogDef
}

export type CatalogList = {
  catalog: CatalogEntry[]
}

export type ChangeAttachment = {
  name: string
  bytes: number
}

export type ChangeBoard = {
  columns: BoardColumn[]
}

export type ChangeCockpit = {
  change: Change
  projectSlug: string
  detail: RunSnapshot | null
  adrs: ADRChangeSummary[]
  evidence: CockpitEvidence
  runs: ChangeRunSummary[]
  commits: ChangeCommit[]
  head?: string
  diffstat: string
  committedDiff: CommittedComparison
  report: string
  editing: number
  state: string
  stateDetail: string
  usage: ChangeUsage
  next: NextAct[]
  approval?: ApprovalStanding | null
  mainConflict?: MainConflict | null
}

export type ChangeEnvelope = {
  change: Change
}

export type ChangePreview = {
  url: string
  hostOnly?: boolean
  commit: string
}

export type ChangeRelease = {
  number: number
  deployment: string
  state: ReleaseState
  serving: boolean
  observed?: string
  reason?: string
}

export type ChangeRun = {
  change: Change
  run: Run
}

export type ChatHistoryView = {
  sessions: ChatHistoryEntry[]
  unreadable: number
  unreadableFolders?: string[]
}

export type CloudAccountCheck = {
  ok: boolean
  detail: string
  dryRun: boolean
  registry: string
}

export type CloudTargetAttached = {
  ok: boolean
  target: Target | null
  detail: string
  registry: string
}

export type CockpitEvidence = {
  targets: AcceptanceCheck[]
  merged: boolean
  releases: ChangeRelease[]
  inAdvance?: AdvanceYes | null
}

export type ComplaintFiled = {
  ok: boolean
  id: string | null
}

export type ComplaintList = {
  complaints: Complaint[]
}

export type ComplaintSteeringText = {
  step: string
  console: string
  note: string
}

export type ComplaintsArchived = {
  ok: boolean
  archived: number
}

export type ConformanceFixList = {
  fixes: ConformanceFix[]
}

export type ConformanceHistory = {
  history: HistoryPoint[]
}

export type ConformanceObservation = {
  id: string | null
  reviewId: string | null
  projectId: string | null
  projectSlug: string
  sha: string
  block: string
  version: number
  check: string
  evaluator: string
  evaluatorVer: string
  mode: string
  severity: string
  detail: string
  at: string
  exceptedBy?: string
  evidenceHash?: string
  state: string
}

export type ConformanceReviewDetail = {
  review: Review | null
  verdicts: Verdict[]
}

export type ConformanceReviewList = {
  reviews: ReviewSummary[]
}

export type ConformanceView = {
  lens: LensInfo
  projects: ProjectStanding[]
  byProject: Record<string, ConformanceObservation[]>
  byCheck: Record<string, ConformanceObservation[]>
  checkDefs: Record<string, CheckDefinition>
  exceptions: Record<string, Disposition[]>
  adjudications: Record<string, Adjudication>
  priorConfirmations: Record<string, PriorConfirmation>
  summary: Summary
  reviews: Review[]
}

export type ConnectorCatalog = {
  catalog: ConnectorEntry[]
}

export type ConsoleChangeList = {
  changes: ConsoleChange[]
}

export type ConsoleMessagePage = {
  messages: Message[]
  state: string
  turn: string
}

export type ConsoleOpened = {
  session: ConsoleSessionView
}

export type ConsolePreviewAddress = {
  url: string
  containerUrl: string
  port: number
}

export type ConsoleSessionList = {
  sessions: ConsoleSessionView[]
}

export type ConsoleSessionView = {
  id: string
  projectSlug: string
  model: string
  createdAt: string
  state: string
  changeId: string | null
  continuedFrom?: string
  openedBy: string
  mayAct: boolean
  cliMissing?: boolean
  turn?: string
  placement?: string
  saved?: ConsoleSave | null
  recordAsOf?: string | null
}

export type ContractCheck = {
  ok: boolean
  errors: string[]
  warnings: string[]
  summary: ContractSummary
}

export type ContractUnparsed = {
  ok: boolean
  errors: string[]
}

export type CreatedChange = {
  change: Change
  run: Run | null
}

export type DashboardView = {
  pendingApprovals: PendingApproval[]
  activeRuns: ActiveRun[]
  recentAdrs: ADRRecord[]
  endedOperations: Operation[]
}

export type DelegationList = {
  delegations: Delegation[]
}

export type DelegationRevoked = {
  ok: boolean
  id: string
}

export type Departures = {
  departures: Departure[]
  previewsObserved?: string
  previewsReason?: string
}

export type DependencyKind = {
  sizes?: Record<string, Size>
  resources?: MemoryDefault
  capability: string
  title: string
  icon?: string
  description?: string
  summary?: string
  status?: string
  provides?: Record<string, string>
  defaults?: Record<string, string>
  parameters?: string[]
  credentials?: Record<string, string>
  generate?: string[]
  integration?: boolean
  local: { image: string; command?: string[]; entrypoint?: string[]; configAtStart?: boolean; keepsImage?: boolean; stepEnv?: Record<string, string>; port?: number; env?: Record<string, string>; inject?: Record<string, string>; health?: string; data?: string; measure?: string }
  copy?: Copy | null
  cloud?: Record<string, Cloud>
  register?: { connector: string; adapter?: string; inject?: Record<string, string> } | null
}

export type DependencyList = {
  dependencies: DependencyKind[]
}

export type DeploymentAIModels = {
  keys: AIModelsKey[]
}

export type DeploymentAddress = {
  deploymentId: string | null
  targetId: string | null
  generatedHostname: string
  customHostname: string
  certificateDomain: string
  revision: number
  servingURL: string
  reservedHosts: string[]
}

export type DeploymentBackups = {
  backups: Backup[]
  plan: BackupPlanItem[]
  note: string
  keep: number
}

export type DeploymentChecked = {
  deployment: string
  checked: RecoveryResult[]
}

export type DeploymentDiagnostics = {
  deployment: string
  generation: number
  desiredGeneration: number
  commit?: string
  releaseNumber?: number
  runtime: DiagnosedRuntime
  containers: boolean
  unsupported?: string
  unsettled?: string
  parts: PartDiagnosis[]
  events: LimitEvent[]
  eventsKnown: boolean
  eventsSource: string
  hold?: CrashLoopHold | null
  logMaxLines: number
  logMaxBytes: number
}

export type DeploymentProtection = {
  deployment: string
  dataClass: string
  offHost: boolean
  keptOn?: string
  unknown?: string
  note?: string
  target: string
  operations?: DataOperations | null
  takeDown?: string
  items: ItemProtection[]
  running?: Operation | null
  recent: Operation[]
  startFrom?: StartedFromCopy | null
  rehearsal?: RehearsalRef | null
  rehearsalNote?: string
  restoreTest?: RestoredCopy | null
  restoreTestNote?: string
  restoredCopies: RestoredCopy[]
  coverage: Coverage
}

export type DeploymentRemoved = {
  ok: boolean
  deleted: string[]
  finalBackup?: FinalSet | null
  orphans: string[]
}

export type DeploymentResources = {
  recorded: boolean
  profile?: string
  how?: string
  chosen?: string
  parts: PartResource[]
  share?: HostShare | null
  limits: LimitCount[]
  limitsKnown: boolean
  limitsSource: string
}

export type DeploymentView = {
  targetId: string | null
  id: string | null
  displayLabel: string
  name: string
  kind: string
  url: string
  hostOnly?: boolean
  running: boolean | null
  releaseRun: string | null
  updatedAt: string
  static?: boolean
  target?: DeploymentTargetView | null
  observation: string
  observationIssue?: string
  observedAt?: string
  observationAttemptedAt?: string
  observationStale: boolean
  feed?: string
  audience?: string
  data?: string
  protected: boolean
  generation?: number
  readiness?: string
  changeId?: string | null
  needs?: DeploymentNeedView[]
  aiModels?: string[]
  restoreCheck?: string
  dataAfterRestore?: RestoreFill | null
}

export type DevComposition = {
  compose: string
  env: Record<string, string>
  procfile: string
  staticBuilds?: DevStaticBuild[]
}

export type DiscoveredModels = {
  models: string[]
}

export type DiskReadings = {
  disks: Reading[]
}

export type DispositionCreated = {
  id: string | null
}

export type DispositionList = {
  dispositions: Disposition[]
}

export type DocGuide = {
  slug: string
  content: string
  links: DocLinks
}

export type DocsIndex = {
  groups: DocGuideGroup[]
  integrations: DocIntegrationEntry[]
}

export type Done = {
  ok: boolean
}

export type EvalDeleted = {
  ok: boolean
}

export type EvalList = {
  evals: Eval[]
}

export type EvalScorecard = {
  id: string | null
  number: number
  startedBy: string
  startedAt: string
  meaning: string
  subjectKind: string
  subject: string
  subjectDigest?: string
  budgetMicros: number
  state: string
  reason?: string
  finishedAt?: string | null
  arms: Arm[]
  costMicros: number
  incomplete: number
  armCosts: Record<string, ArmCost>
  complaints: Complaint[]
  tallies: Tally[]
  comparable: number
  notComparable: number
}

export type EvalStartForm = {
  meaning: string
  profiles: string[]
  packs: PackChoice[]
  maxChanges: number
}

export type ExternalBindRequest = {
  providerId: string | null
  principalId: string | null
  subject: string
}

export type ExternalBindingList = {
  bindings: ExternalBindingView[]
}

export type ExternalBindingView = {
  id: string | null
  providerId: string | null
  principalId: string | null
  issuer: string
  subject: string
  revoked: boolean
}

export type ExternalEnableRequest = {
  revision: number
  enabled: boolean
}

export type ExternalProvider = {
  id: string | null
  revision: number
  issuer: string
  displayName: string
  clientId: string
  secretRef: string
  callbackOrigin: string
  enabled: boolean
  allowSignup: boolean
  initialGroup: string
}

export type ExternalProviderChange = {
  revision: number
  issuer: string
  displayName: string
  clientId: string
  secretRef: string
  allowSignup: boolean
  initialGroup: string
}

export type ExternalProviderSummary = {
  displayName: string
  allowSignup: boolean
}

export type ExternalRedirect = {
  url: string
}

export type ExternalStartRequest = {
  name: string
  returnPath: string
  password: string
  link: boolean
}

export type FinalCopies = {
  copies: FinalCopy[]
}

export type FleetView = {
  packs: FleetPack[]
}

export type GeneratedGuidance = {
  generated: steeringSection[]
  alwaysBytes: number
  conditionalBytes: number
}

export type GrantCreated = {
  id: string | null
}

export type GrantList = {
  grants: GrantView[]
}

export type GroupList = {
  groups: Group[]
}

export type GuidanceBlockDetail = {
  block: Block
  shownVersion: number
  versions: BlockVersion[]
  usedBy: BlockUse[]
  checks: Declaration[]
}

export type InAdvanceQuestion = {
  workflow: string
  acceptance: string
  landing: string[]
  notOffered?: string
}

export type InfrastructureTargets = {
  targets: InfrastructureView[]
}

export type InstanceActivity = {
  agents: ActiveAgent[]
  active: RunRow[]
  history: ChangeHistory[]
  deployments: ActivityDeployment[]
}

export type InstanceSecret = {
  name: string
  held: boolean
}

export type InstanceUpdateArtifact = {
  name: string
  sha256: string
  size: number
}

export type InstanceUpdateCandidate = {
  version: string
  commit: string
  platform: string
  notes: string
  archive: InstanceUpdateArtifact
  evidence: InstanceUpdateEvidence
}

export type InstanceUpdateEvidence = {
  manifestSHA256: string
  checksumsSHA256: string
  signer: string
  issuer: string
  sourceCommit: string
}

export type InstanceUpdateStatus = {
  build: string
  platform: string
  channel: string
  state: string
  lastAttempt?: string | null
  lastSuccess?: string | null
  candidate?: InstanceUpdateCandidate | null
  reasonCode?: string
  reason?: string
}

export type IntegrationDocs = {
  admin: string
  developer: string
}

export type IntegrationField = {
  key: string
  label: string
  type?: string
  help?: string
  placeholder?: string
  optional?: boolean
}

export type IntegrationHealth = {
  ok: boolean
  detail: string
  checked: boolean
  checkedAt?: string
}

export type IntegrationList = {
  integrations: IntegrationEntry[]
}

export type IntegrationSaved = {
  ok: boolean
  health: IntegrationHealth
}

export type IntegrationWiring = {
  change: Change
  run?: Run | null
  url: string
}

export type InviteLink = {
  invite: Invite
  link: string
  note: string
}

export type InviteList = {
  invites: Invite[]
}

export type InviteOffer = {
  purpose: string
  username: string
  role: string
  invitedBy: string
  expiresAt: string
  state: string
}

export type InvokedChange = {
  change: Change
  run: Run | null
  url: string
}

export type IssuedToken = {
  token: string
  name: string
  user: string
  role: string
  purpose: string
  note: string
}

export type JobList = {
  jobs: Job[]
}

export type LandingStarted = {
  runId: string | null
  message: string
}

export type LatestWhatIf = {
  whatIf: WhatIf | null
  eligible: string[]
  ownPacksNewest: PackVersion[]
}

export type LibraryWorkflow = {
  name: string
  description: string
  latestVersion: number
  origin: string
  updatedAt: string
  stages: string[]
  kind: string
  category: string
  tier: string
  system: boolean
  usedBy: LibraryWorkflowUse[]
  yaml: string
}

export type ModelCatalogList = {
  models: ModelEntry[]
  unlisted: UnlistedBinding[]
}

export type ModelConfigured = {
  ok: boolean
  model: string
}

export type ModelSlotMatrix = {
  profiles: string[]
  slots: ModelSlotView[]
  projects: ModelSlotProject[]
  overrides: ModelSlotOverride[]
  spendNote: string
}

export type NarrativeRefreshed = {
  ok: boolean
  summary: string
}

export type OpenVEXDocument = {
  "@context": string
  "@id": string
  author: string
  timestamp: string
  version: number
  statements: OpenVEXStatement[]
}

export type Operation = {
  id: string | null
  number: number
  kind: string
  projectId: string | null
  subject?: string
  holds: string[]
  askedBy: string
  askedVia?: string
  input: Record<string, unknown>
  state: OperationState
  verification?: string
  steps: OperationStep[]
  result: Record<string, unknown>
  detail?: string
  cancelRequested?: boolean
  cancellable: boolean
  createdAt: string
  startedAt?: string
  touchedAt: string
  finishedAt?: string
  dismissedAt?: string
}

export type OperationAdmitted = {
  operationId: string | null
  state: OperationState
  url: string
}

export type OperationDismissed = {
  ok: boolean
}

export type OverrideResult = {
  override?: Override | null
  release?: RedeployNumber | null
  notYet?: string
}

export type PackHistory = {
  name: string
  description: string
  origin: string
  declaration: PackSpec
  recommended: number
  latest: number
  versions: Publication[]
}

export type PackInterviewPick = {
  decided: boolean
  packs: string[]
  why: string
  icon: string
}

export type PackInterviewQuestions = {
  decided: boolean
  questions: string[]
}

export type PackOffered = {
  ok: boolean
  name: string
  enabled: boolean
}

export type PackPulled = {
  pack: string
  installed: string[]
  removed: string[]
  guidanceToAdd: string[]
  conflicting: string[]
  keptReferenced: string[]
  deprecated: string[]
}

export type PackSaved = {
  ok: boolean
  version: number
  warnings: string[]
}

export type PackView = {
  apiVersion?: string
  name: string
  description: string
  platform?: boolean
  guidance: string[]
  workflows: string[]
  policies: PackPolicy[]
  toolchain?: string
  tools?: Record<string, string>
  services?: ServiceSpec[]
  extensions?: string[]
  version?: number
  origin: string
  enabled: boolean
  requires: packRequirement[]
  guidanceBytes: number
  generatedBytes: number
  declaration: PackSpec
  staticStarter?: boolean
}

export type PartLog = {
  part: string
  container: string
  generation?: number | null
  text: string
  truncated: boolean
  maxLines: number
  maxBytes: number
  readAt: string
}

export type PastedPicture = {
  name: string
  path: string
}

export type PolicyLibraryList = {
  library: PolicyLibraryEntry[]
}

export type PolicyUsageList = {
  usedBy: PolicyUsage[]
}

export type Project = {
  id: string | null
  slug: string
  name: string
  defaultBranch: string
  namespace: string
  icon: string
  blurb: string
  createdAt: string
  requireIndependentApproval: boolean
  settingsRevision: number
}

export type ProjectAdoptionRecord = {
  current: RecordedRevision | null
  history: RecordedRevision[]
  packs: projectPack[]
  additions: ReceiptBlock[]
  exclusions: ReceiptBlock[]
  conditions: placementCondition[]
}

export type ProjectCleanupList = {
  cleanups: Deletion[]
}

export type ProjectCreationService = {
  capability: string
  params: Record<string, string>
}

export type ProjectCreationSetup = {
  origin: string
  sourceURL: string
  sourceBranch: string
  credentialSecret: string
  bundleSHA256: string
  services: ProjectCreationService[]
  upstream: ProjectCreationUpstream
}

export type ProjectCreationUpstream = {
  enabled: boolean
  namespaceId: number
  repositoryName: string
  attachmentRequestId: string | null
}

export type ProjectDeletionState = {
  projectId: string | null
  state: string
  requestedBy?: string
  requestedByPrincipal?: string | null
  bindingId?: string | null
  synthetic?: boolean
  takeDownsStarted?: boolean
  stalled?: string
  requestedAt?: string
  deletedAt?: string
  keptUntil?: string
  purgeStartedAt?: string
  slug: string
  remaining?: string[]
  takeDown?: Operation | null
  purge?: Operation | null
  bindingRevoked?: boolean
}

export type ProjectDetail = {
  id: string | null
  slug: string
  name: string
  namespace: string
  icon: string
  blurb: string
  defaultBranch: string
  createdAt: string
  runtimeForm: string
  intranetReady: boolean
  outOfBandEdits: string[]
  main?: MainStanding | null
  requireIndependentApproval: boolean
  settingsRevision: number
  lifecycle: Lifecycle
}

export type ProjectDraft = {
  requestId?: string | null
  automaticAddress?: boolean
  setup?: ProjectCreationSetup | null
  slug: string
  name: string
  namespace: string
  blurb: string
  icon: string
  group: string
  packs?: string[]
}

export type ProjectFindingsView = {
  findings: Finding[]
  sources: FindingSource[]
  dispositions: Disposition[]
  projectSlug: string
  repo: FindingsRepoState
  summary: FindingsSummary
}

export type ProjectGuidance = {
  revision: number
  imports: Import[]
  generated: steeringSection[]
  effective: EffectiveGuidance
}

export type ProjectLanes = {
  lanes: LaneView[]
  system: LaneView[]
}

export type ProjectManifestView = {
  project: ManifestProject
  repo: ManifestRepo
  packs: ManifestPack[]
  workflows: ManifestWorkflow[]
  guidance: ManifestPin[]
  guidanceHash: string
  bindings: ManifestBinding[]
  policies: Policy[]
}

export type ProjectMemberList = {
  roles: GrantView[]
  open: boolean
}

export type ProjectOverviewView = {
  repo: OverviewRepo | null
  active: RunRow[]
  open: OpenChange[]
  history: ChangeHistory[]
}

export type ProjectPage = {
  projects: ProjectRow[]
  total: number
}

export type ProjectPolicyList = {
  policies: Policy[]
  violations: Violation[]
}

export type ProjectRow = {
  id: string | null
  slug: string
  name: string
  defaultBranch: string
  namespace: string
  icon: string
  blurb: string
  createdAt: string
  requireIndependentApproval: boolean
  settingsRevision: number
  activeItems: number
  adrCount: number
  doneRuns: number
  lastRunAt: string
  toolchain: string[]
  prodRunning: boolean | null
  deployed: boolean
  observation: string
  observationIssue?: string
  observedAt?: string
  observationAttemptedAt?: string
  observationStale: boolean
}

export type ProjectSecrets = {
  declared: boolean
  spec: string
  problem?: string
  project?: string
  revision?: string
  profiles?: string[]
  ignored?: string[]
  secrets?: DeclaredSecret[]
  stores: SecretStoreInfo[]
  deployments: SecretDeployment[]
  choosable: string[]
}

export type ProjectServiceList = {
  needs: DeclaredNeed[]
  contract: string
  declaration: string
}

export type ProjectTargets = {
  targets: OfferedTarget[]
  group: string
}

export type ProjectWorkflowRow = {
  id: string | null
  projectId: string | null
  name: string
  kind: string
  version: number
  yaml: string
  origin: string
  updatedAt: string
  summary?: workflowSummary | null
  runsCount: number
  lastRunAt?: string
  system: boolean
  tier: string
  withdrawn?: boolean
}

export type PullPlan = {
  pack: string
  additions: string[]
  removals: string[]
  keptReferenced: string[]
  deprecated: string[]
  guidance: string[]
  conflicting: string[]
}

export type RecentWhatIfs = {
  whatIfs: WhatIf[]
}

export type RecheckStarted = {
  runId: string | null
  rerun: string[]
  message: string
}

export type ReleaseDefault = {
  eligibilityIssue?: string
  projectId: string | null
  deploymentId?: string | null
  revision: number
}

export type ReleaseDetail = {
  release: Release | null
  changes: ReleaseChange[]
  isLive: boolean
  events: ReleaseEvent[]
  sbomArtifact?: string | null
  bundle?: ReleaseBundleView | null
}

export type ReleaseListed = {
  id: string | null
  projectId: string | null
  number: number
  state: ReleaseState
  deployment: string
  externalRef?: string
  approvedBy?: string
  runId?: string | null
  createdAt: string
  releasedAt?: string
  notes?: string
  commitSha?: string
  bundleId?: string | null
  deploymentId?: string | null
  generation?: number
  artifactHash?: string
  origin: string
  changes: ReleaseChangeTitle[]
  serving: boolean
  observed?: string
  reason?: string
  sbomArtifact?: string | null
}

export type ReleasePlan = {
  deploymentId?: string | null
  deploymentLabel?: string
  defaultRevision: number
  address?: DeploymentAddress | null
  configured: boolean
  deployment: string
  configError?: string
  url?: string
  published: boolean
  head?: ReleaseHead | null
  lastRelease?: LastRelease | null
  changesSinceLast: ShippedChange[]
}

export type ReleasePreviewStarted = {
  url: string
}

export type ReleaseRequested = {
  ok: boolean
  number: number
  deployment: string
  state: ReleaseState
}

export type RemediationDetail = {
  remediation: Remediation
  items: Item[]
  attempts: Record<string, Attempt[]>
  occupancy: Occupancy
}

export type RemediationList = {
  remediations: Remediation[]
  occupancy: Occupancy
}

export type RemediationOpened = {
  change: Change
  run: Run | null
  started: boolean
}

export type RemediationPreview = {
  items: number
  projects: number
  findings: ConformanceObservation[]
}

export type RemediationStarted = {
  remediation: Remediation
  items: Item[]
}

export type RemoteAttached = {
  attachmentId: string | null
  remote: string
  pushed: string
}

export type RemoteNamespaces = {
  namespaces: RemoteNamespace[]
  configured: boolean
}

export type RepositoryAttachmentState = {
  state: string
  remote: string
  repositoryName: string
  attachmentId: string | null
  providerProjectId: number
  request?: AttachmentRequest | null
  canResume: boolean
}

export type RestartedLanding = {
  run: Run | null
}

export type RestoreResult = {
  backup: string | null
  from: string
  safetyBackup: string | null
  stopped: boolean
  started?: boolean | null
  notStarted?: string[]
  ready?: boolean | null
  status: RestoreStatus
  items: RestoredItem[]
  error?: string
}

export type RetainedList = {
  retained: RetainedResource[]
}

export type RoleCatalog = {
  roles: RoleView[]
  capabilities: Record<string, CapabilityView>
}

export type RoutineAdoptionTarget = {
  currentId: string | null
  targetId: string | null
  candidateDigest: string
  classifications: RoutineConsentRef[]
}

export type RoutineConsentRef = {
  id: string | null
  digest: string
  shown: string
}

export type RoutinePublicationClassification = {
  id: string | null
  requestId: string | null
  question: RoutinePublicationQuestion
  issuer: StandingActor
  at: string
  revocation?: RoutinePublicationRevocation | null
}

export type RoutinePublicationConsent = {
  requestId: string | null
  transition: RoutinePublicationTransition
  shownDigest: string
}

export type RoutinePublicationQuestion = {
  transition: RoutinePublicationTransition
  before: unknown
  after: unknown
  eligible: boolean
  reason: string
  shown: string
  digest: string
}

export type RoutinePublicationRevocation = {
  actor: StandingActor
  at: string
  reason: string
}

export type RoutinePublicationTransition = {
  kind: string
  id: string | null
  from: number
  to: number
}

export type RunFindingsView = {
  findings: Finding[]
  unread: UnreadReport[]
  projectSlug: string
  summary: FindingsSummary
}

export type RunRow = {
  runId: string | null
  runNumber: number
  changeId: string | null
  changeNumber: number
  workflow: string
  title: string
  status: RunState
  stage: string
  state?: string
  detail?: string
  project?: string
  startedAt: string
  durationS: number
}

export type RunTimeline = {
  run: string | null
  workflow: string
  createdAt: string
  steps: TimelineEntry[]
}

export type RunTrace = {
  run: Run | null
  projectSlug: string
  change: Change | null
  workflow: TraceWorkflow
  stages: string[]
  steps: (Step | null)[]
  events: Event[]
  artifacts: Artifact[]
  guidance: unknown
}

export type RunsAbandoned = {
  ok: boolean
  abandoned: (string | null)[]
}

export type ScheduleList = {
  evalCopy: boolean
  schedules: ScheduleView[]
}

export type SecretBound = {
  deployment: string
  name: string
  store: string
  version: string
  bound: boolean
}

export type SecretNameList = {
  names: string[]
  stores: Record<string, string>
}

export type SecretProfileSet = {
  deployment: string
  profile: string
}

export type SecretRevealed = {
  name: string
  deployment: string
  version: number
  value: string
}

export type SecretStoreInfo = {
  name: string
  title: string
  available: boolean
  note: string
}

export type SendBackAccepted = {
  runId: string | null
  stepId: string | null
}

export type ServiceKindDefinition = {
  capability: string
  title: string
  description?: string
  icon?: string
  connector: { fields?: IntegrationField[] }
  resource: { fields?: IntegrationField[] }
  env?: EnvironmentEntry[]
  steering?: string
  guidance?: string
}

export type ServiceKindList = {
  services: ServiceKindEntry[]
}

export type ServicesProposed = {
  changeId: string | null
  changeNumber: number
  needs: NeedSelection[]
}

export type ServicesSeeded = {
  needs: NeedSelection[]
  direct: boolean
}

export type SetupState = {
  needsModel: boolean
  agentImage: string
  agentImageError: string
  openNonLoopback: boolean
}

export type SignedInAs = {
  ok: boolean
  user: string
  role: string
}

export type SpecGate = {
  require?: string[]
}

export type SpecStage = {
  name: string
  onEnter?: SpecStep[]
  steps?: SpecStep[]
  gate?: SpecGate | null
}

export type SpecStep = {
  name: string
  uses: string
  with?: Record<string, unknown>
}

export type StandingActor = {
  principal?: string | null
  name: string
  via?: string
}

export type StandingAdvisoryFix = {
  id: string
  source: string
  modified: string
  bodyDigest: string
  package: string
  before: string
  after: string
}

export type StandingAdvisoryObservation = {
  status: string
  reason: string
  fixes: StandingAdvisoryFix[]
}

export type StandingAssessment = {
  adoption?: RoutineAdoptionTarget | null
  ruleId: string | null
  ruleRevision: number
  questionDigest: string
  projectId: string | null
  changeId: string | null
  runId: string | null
  stage: string
  retryRevision: number
  adoptionId: string | null
  adoptionRevision: number
  inspection: StandingInspection
  processingAvailable: boolean
}

export type StandingCategories = {
  dependencies: string
  baseImages: boolean
  securityFixes: boolean
  waysOfWorking: boolean
}

export type StandingCommand = {
  ruleId: string | null
  expectedRevision: number
  definition: StandingDefinition
  shownDigest: string
}

export type StandingDefinition = {
  administrative: boolean
  projects: StandingProject[]
  categories: StandingCategories
  delivery: string
}

export type StandingDisable = {
  expectedRevision: number
}

export type StandingEnvironment = {
  deploymentId: string | null
  targetId: string | null
  promotion: boolean
}

export type StandingImageObservation = {
  reference: string
  manifest: string
  configDigest: string
  preparedImage: string
  node: string
  platform: string
  reportDigest: string
  scratchSettlement: string
}

export type StandingInspection = {
  advisories?: StandingAdvisoryObservation | null
  images?: StandingImageObservation[]
  classification: string
  reason: string
  classifier: string
  base: string
  candidate: string
  tree: string
  changedFiles: string[]
  validation?: StandingValidation | null
}

export type StandingProcedure = {
  acceptance: StandingPurpose
  landing: StandingPurpose
}

export type StandingProject = {
  workflow: string
  projectId: string | null
  environments: StandingEnvironment[]
}

export type StandingProjectMeaning = {
  projectId: string | null
  procedure: StandingProcedure
}

export type StandingPurpose = {
  workflow: string
  version: number
  step: string
  means: string
  approvers: string[]
}

export type StandingQuestion = {
  definition: StandingDefinition
  projects: StandingProjectMeaning[]
  shown: string
  digest: string
}

export type StandingRevision = {
  ruleId: string | null
  revision: number
  enabled: boolean
  question: StandingQuestion
  issuer: StandingActor
  enabledAt: string
  changedBy: StandingActor
  changedAt: string
  reason: string
}

export type StandingStatus = {
  rule: StandingRevision
  eligible: boolean
  reason: string
  processingAvailable: boolean
}

export type StandingValidation = {
  image: string
  node: string
  npm: string
  platform: string
  reportDigest: string
  scratchSettlement: string
}

export type StepInterrupted = {
  ok: boolean
  stopped: string[]
}

export type StepProgress = {
  running: boolean
  status?: StepState | null
  lines: string[]
  total: number
  elapsedMs?: number | null
}

export type StepTiming = {
  stage: string
  name: string
  type: string
  runs: number
  medianMs: number
  p90Ms: number
  maxMs: number
  waitMedianMs: number
  costMicros: number
  costIncomplete: number
  reworks: number
}

export type SystemWorkflowBindings = {
  bindings: SystemWorkflowBinding[]
  candidates: string[]
}

export type TargetAttached = {
  ok: boolean
  target: Target | null
  detail: string
}

export type TargetCapacity = {
  target: string
  kind: string
  elastic: boolean
  words: string
  memory?: ResourceCapacity | null
  cpu?: ResourceCapacity | null
  disk?: DiskCapacity | null
  tenants?: Tenant[]
  parts?: ElasticPart[]
  estimate?: Figure | null
  readAt: string
}

export type TargetCheck = {
  ok: boolean
  detail: string
  dryRun: boolean
}

export type TargetChecked = {
  ok: boolean
  detail: string
  status: TargetState
}

export type TargetEdited = {
  ok: boolean
  target: Target | null
}

export type TargetList = {
  targets: TargetView[]
  kinds: KindInfo[]
  groups: string[]
}

export type TargetOffers = {
  ok: boolean
  offers: Offer[]
}

export type TargetPage = {
  target: TargetView | null
  history: TargetEvent[]
  builtIn: boolean
  registry?: RegistryReading | null
}

export type TargetRetired = {
  ok: boolean
  status: TargetState
}

export type TierMap = {
  tiers: TierView[]
}

export type TimelineEntry = {
  stage: string
  name: string
  type: string
  status: string
  startedAt?: string
  finishedAt?: string
  ms: number
  waitMs: number
  costMicros: number
  costIncomplete: number
}

export type TimingReport = {
  days: number
  workflows: WorkflowTiming[]
}

export type TokenList = {
  tokens: Token[]
}

export type ToolchainWritten = {
  ok: boolean
  toolchain: string
}

export type UsageReport = {
  windowDays: number
  totals: Totals
  byKind: UsageKindLine[]
  byModel: UsageModelLine[]
  byDay: UsageDayLine[]
  byDayKind: UsageDayKind[]
  byWorkflow?: UsageWorkflowLine[]
  byProject?: UsageProjectLine[]
}

export type UserList = {
  users: Account[]
}

export type WhatIfStarted = {
  id: string | null
}

export type WorkerChatList = {
  chats: WorkerChatView[]
}

export type WorkerPools = {
  pools: WorkerPool[]
}

export type WorkflowInvalid = {
  ok: boolean
  error: string
}

export type WorkflowPreview = {
  ok: boolean
  name: string
  description: string
  kind: string
  stages: WorkflowStagePreview[]
}

export type WorkflowPublished = {
  name: string
  version: number
}

export type WorkflowTiming = {
  workflow: string
  days: number
  runs: number
  totalMedianMs: number
  steps: StepTiming[]
}

export type Ingress = {
  hosts: string[]
  url: string
  certificateDomain: string
}

export type Candidate = {
  requiredCapabilities?: string[]
  expectedRevision: number
  packs: PackPick[]
  additions: string[]
  receipt: Receipt
  digest: string
  added: string[]
  removed: string[]
  changed: string[]
  older: string[]
  moves: RuleMove[]
  lanesAdded: string[]
  lanesRemoved: string[]
  newChecks: CheckSummary[]
  weakens: string[]
  kept: string[]
  problems: string[]
  copyOf?: CopySource | null
}

export type RunState = "running" | "done" | "abandoned"

export type Step = {
  id: string | null
  runId: string | null
  stage: string
  name: string
  type: string
  config: unknown
  status: StepState
  evidence: unknown
  overrideBy: string
  overrideReason: string
  startedAt: string
  finishedAt: string
  checkReading?: CheckReading | null
}

export type ProjectBackup = {
  id: string | null
  apptown?: string | null
  projectId: string | null
  deployment: string
  takenAt: string
  trigger: string
  actor: string
  actorPrincipal?: string | null
  path: string
  bytes: number
  items: BackupItem[]
  status: BackupState
  error?: string
  store: string
  dataClass?: string
  storeEndpoint?: string
  storeBucket?: string
  project: string
}

export type ChangeAdds = {
  base: string
  head: string
  rows: AddsRow[]
  digest: string
  readFrom: string
  nothingNew: string
  lines: AddsLine[]
}

export type Attachable = {
  capability: string
  title: string
  description: string
  provisionable: boolean
  connectors: string[]
  resourceFields: IntegrationField[]
}

export type AttachmentView = {
  capability: string
  title: string
  connector: string
  provenance: string
  resource: Record<string, unknown>
  createdAt: string
  wirable: boolean
  docs?: string
  lastEvent?: string
  wire?: IntegrationWire | null
}

export type Decision = {
  allow: boolean
  code?: string
  reason: string
  grantIds?: (string | null)[]
  roles?: string[]
  paths?: string[]
}

export type BackupStore = {
  endpoint: string
  region: string
  bucket: string
  accessKeyFrom: string
  secretKeyFrom: string
  attachedBy: string
  attachedByPrincipal?: string | null
  attachedAt: string
  checkedAt: string
}

export type Backup = {
  id: string | null
  apptown?: string | null
  projectId: string | null
  deployment: string
  takenAt: string
  trigger: string
  actor: string
  actorPrincipal?: string | null
  path: string
  bytes: number
  items: BackupItem[]
  status: BackupState
  error?: string
  store: string
  dataClass?: string
  storeEndpoint?: string
  storeBucket?: string
}

export type Change = {
  id: string | null
  number: number
  projectId: string | null
  type: string
  title: string
  body: string
  runId: string | null
  state: ChangeState
  branch?: string
  baseSha?: string
  landedSha?: string
  externalRef?: string
  summary?: string
  provenance?: string
  createdAt: string
  closedAt?: string
  closedAs?: string
  stateLabel: string
  targetAdoptionId?: string | null
  expectedRevision?: number
  adoptedRevision?: number
  editRevision: number
  openedBy?: string
  openedByPrincipal?: string | null
  requestMet: string
  requestGap?: string
}

export type Run = {
  id: string | null
  number: number
  projectId: string | null
  workflowName: string
  workflowVersion: number
  kind: string
  status: RunState
  currentStage: string
  changeId: string | null
  parentRunId?: string | null
  parentStepId?: string | null
  adoptionId?: string | null
  createdAt: string
  updatedAt: string
}

export type CatalogChange = {
  name: string
  description: string
}

export type CatalogDef = {
  params?: CatalogParam[]
  prompt: string
  workflow: string
  title?: string
  type?: string
  model?: string
  thinking?: string
  timeoutMinutes?: number
}

export type CatalogEntry = {
  name: string
  description: string
  missingWorkflow?: string
  fromPack?: string
}

export type BoardColumn = {
  state: ChangeState
  total: number
  items: ChangeSummary[]
}

export type RunSnapshot = {
  run: Run | null
  stages: string[]
  steps: (Step | null)[]
  state?: string
  stateDetail?: string
  ending?: string
  events: Event[]
  artifacts: Artifact[]
  landingMeans: string[]
  landingQueued?: boolean
  next: NextAct[]
  sentBack?: SentBack | null
}

export type ChangeRunSummary = {
  id: string | null
  number: number
  workflowName: string
  status: RunState
  currentStage: string
  role?: string
  createdAt: string
  updatedAt: string
}

export type ChangeCommit = {
  sha: string
  subject: string
  when: string
}

export type CommittedComparison = {
  status: string
  message?: string
  shipped?: ShippedComparison | null
}

export type ChangeUsage = {
  total: number
  costMicros: number
}

export type NextAct = {
  act: string
  stepId: string | null
  step: string
}

export type ApprovalStanding = {
  stepId: string | null
  viewer: string
  reason?: string
  adds?: ChangeAdds | null
  addsError?: string
}

export type MainConflict = {
  mainBranch: string
  main: string
  step: string
  files: string[]
  catchUp: string
}

export type ReleaseState = "releasing" | "released" | "failed"

export type ChatHistoryEntry = {
  id: string
  createdAt: string
  actor: string
  ended: boolean
  imported: boolean
  messages: Message[]
}

export type Target = {
  displayLabel: string
  connectionId?: string | null
  providerScope?: unknown
  id: string | null
  name: string
  kind: string
  provider: string
  address: Record<string, unknown>
  origin: string
  capabilities: TargetCapabilities
  ingress: Record<string, unknown>
  needsMode: string
  status: TargetState
  settingsRevision: number
}

export type AcceptanceCheck = {
  block: string
  check: string
  version?: number
  title?: string
  expects?: string
}

export type AdvanceYes = {
  approver: string
  principal: string | null
  via?: string
  at: string
  workflow: string
  shown: string[]
}

export type Complaint = {
  id: string | null
  number: number
  at: string
  source: string
  runId?: string | null
  runNumber: number
  workflow?: string
  step?: string
  sessionId?: string
  actor?: string
  actorPrincipal?: string | null
  text: string
  changeId?: string | null
  changeNumber: number
  why?: string
  wish?: string
  seen: boolean
  answer?: string
  answeredBy?: string
  answeredByPrincipal?: string | null
  answeredAt?: string
  archivedAt?: string
  state: string
  project?: string
  projectName?: string
  about: string
  packVersion?: string
  evalId?: string | null
  armId?: string | null
}

export type ConformanceFix = {
  fix: Fix
  sentence: string
}

export type HistoryPoint = {
  reviewId: string | null
  at: string
  findings: number
  flagged: number
  excepted: number
  unassessed: number
  broken: number
  total: number
  standing: string
}

export type Outcome = "conforms" | "does-not-conform" | "excepted" | "not-applicable" | "cannot-assess" | "error"

export type Review = {
  id: string | null
  number: number
  trigger: string
  actor: string
  actorPrincipal?: string | null
  startedAt: string
  finishedAt: string
  projects: number
  verdicts: number
  errors: number
  cost: number
  status: ReviewState
  projectsTotal: number
  projectsDone: number
  currentProject: string
}

export type Verdict = {
  id: string | null
  reviewId: string | null
  projectId: string | null
  projectSlug: string
  sha: string
  block: string
  version: number
  check: string
  evaluator: string
  evaluatorVer: string
  mode: string
  state: Outcome
  severity: string
  detail: string
  at: string
  exceptedBy?: string
  evidenceHash?: string
}

export type ReviewSummary = {
  id: string | null
  number: number
  trigger: string
  actor: string
  actorPrincipal?: string | null
  startedAt: string
  finishedAt: string
  projects: number
  verdicts: number
  errors: number
  cost: number
  status: ReviewState
  projectsTotal: number
  projectsDone: number
  currentProject: string
  findings: number
  flagged: number
  projectsWithFindings: number
  judged: number
  total: number
  delta?: ReviewDelta | null
}

export type LensInfo = {
  kind: string
  review?: Review | null
  delta?: ReviewDelta | null
}

export type ProjectStanding = {
  projectId: string | null
  slug: string
  name: string
  standing: string
  sentence: string
  lanes: Lanes
  total: number
  judged: number
  high: number
  adopted: string[]
  sha: string
  judgedAt: string
}

export type CheckDefinition = {
  title?: string
  key: string
  block: string
  version: number
  check: string
  expects: string
  evaluator: string
  severity: string
  sh?: string
  rubric?: string
  tool?: string
  probe?: string
  scope: string
  files?: string[]
  judgedWhere: string
  adopters: string[]
}

export type Disposition = {
  id: string | null
  fingerprint?: string
  identifier?: string
  package?: string
  status: DispositionKind
  justification: string
  by: string
  byPrincipal?: string | null
  createdAt: string
  expiresAt?: string
}

export type Adjudication = {
  id: string | null
  verdictId: string | null
  kind: string
  by: string
  byPrincipal?: string | null
  note?: string
  createdAt: string
}

export type PriorConfirmation = {
  sha: string
  by: string
  at: string
}

export type Summary = {
  projects: number
  byStanding: Record<string, number>
  findings: number
  flagged: number
  high: number
  checks: number
}

export type ConnectorEntry = {
  capability: string
  provider: string
  title: string
  description: string
  multi: boolean
  managed: boolean
  icon?: string
  fields: ConnectorField[]
  presets?: ConnectorPreset[]
  hasDocs: boolean
}

export type ConsoleChange = {
  changeId: string | null
  title: string
  state: ChangeState
  closedAs?: string
  stateLabel: string
  doing: string
  detail: string
}

export type Message = {
  i: number
  role: string
  text?: string
  tools?: ({ name: string; detail?: string })[]
  at?: string
  error?: boolean
  stopped?: boolean
}

export type ConsoleSave = {
  at: string
  outcome: string
  commit?: string
  reason?: string
  kept?: boolean
}

export type ContractSummary = {
  parts: string[]
  needs: string[]
}

export type PendingApproval = {
  stepId: string | null
  stepName: string
  stage: string
  runId: string | null
  changeId: string | null
  changeNumber: number
  itemTitle: string
  projectSlug: string
  projectName: string
}

export type ActiveRun = {
  runId: string | null
  runNumber: number
  changeId: string | null
  changeNumber: number
  itemTitle: string
  workflow: string
  stage: string
  projectSlug: string
  state: string
  stateDetail?: string
}

export type Delegation = {
  id: string | null
  user: string
  clientId: string
  clientName: string
  audience: string
  scopes: string[]
  authTime: string
  createdAt: string
  lastUsed: string
  expiresAt: string
  revokedAt?: string
}

export type Departure = {
  at: string
  slug: string
  name: string
  icon?: string
  tier: string
  label: string
  who?: string
  state: string
  url?: string
}

export type Size = {
  description: string
  resources: MemoryDefault
  command: string[]
}

export type MemoryDefault = {
  memory?: string
}

export type Copy = {
  format?: string
  ext?: string
  client?: string
  runIn?: string
  export?: string
  import?: string
  restore?: string
  restoreStopped?: boolean
  verify?: string
  caveat?: string
  empty?: string
  env?: Record<string, string>
}

export type Cloud = {
  kind?: string
  reach?: Reach | null
  native?: Native | null
}

export type AIModelsKey = {
  name: string
  allowance?: Allowance | null
  unknown?: string
}

export type BackupPlanItem = {
  kind: string
  name: string
  volume?: string
  how: string
  caveat?: string
}

export type RecoveryResult = {
  attempt: string | null
  outcome: string
  reason?: string
}

export type DiagnosedRuntime = {
  state: string
  observedAt?: string
  attemptedAt?: string
  stale: boolean
  reason?: string
}

export type PartDiagnosis = {
  part: string
  public?: boolean
  need?: boolean
  generation?: number | null
  presence: string
  facts?: ContainerFacts | null
  observedAt?: string
  attemptedAt?: string
  stale: boolean
  reason?: string
}

export type LimitEvent = {
  part: string
  kind: string
  container: string
  at: string
  exitCode?: number | null
  generation?: number | null
}

export type CrashLoopHold = {
  receipt: string | null
  at: string
  confirmed: boolean
  restarts: number
  window: string
  exitCode?: string
  detail?: string
}

export type DataOperations = {
  copy: Availability
  check: Availability
  restore: Availability
  rehearsal: Availability
  startFromCopy: Availability
  finalCopy: Availability
}

export type ItemProtection = {
  need: string
  volume?: string
  where: string
  copy: string
  copyNote?: string
  lastCopy?: CopyRef | null
  native?: NativeBackups | null
  drill?: DrillRef | null
}

export type StartedFromCopy = {
  backup: string | null
  filled?: string
}

export type RehearsalRef = {
  at: string
  outcome: string
  passed: boolean
  backup: string | null
  release: number
  host: string
  failedStep?: string
  askedBy: string
  askedByPrincipal?: string | null
  operation: string | null
  facts: RehearsalFacts
  previous?: RehearsalRef | null
}

export type RestoredCopy = {
  id: string | null
  number: number
  source: string
  deployment?: string
  backup: string | null
  release?: number
  target?: string
  askedBy: string
  askedByPrincipal?: string | null
  startedAt: string
  outcome?: string
  failedStep?: string
  checkedAt?: string
  endedAt?: string
  endDetail?: string
  facts: RehearsalFacts
  operation: string | null
  warning: string
  boundary: string
  protection?: string
}

export type Coverage = {
  assessed: string[]
  notAssessed: string[]
}

export type FinalSet = {
  id: string | null
  bytes: number
}

export type PartResource = {
  size?: string
  suppliedService?: boolean
  memoryDefault?: MemoryDefaultSource | null
  memoryUnlimited?: boolean
  part: string
  public: boolean
  need?: boolean
  memory: string
  cpu: string
  runsOn?: string
  override?: Override | null
  overrideApplied: boolean
  observed: PartMemory
}

export type HostShare = {
  target: string
  elastic: boolean
  hostMemory: Figure
  declared: Figure
  used: Figure
  overcommitted: boolean
  lowDisk: boolean
  diskRefuses: boolean
}

export type LimitCount = {
  part: string
  kind: string
  count: number
  last: string
}

export type DeploymentTargetView = {
  displayLabel: string
  name: string
  kind: string
  origin: string
  title: string
  vendor: string
  family: string
  region: string
}

export type DeploymentNeedView = {
  need: string
  name: string
  id?: string
  region?: string
  size?: string
  version?: string
  status: string
}

export type RestoreFill = {
  set?: string | null
  state: string
  detail?: string
  updatedAt: string
}

export type DevStaticBuild = {
  part: string
  context: string
  target?: string
  output: string
  folder: string
}

export type Reading = {
  what: string
  path?: string
  totalBytes?: number
  freeBytes?: number
  floorBytes?: number
  warningBytes?: number
  refuses: boolean
  low: boolean
  unknown?: string
  readAt: string
}

export type DocLinks = {
  origin: DocLink
  mcp: DocLink
  git: DocLink
}

export type DocGuideGroup = {
  title: string
  guides: DocGuideEntry[]
}

export type DocIntegrationEntry = {
  capability: string
  provider: string
  title: string
}

export type Eval = {
  id: string | null
  number: number
  startedBy: string
  startedAt: string
  meaning: string
  subjectKind: string
  subject: string
  subjectDigest?: string
  budgetMicros: number
  state: string
  reason?: string
  finishedAt?: string | null
  arms: Arm[]
}

export type Arm = {
  id: string | null
  sourceProjectId: string | null
  sourceChangeId: string | null
  sourceChangeNumber: number
  arm: string
  profile: string
  models: Record<string, string>
  state: string
  reason?: string
  projectId?: string | null
  changeId?: string | null
  changeNumber: number
  runId?: string | null
  landingRunId?: string | null
  startedAt?: string | null
  readyAt?: string | null
  finishedAt?: string | null
  score: Score
  declares: CheckVersion[]
}

export type ArmCost = {
  costMicros: number
  tokens: number
  incomplete: number
}

export type Tally = {
  measure: string
  better: number
  same: number
  worse: number
  unknown: number
}

export type PackChoice = {
  name: string
  latest: number
  recommended: number
}

export type FinalCopy = {
  backup: string | null
  deployment: string
  takenAt: string
  bytes: number
  dataClass: string
  verified: boolean
}

export type FleetPack = {
  pack: string
  platform?: boolean
  enabled: boolean
  recommended: number
  versions: FleetPackVersion[]
}

export type steeringSection = {
  name: string
  source: string
  bodyMd?: string
  note: string
  bytes: number
  always: boolean
}

export type GrantView = {
  id: string | null
  subjectType: string
  subject: string
  role?: string
  roleTitle?: string
  capability?: string
  scopeType: string
  scopeId: string
  scopeName?: string
  origin: string
  grantedBy: string
  reason?: string
  createdAt: string
  expiresAt?: string
  reviewAt?: string
}

export type Group = {
  name: string
  description: string
  kind: string
  members: string[]
}

export type Block = {
  id: string | null
  name: string
  tags: string
  latestVersion: number
  bodyMd?: string
  origin: string
  createdAt: string
  updatedAt: string
  bytes: number
  tier?: string
  conflicts?: string
  withdrawn?: boolean
}

export type BlockVersion = {
  version: number
  createdAt: string
  source: string
  signer: string
}

export type BlockUse = {
  projectSlug: string
  version: number
  disabled: boolean
}

export type Declaration = {
  name: string
  title?: string
  expects: string
  severity?: string
  evaluator?: string
  sh?: string
  rubric?: string
  probe?: string
  tool?: string
  scope?: string
  files?: string[]
  occurrences?: string
  appliesTo?: string[]
}

export type InfrastructureView = {
  target: string
  kind: string
  family: string
  origin: string
  status: string
  facts: string
  domain?: string
  place: string
  vendor: string
  live: boolean
  error?: string
  unreported?: boolean
  asOf?: string
  refreshing: boolean
  resources: Resource[]
  shared?: string
}

export type ActiveAgent = {
  project: string
  runId: string | null
  changeId: string | null
  title: string
  step: string
  type: string
  elapsedS: number
}

export type ChangeHistory = {
  completedRuns: number
  merged: boolean
  changeId: string | null
  changeNumber: number
  title: string
  type: string
  state: ChangeState
  closedAs?: string
  stateLabel: string
  project?: string
  workflows: string
  runs: number
  when: string
  tookS: number
}

export type ActivityDeployment = {
  project: string
  name: string
  url: string
  hostOnly?: boolean
  running: boolean | null
  observation: string
  observationIssue?: string
  observedAt?: string
  observationAttemptedAt?: string
  observationStale: boolean
}

export type IntegrationEntry = {
  displayLabel: string
  id: string | null
  name: string
  capability: string
  provider: string
  config: Record<string, unknown>
  version: number
  updatedAt: string
  managed: boolean
  configHash: string
  usedBy: IntegrationUse
  health: IntegrationHealth
  testable: boolean
}

export type Invite = {
  id: string | null
  purpose: string
  username: string
  label: string
  role: string
  invitedBy: string
  invitedByPrincipal?: string | null
  createdAt: string
  expiresAt: string
  state: string
}

export type Job = {
  id: number
  kind: string
  key: string
  state: string
  attempts: number
  error?: string
  receipt?: Record<string, unknown>
  createdAt: string
  startedAt?: string
  finishedAt?: string
  runAfter?: string
}

export type WhatIf = {
  id: string | null
  pack: string
  version: number
  requestedBy: string
  requestedByPrincipal?: string | null
  state: string
  detail?: string
  projectsTotal: number
  projectsDone: number
  createdAt: string
  finishedAt: string
  rubrics: boolean
  spendLimitMicros: number
  advanceWorkflows: boolean
  also: PackVersion[]
  costMicros: number
  incomplete: number
  rows: WhatIfRow[]
  projects: WhatIfProject[]
  fleet: WhatIfFleetGap[]
  movesBackProjects: number
}

export type PackVersion = {
  pack: string
  version: number
}

export type LibraryWorkflowUse = {
  project: string
  origin: string
}

export type ModelEntry = {
  pattern: string
  binding: string
  source: string
}

export type UnlistedBinding = {
  binding: string
  error: string
}

export type ModelSlotView = {
  slot: string
  gloss: string
  patterns: Record<string, string>
  fallback?: string
  consumers?: string[]
  spend7d?: Record<string, number>
}

export type ModelSlotProject = {
  slug: string
  name: string
  profile: string
}

export type ModelSlotOverride = {
  project: string
  slot: string
  pattern: string
}

export type OpenVEXStatement = {
  vulnerability: Record<string, string>
  products: OpenVEXProduct[]
  status: string
  justification?: string
  action_statement?: string
  impact_statement?: string
  timestamp: string
}

export type OperationState = "queued" | "running" | "succeeded" | "failed" | "unknown" | "cancelled"

export type OperationStep = {
  name: string
  started: string
  ended?: string | null
  failed?: boolean
  unknown?: boolean
  detail?: string
  bytes?: number
}

export type Override = {
  memoryUnlimited?: boolean
  part: string
  resources: Resources
  reason: string
  setBy: string
  setAt: string
}

export type RedeployNumber = {
  number: number
  deployment: string
}

export type PackSpec = {
  platform?: boolean
  guidance: string[]
  workflows: string[]
  policies: PackPolicy[]
  toolchain?: string
  tools?: Record<string, string>
  services?: ServiceSpec[]
  extensions?: string[]
  requires?: string[]
}

export type Publication = {
  version: number
  name?: string
  basedOn?: number
  declaration: PackSpec
  spec: PackSpec
  pins: PinSet
  note: string
  source: string
  signer: string
  at: string
}

export type PackPolicy = {
  name: string
  description: string
  rule: string
  with: Record<string, unknown>
}

export type ServiceSpec = {
  name: string
  image: string
  env: Record<string, string>
  command?: string[]
  entrypoint?: string[]
  configAtStart?: boolean
  keepsImage?: boolean
  health?: string
  appPart?: boolean
  port?: number
  resources?: Resources
  data?: string
  credentials?: string[]
}

export type packRequirement = {
  capability: string
  bound: boolean
  binding?: string
  provider?: string
}

export type PolicyLibraryEntry = {
  name: string
  description: string
  rule: string
  with: Record<string, unknown>
  origin: string
  adoptedBy: number
  nonConformant: number
  packs: string[]
  reading: string
  fix: string
}

export type PolicyUsage = {
  project: string
  slug: string
  conformant: boolean
  violations: Violation[]
}

export type RecordedRevision = {
  id: string | null
  projectId: string | null
  revision: number
  changeId: string | null
  kind: AdoptionKind
  reason: string
  digest: string
  createdBy: string
  createdByPrincipal?: string | null
  createdAt: string
  receipt: Receipt
}

export type projectPack = {
  pack: string
  version: number
  recommended: number
  platform?: boolean
  reason: string
  adoptedBy: string
  adoptedAt: string
}

export type ReceiptBlock = {
  position: number
  name: string
  blockId: string | null
  version: number
  disabled?: boolean
  tier?: string
}

export type placementCondition = {
  kind: string
  blocks?: string[]
  sentence: string
}

export type Deletion = {
  projectId: string | null
  slug: string
  requestedBy: string
  requestedByPrincipal?: string | null
  requestedAt: string
  plan: unknown
  state: string
  detail: string
  attempts: number
  round: number
  finishedAt?: string | null
}

export type MainStanding = {
  state: string
  recorded?: RecordedMain | null
  hub?: string
  ahead?: number
  note?: string
  upstream?: UpstreamMain | null
}

export type Lifecycle = {
  projectId: string | null
  state: string
  requestedBy?: string
  requestedByPrincipal?: string | null
  bindingId?: string | null
  synthetic?: boolean
  takeDownsStarted?: boolean
  stalled?: string
  requestedAt?: string
  deletedAt?: string
  keptUntil?: string
  purgeStartedAt?: string
}

export type Finding = {
  fingerprint: string
  kind: string
  severity: string
  identifiers?: string[]
  package?: PackageRef | null
  location?: Location | null
  title: string
  detail?: string
  url?: string
  source: string
  reached?: boolean | null
  items?: string[]
  regression?: string
  preExisting?: boolean
  disposition?: Disposition | null
}

export type FindingSource = {
  binding: string
  runId: string | null
  runNumber: number
  changeId: string | null
  changeNumber: number
  at: string
  findings: number
  unreadable?: string
}

export type FindingsRepoState = {
  sha?: string
  committedAt?: string | null
  stale?: boolean | null
}

export type FindingsSummary = {
  total: number
  dispositioned: number
  actionable: number
  bySeverity: Record<string, number>
}

export type Import = {
  position: number
  blockId: string | null
  name: string
  version: number
  latestVersion: number
  disabled: boolean
  bytes: number
  tier?: string
  conflicts?: string
  origin: string
  withdrawn?: boolean
}

export type EffectiveGuidance = {
  text: string
  hash: string
  bytes: number
  generatedBytes: number
  conditionalBytes: number
  contextBytes: number
}

export type LaneView = {
  name: string
  description: string
  tier: string
  origin: string
  version: number
  selected: boolean
  deprecated?: boolean
  withdrawn?: boolean
  runsCount: number
  summary?: workflowSummary | null
}

export type ManifestProject = {
  slug: string
  name: string
}

export type ManifestRepo = {
  backed: boolean
  defaultBranch?: string
  remote?: string
}

export type ManifestPack = {
  name: string
  at: string
}

export type ManifestWorkflow = {
  name: string
  version: number
  origin: string
}

export type ManifestPin = {
  name: string
  version: number
}

export type ManifestBinding = {
  capability: string
  binding: string
  provider: string
  version: number
  configHash: string
}

export type Policy = {
  id: string | null
  name: string
  description: string
  rule: string
  with: Record<string, unknown>
  origin?: string
  createdAt: string
}

export type OverviewRepo = {
  defaultBranch: string
  path: string
  head: OverviewHead
  commits: string
  remote?: string
  cloneCmd?: string
  webUrl?: string
}

export type OpenChange = {
  changeId: string | null
  changeNumber: number
  title: string
  state: ChangeState
  editing: number
}

export type Violation = {
  policy: string
  workflow: string
  detail: string
}

export type DeclaredSecret = {
  name: string
  description: string
  generate: boolean
  domain?: string
  consumers: string[]
  allParts: boolean
  profiles: Record<string, boolean>
}

export type SecretDeployment = {
  name: string
  chosen: string
  profile: string
  secrets: SecretStatus[]
  missing: string[]
}

export type DeclaredNeed = {
  capability: string
  params?: Record<string, string>
}

export type OfferedTarget = {
  displayLabel?: string
  id?: string | null
  settingsRevision: number
  name: string
  kind: string
  kindInfo: KindInfo
  address: Record<string, unknown>
  capabilities: TargetCapabilities
  ingress: Record<string, unknown>
  needsMode: string
  origin: string
  offer: Offer | null
  bound: number
}

export type workflowSummary = {
  description?: string
  pacing: string
  trigger?: string
  flow: string
  stages: workflowStageSummary[]
}

export type Release = {
  id: string | null
  projectId: string | null
  number: number
  state: ReleaseState
  deployment: string
  externalRef?: string
  approvedBy?: string
  runId?: string | null
  createdAt: string
  releasedAt?: string
  notes?: string
  commitSha?: string
  bundleId?: string | null
  deploymentId?: string | null
  generation?: number
  artifactHash?: string
  origin: string
}

export type ReleaseChange = {
  id: string | null
  title: string
  type: string
}

export type ReleaseEvent = {
  at: string
  text: string
}

export type ReleaseBundleView = {
  id: string | null
  byDigest: boolean
  registry?: string
  platform?: string
  parts: Record<string, string>
  init: Record<string, string>
}

export type ReleaseChangeTitle = {
  id: string | null
  title: string
}

export type ReleaseHead = {
  sha: string
  subject: string
}

export type LastRelease = {
  number: number
  at: string
}

export type ShippedChange = {
  id: string | null
  title: string
}

export type Remediation = {
  id: string | null
  number: number
  scope: string
  scopeRef: string
  mode: string
  actor: string
  actorPrincipal?: string | null
  status: string
  concurrency: number
  retries: number
  reviewId: string | null
  createdAt: string
  updatedAt: string
  finishedAt?: string
  counts?: Record<string, number>
  sentence?: string
  cost?: number
}

export type Item = {
  id: string | null
  remediationId: string | null
  projectId: string | null
  projectSlug: string
  checkKey: string
  block: string
  version: number
  check: string
  rank: number
  state: string
  reason?: string
  originVerdictId: string | null
  originSha: string
  originEvaluatorVer?: string
  originEvidenceHash?: string
  originDetail?: string
  attempt: number
  transientRetries: number
  changeId?: string | null
  runId?: string | null
  changeNumber: number
  runNumber: number
  leaseWorker?: string
  leaseUntil?: string
  notBefore?: string
  verifiedReviewId?: string | null
  costMicros: number
  updatedAt: string
  doing?: string
}

export type Attempt = {
  id: string | null
  itemId: string | null
  n: number
  changeId: string | null
  runId: string | null
  baseSha: string
  outcome: string
  costMicros: number
  reason?: string
  startedAt: string
  finishedAt?: string
}

export type Occupancy = {
  kind: string
  workers: number
  busy: number
  holding: Item[]
  queued: number
  cooldownUntil?: string
  cooldownWhy?: string
}

export type RemoteNamespace = {
  id: number
  path: string
}

export type AttachmentRequest = {
  requestId: string | null
  expectedAttachmentId: string | null
  url: string
  create: boolean
  namespaceId: number
  repositoryName: string
  recoverProviderId: number
}

export type RestoreStatus = "restored" | "restored, not verified" | "partly restored"

export type RestoredItem = {
  kind: string
  name: string
  outcome: RestoreOutcome
  detail?: string
  caveat?: string
}

export type RetainedResource = {
  id: string | null
  projectId: string | null
  project?: string
  deployment: string
  targetId: string | null
  target?: string
  need: string
  handle: Record<string, unknown>
  retainedBy: string
  retainedByPrincipal?: string | null
  retainedAt: string
  disposition: RetentionState
  note?: string
  settledBy?: string
  settledByPrincipal?: string | null
  settledAt?: string
  holdsData: boolean
}

export type RoleView = {
  name: string
  title: string
  description: string
  scope: string
  surfaced: boolean
  capabilities: string[]
}

export type CapabilityView = {
  description: string
  risk: string
}

export type UnreadReport = {
  name: string
  kind: string
  reason: string
}

export type TraceWorkflow = {
  name: string
  version: number
  yaml: string
  definition: Spec | null
}

export type Event = {
  id: string | null
  runId: string | null
  at: string
  kind: string
  payload: unknown
}

export type Artifact = {
  id: string | null
  runId: string | null
  kind: string
  name: string
  ref: string
  body: string
  bodyBytes?: number
  createdAt: string
  binary?: boolean
}

export type ScheduleView = {
  source: ScheduleSource
  cron: string
  timeZone: string
  skipIfActive: boolean
  next?: string
  due: boolean
  last?: ScheduleFiringView | null
  problem?: string
}

export type EnvironmentEntry = {
  key: string
  value: string
  browser?: boolean
}

export type ServiceKindEntry = {
  capability: string
  title: string
  description?: string
  icon?: string
  connector: { fields?: IntegrationField[] }
  resource: { fields?: IntegrationField[] }
  env?: EnvironmentEntry[]
  steering?: string
  guidance?: string
  yaml: string
  updatedAt: string
  error?: string
}

export type NeedSelection = {
  capability: string
  params: Record<string, unknown>
}

export type StepState = "pending" | "running" | "pass" | "fail" | "needs_human" | "error" | "skipped" | "not_applicable"

export type SystemWorkflowBinding = {
  stock: string
  name: string
  custom: boolean
  description: string
}

export type ResourceCapacity = {
  total: Figure
  promised: Promised
  inUse?: Split | null
  hostUsed?: Figure | null
  overcommitted: boolean
}

export type DiskCapacity = {
  total: Figure
  free: Figure
  low: boolean
  warning?: number
  refuses: boolean
  floor?: number
  path?: string
  readAt?: string
}

export type Tenant = {
  kind: string
  name: string
  project?: string
  deployment?: string
  containers: number
  used: Figure
  promised: Figure
  unlimited: number
  cpus: number
  cpuUnlimited: number
  cpuUnread: number
}

export type ElasticPart = {
  deployment: string
  part: string
  need?: boolean
  runsOn: string
  usdPerMonth: Figure
}

export type Figure = {
  known: boolean
  value: number
  reason?: string
}

export type TargetState = "ready" | "unreachable" | "retired"

export type TargetView = {
  displayLabel: string
  connectionId?: string | null
  providerScope?: unknown
  id: string | null
  name: string
  kind: string
  provider: string
  address: Record<string, unknown>
  origin: string
  capabilities: TargetCapabilities
  ingress: Record<string, unknown>
  needsMode: string
  status: TargetState
  settingsRevision: number
  kindInfo: KindInfo
  offers: Offer[]
  deployments: TargetDeployment[]
  retained: number
  copies?: Availability | null
}

export type KindInfo = {
  containerMemoryLimits: boolean
  kind: string
  title: string
  provider: string
  family: string
  vendor: string
  region: string
  environment: boolean
}

export type Offer = {
  id: string | null
  targetId: string | null
  group: string
  caps: OfferCaps
  createdBy: string
  createdByPrincipal?: string | null
  createdAt: string
  implicit?: boolean
}

export type TargetEvent = {
  at: string
  actor: string
  kind: string
  payload: unknown
}

export type RegistryReading = {
  registry?: string
  tier?: string
  included: number
  used: number
  full: boolean
  gate?: boolean
  none?: boolean
  unknown?: string
  readAt?: string
}

export type TierView = {
  name: string
  title: string
  gloss: string
  onward?: string
  future?: boolean
  apps: TierApp[]
  observed?: string
  reason?: string
}

export type Token = {
  Name: string
  User: string
  Role: string
  CreatedAt: string
  Purpose: string
  LastUsed: string
  RevokedAt: string
}

export type Totals = {
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  total: number
  costMicros: number
  incomplete: number
}

export type UsageKindLine = {
  kind: string
  input: number
  output: number
  total: number
  costMicros: number
}

export type UsageModelLine = {
  model: string
  input: number
  output: number
  total: number
  costMicros: number
}

export type UsageDayLine = {
  day: string
  input: number
  output: number
  total: number
  costMicros: number
}

export type UsageDayKind = {
  day: string
  kind: string
  total: number
  costMicros: number
}

export type UsageWorkflowLine = {
  workflow: string
  input: number
  output: number
  total: number
  costMicros: number
}

export type UsageProjectLine = {
  project: string
  input: number
  output: number
  total: number
  costMicros: number
}

export type Account = {
  principalId: string | null
  username: string
  role: string
  hasPassword: boolean
  acted: boolean
  lastSeen: string
  disabled: boolean
  groups: string[]
}

export type WorkerChatView = {
  chat: string
  live: boolean
  saved?: ConsoleSave | null
  recordAsOf?: string | null
  refused?: RefusedLastSave | null
}

export type WorkerPool = {
  kind: string
  count: number
  gloss: string
}

export type WorkflowStagePreview = {
  name: string
  steps: string[]
}

export type PackPick = {
  pack: string
  version: number
}

export type Receipt = {
  resolver: number
  packs?: string[]
  guidance: ReceiptBlock[]
  workflows: ReceiptWorkflow[]
  policies: ReceiptPolicy[]
  extensions: string[]
}

export type RuleMove = {
  rule: string
  from: number
  to: number
}

export type CheckSummary = {
  block: string
  check: string
  version?: number
  title?: string
  expects?: string
}

export type CopySource = {
  project: string | null
  revision?: number
}

export type CheckReading = {
  checks?: CheckReceipt[]
  checkErrors?: number
  undecided?: string[]
  placement?: Placement | null
  unreadable?: string
}

export type BackupItem = {
  engineImageID?: string
  kind: string
  name: string
  file: string
  bytes: number
  sha256: string
  error?: string
  how: string
  restore?: string
  volume?: string
  copy?: CopyMethod
  restoreStopped?: boolean
  verify?: string
  caveat?: string
  format?: string
  instance?: string
  database?: string
  bucket?: string
  asUser?: string
  client?: string
}

export type BackupState = "ok" | "partial" | "failed" | "pruned"

export type AddsRow = {
  name: string
  added: string[]
  removed: string[]
  unknown?: string
  baseUnread?: string
}

export type AddsLine = {
  kind: string
  text: string
}

export type IntegrationWire = {
  changeId: string | null
  changeNumber: number
  state: string
  stateLabel: string
  title: string
}

export type ChangeState = "in_progress" | "landing" | "shipped" | "abandoned"

export type CatalogParam = {
  name: string
  description?: string
  required?: boolean
  default?: string
}

export type ChangeSummary = {
  id: string | null
  number: number
  projectId: string | null
  type: string
  title: string
  body: string
  runId: string | null
  state: ChangeState
  branch?: string
  baseSha?: string
  landedSha?: string
  externalRef?: string
  summary?: string
  provenance?: string
  createdAt: string
  closedAt?: string
  closedAs?: string
  stateLabel: string
  targetAdoptionId?: string | null
  expectedRevision?: number
  adoptedRevision?: number
  editRevision: number
  openedBy?: string
  openedByPrincipal?: string | null
  requestMet: string
  requestGap?: string
  stage: string
  runStatus: RunState | ""
  workflowName: string
}

export type SentBack = {
  stepId: string | null
  step: string
  by: string
  note: string
}

export type ShippedComparison = {
  deployment?: string
  release?: number
  commit?: string
  previous?: number
  previousCommit?: string
  since?: number
}

export type TargetCapabilities = {
  persistentVolumes: boolean
  previews: boolean
  ownLiveness: boolean
  rollout: string[]
  needs: string[]
}

export type Fix = {
  id: string | null
  projectId: string | null
  checkKey: string
  check: string
  beforeVerdictId: string | null
  afterVerdictId: string | null
  beforeSha: string
  afterSha: string
  beforeState: Outcome
  beforeDetail?: string
  reviewId: string | null
  reviewNumber: number
  changeIds: (string | null)[]
  changeNumbers: number[]
  commits?: string
  remediationItemId?: string | null
  acceptedBy?: string
  acceptedByPrincipal?: string | null
  also?: string[]
  at: string
}

export type ReviewState = "running" | "done" | "error" | "queued"

export type ReviewDelta = {
  previous: string | null
  comparedProjects: number
  fixed: number
  new: number
  stillOpen: number
  newlyJudged: number
  newlyUnjudged: number
  fixedItems?: string[]
  newItems?: string[]
}

export type Lanes = {
  conforms: number
  excepted: number
  findings: number
  flagged: number
  unassessed: number
  broken: number
  na: number
}

export type DispositionKind = "accepted" | "false_positive"

export type ConnectorField = {
  key: string
  label: string
  type: string
  help?: string
  placeholder?: string
  options?: string
}

export type ConnectorPreset = {
  name: string
  title: string
  icon: string
  description: string
  suggest?: string
  config: Record<string, unknown>
}

export type Reach = {
  how: string
  from: string
  credential: string
}

export type Native = {
  mechanism: string
}

export type Allowance = {
  limit?: number | null
  remaining?: number | null
  reset?: string
  periodSpend: number
  lifetimeSpend: number
  disabled?: boolean
}

export type ContainerFacts = {
  id: string
  status: string
  running: boolean
  exitCode?: number | null
  oomKilled: boolean
  restarts: number
  startedAt?: string
  finishedAt?: string
  health: string
}

export type Availability = {
  available: boolean
  reason?: string
  why?: string
}

export type CopyRef = {
  backup: string | null
  takenAt: string
  bytes: number
  sha256: string
}

export type NativeBackups = {
  mechanism: string
  count: number
  oldest?: string
  newest?: string
  unknown?: string
}

export type DrillRef = {
  at: string
  backup: string | null
  target: string
  place: string
  user: string
  passed: boolean
  detail?: string
}

export type RehearsalFacts = {
  checks: RehearsalCheck[]
  withheld: string[]
  notStarted: string[]
  images: Record<string, string>
  egress: string
  sharedHost?: boolean
  gate?: RehearsalCheck[]
}

export type MemoryDefaultSource = {
  kind: string
  name?: string
}

export type PartMemory = {
  part: string
  need?: boolean
  usedBytes: number
  limitBytes: number
  known: boolean
  reason?: string
}

export type DocLink = {
  url: string
  path: string
}

export type DocGuideEntry = {
  slug: string
  title: string
}

export type Score = {
  readyToLand: boolean
  landed: boolean
  deploy?: string
  checks: CheckResult[]
  requestMet?: string
  requestGap?: string
  reworks: number
  workSeconds?: number
  compared?: ArmCompared | null
  compareError?: string
  notComparable?: boolean
  unreadable?: string
}

export type CheckVersion = {
  block: string
  version: number
}

export type FleetPackVersion = {
  version: number
  projects: string[]
  note?: string
  at: string
}

export type Resource = {
  kind: string
  name: string
  id?: string
  status?: string
  region?: string
  size?: string
  version?: string
  url?: string
  detail?: string
  tags?: string[]
  record: string
  owner?: string
  stack?: string
  hosts?: ResourceHost[]
  retained?: RetainedNote | null
}

export type IntegrationUse = {
  projects: AttachedProject[]
  deployments: ProviderDeployment[]
}

export type WhatIfRow = {
  projectId: string | null
  projectSlug: string
  sha: string
  adoptionRevision: number
  candidateDigest?: string
  block?: string
  check?: string
  currentVersion?: number
  proposedVersion?: number
  current?: string
  proposed?: string
  result: string
  detail?: string
  brief?: string
  history?: AttemptHistory | null
  reason?: string
  joins?: boolean
}

export type WhatIfProject = {
  projectId: string | null
  projectSlug: string
  sha?: string
  adoptionRevision?: number
  candidateDigest?: string
  joins?: boolean
  outcome: string
  newFailures: WhatIfCheck[]
  notAssessed: WhatIfCheck[]
  fixed: number
  score: WhatIfScore
  notAssessedBy: WhatIfReasonCount[]
  moves: WhatIfMove[]
}

export type WhatIfFleetGap = {
  block: string
  check: string
  reason: string
  words: string
  remedy: string
  projects: number
}

export type OpenVEXProduct = {
  "@id": string
}

export type Resources = {
  memoryBytes?: number
  cpus?: number
}

export type PinSet = {
  blocks: Record<string, number>
  workflows: Record<string, number>
  policies?: Record<string, string>
}

export type AdoptionKind = "create" | "edit" | "adopt" | "target" | "legacy-snapshot"

export type RecordedMain = {
  branch: string
  sha: string
  via: string
  runId?: string | null
  recordedAt: string
}

export type UpstreamMain = {
  known: boolean
  ahead?: number
  behind?: number
  note?: string
}

export type PackageRef = {
  name: string
  version?: string
  ecosystem?: string
  fixedIn?: string
}

export type Location = {
  file: string
  line?: number
}

export type OverviewHead = {
  sha: string
  subject: string
  when: string
}

export type SecretStatus = {
  name: string
  required: boolean
  bound: boolean
  store?: string
  ref?: string
  version?: string
  generated?: boolean
  default?: boolean
  updatedAt?: string
  updatedBy?: string
}

export type workflowStageSummary = {
  name: string
  steps: workflowStepSummary[]
  gateText: string
}

export type RestoreOutcome = "verified" | "loaded" | "copied" | "failed" | "skipped"

export type RetentionState = "retained" | "released" | "kept"

export type Spec = {
  v?: number
  kind: string
  role?: string
  on?: On
  workspace?: string
  defaults?: Defaults
  stages: SpecStage[]
}

export type ScheduleSource = {
  workflow: string
  version: number
}

export type ScheduleFiringView = {
  at: string
  outcome: "opened" | "skipped" | "failed" | "unknown"
  changeId?: string | null
  changeNumber: number
}

export type Promised = {
  sum: Split
  limited: Count
  unlimited: Count
  unread: Count
}

export type Split = {
  apptown: Figure
  other: Figure
}

export type TargetDeployment = {
  project: string
  name: string
  url: string
  generation: number
  readiness: string
  audience: string
  data: string
}

export type OfferCaps = {
  maxStanding?: number
  audiences?: string[]
  data?: string[]
}

export type TierApp = {
  deploymentId?: string | null
  deploymentLabel?: string
  slug: string
  name: string
  icon?: string
  detail?: string
  url?: string
  hostOnly?: boolean
  running: boolean
  observed?: string
  static?: boolean
  changeId?: string | null
  changeNumber?: number
}

export type RefusedLastSave = {
  commit: string
  reason: string
  at: string
}

export type ReceiptWorkflow = {
  name: string
  version: number
  origin: string
  yamlHash: string
  system?: boolean
}

export type ReceiptPolicy = {
  name: string
  rule: string
  configHash: string
  origin?: string
}

export type CheckReceipt = {
  scanner?: string
  carried?: number
  carriedNote?: string
  baseNote?: string
  block: string
  version: number
  check: string
  evaluator: string
  verdict: CheckVerdict
  exitCode?: number | null
  durationMs?: number | null
  output?: string
  detail?: string
  note?: string
  tool?: string
  exceptedBy?: string
  scope?: string
  gates?: boolean | null
  evaluatorVer?: string
  evidenceHash?: string
  evidence?: string
  files?: string[]
  occurrences?: string[]
  apptownFault?: boolean
  preexisting?: boolean
  undecided?: boolean
  image?: string
}

export type Placement = {
  worker?: string
  local?: boolean
}

export type CopyMethod = "declared" | "files" | "live-files"

export type RehearsalCheck = {
  name: string
  path: string
  status: number
  passed: boolean
}

export type CheckResult = {
  step: string
  type: string
  status: string
}

export type ArmCompared = {
  head: string
  base: CheckAnswer[]
  arm: CheckAnswer[]
  rows: CheckRow[]
}

export type ResourceHost = {
  projectId?: string | null
  deploymentId?: string | null
  project: string
  deployment: string
  generation?: number
  need?: string
}

export type RetainedNote = {
  id: string | null
  project: string
  deployment: string
  retainedBy: string
  retainedAt: string
  reason?: string
}

export type AttachedProject = {
  slug: string
  name: string
  capability: string
}

export type ProviderDeployment = {
  project: string
  name: string
  target: string
}

export type AttemptHistory = {
  attempts: number
  medianCost: number
  medianMinutes: number
  succeededShare: number
}

export type WhatIfCheck = {
  block: string
  check: string
  exceptedToday?: boolean
  reason?: string
  why?: string
}

export type WhatIfScore = {
  checks: number
  judged: number
  passed: number
  failing: number
  couldNotAssess: number
  noLongerRequired: number
  notApplicable: number
  newFailures: number
  fixed: number
  movesBack: number
  ruleChanges: number
}

export type WhatIfReasonCount = {
  reason: string
  words: string
  remedy: string
  count: number
}

export type WhatIfMove = {
  rule: string
  from: number
  to: number
  back: boolean
}

export type workflowStepSummary = {
  name: string
  uses: string
  icon: string
  gloss: string
  human?: boolean
  means?: string
}

export type On = {
  manual?: Record<string, unknown>
  schedule?: ScheduleTrigger | null
}

export type Defaults = {
  model?: string
  thinking?: string
  executor?: string
  image?: string
}

export type Count = {
  apptown: number
  other: number
}

export type CheckVerdict = "pass" | "fail" | "error" | "cannot-assess" | "skipped" | "excepted" | "unclear" | "not-applicable"

export type CheckAnswer = {
  block: string
  version: number
  check: string
  kind: string
  state: string
  reason?: string
  detail?: string
  findings?: Finding[]
}

export type CheckRow = {
  block: string
  version: number
  check: string
  kind: string
  base: string
  arm: string
  new?: string[]
  fixed?: string[]
  alreadyThere?: string[]
  notComparable?: string
}

export type ScheduleTrigger = {
  cron: string
  skipIfActive?: boolean | null
}
