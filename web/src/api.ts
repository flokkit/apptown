// Typed client for the apptownd REST API.

// Response shapes come from the server's own types (wire.gen.ts, generated
// from the Go structs). A type here is either one of those, or derived from
// one where the UI reads it more precisely (a step's evidence by its type);
// api-types.test.ts holds that rule.
import type { ChangeState as WireChangeState, RunState, StepState } from './wire.gen'
import { ENTRY_HEADER, noteServed } from './pageUpdate'
import type { Artifact as WireArtifact, Change as WireChange, Project as WireProject, Run as WireRun, Step as WireStep } from './wire.gen'
import type { ADRChangeSummary, ADRRecord, AdvanceResult as WireAdvanceResult, ChangeCockpit, ChangeRunSummary, CockpitEvidence, DashboardView, Event as WireEvent, ReleaseListed, RunSnapshot, ShippedComparison } from './wire.gen'
import type { Block, GuidanceBlockDetail, Import, ProjectGuidance as GeneratedProjectGuidance, ProjectWorkflowRow, steeringSection, workflowSummary } from './wire.gen'
import type { LibraryWorkflow as GeneratedLibraryWorkflow } from './wire.gen'

// ---- state vocabularies ------------------------------------------------------
// The server's words for where things are, typed so a rename is a compile
// error and not a silently wrong branch (debt review, finding 26).

// A change's lifecycle (design/subsystems/change-lifecycle.md).
import type { Finding as WireFinding } from './wire.gen'
import type { ChangeAdds } from './wire.gen'

export type ChangeState = WireChangeState
// A run's stored status, generated from the server's run states (there is no
// "failed": a run that fails stops running as done or abandoned). "running" is
// honest only with a Doing beside it.
export type RunStatus = RunState
// What a running run is actually doing right now — derived by the server
// (overview.go runState), never stored.
export type Doing = 'waiting on you' | 'blocked' | 'agent working' | 'working' | 'queued' | 'ready to advance'
// A step's status.
export type StepStatus = StepState

export type Project = WireProject

export type WorkflowSummary = workflowSummary

// A project's workflow as the list shows it; its tier is one of three words.
export type Workflow = Omit<ProjectWorkflowRow, 'tier'> & { tier: 'core' | 'extra' | 'system' }

export type LibraryWorkflow = GeneratedLibraryWorkflow

export type Change = WireChange

export type Release = ReleaseListed

// ---- steps and their evidence ----------------------------------------------
// Evidence is what a step leaves behind; its shape depends on the step type
// (engine.go and the per-type runners). Discriminated by Step.type so the
// UI reads named fields instead of casting.

type EvidenceBase = {
  reason?: string; durationMs?: number
  logTail?: string
  // The commit it judged. Where it ran is the step's checkReading.
  candidate?: string
}
export type ApprovalWithdrawal = { by: string; reason: string; at: string }
export type ApprovalEvidence = EvidenceBase & {
  withdrawal?: ApprovalWithdrawal; previousWithdrawals?: ApprovalWithdrawal[]
  lastRejection?: { approver: string; comment: string; at: string }
  approver?: string; decision?: 'approve' | 'reject'; comment?: string; at?: string
  // adds: what the commit added as this vote was shown it, recorded with
  // the vote (an approval of a commit).
  // via: the way the vote was cast (a CLI token's name, a session).
  decisions?: { approver: string; via?: string; decision: string; comment?: string; at?: string; adds?: ChangeAdds }[]
  quorum?: number; approvals?: number
  means?: string
  // Set when an eval's start settled this approval in one of its throwaway
  // copies: no person approved it, and it is not acceptance.
  evalAuthorised?: string
}
export type AgentEvidence = EvidenceBase & {
  provider?: string; model?: string; output?: string
  guidanceHash?: string; skills?: Record<string, unknown>
  files?: string[]
}
export type ReviewEvidence = AgentEvidence & {
  verdict?: string; summary?: string; findings?: string[]; reviewedReport?: string
  // Set when AppTown's own file check failed a review the model passed:
  // the model's own verdict and summary, kept apart from the step's.
  reviewerVerdict?: string; reviewerSummary?: string
}
export type ScriptEvidence = EvidenceBase & { log?: string; exitCode?: number; command?: string }
// What the tests' report said, when one was read; otherwise testReport says
// why the counts are not known, and testReportUnreadable is the reason a
// report that was written could not be read.
export type TestsEvidence = ScriptEvidence & {
  testCounts?: { passed: number; failed: number; skipped: number }
  skippedTests?: string[]; testReport?: string; testReportUnreadable?: string; testReportFormat?: string
}
export type ScanEvidence = EvidenceBase & {
  binding?: string; bindingVersion?: number; image?: string; exitCode?: number
  configHash?: string; note?: string
  findings?: { total: number; actionable: number; dispositioned: number }
}
// A boot step's screenshots (workflowexec.screenshots): AppTown's own
// pictures of the preview it booted, from commit, each an artifact.
export type PreviewScreenshots = {
  commit?: string
  shots?: { artifact: string; path: string; view: string }[]
  missing?: string[]
  ignored?: string[]
  unavailable?: string
}
export type ServeEvidence = EvidenceBase & { url?: string; commit?: string; screenshots?: PreviewScreenshots }
// Its checks are the step's checkReading, decoded by the server.
export type ConformanceEvidence = EvidenceBase & {
  findings?: string[]
  findingsSummary?: { total: number; actionable: number; dispositioned: number }
}
export type CallEvidence = EvidenceBase & { childRunId?: string; childRunNumber?: number; workflow?: string; workflowVersion?: number }
export type RequireEvidence = EvidenceBase & {
  adrs?: { id: string; number: number; title: string }[]; requiredStatus?: string
  artifactId?: string; kind?: string; name?: string; ref?: string
}
export type PolicyEvidence = EvidenceBase & { violations?: { policy: string; reading: string }[] }

export type StepConfig = Record<string, unknown>
// An approval step's config: what approving MEANS (plain words) and any
// instructions for the approver, straight from the workflow YAML.
export type ApprovalConfig = StepConfig & { means?: string; instructions?: string; approvers?: string[]; quorum?: number }

// The server's step, with config and evidence typed by the step's type below.
type StepBase = Omit<WireStep, 'type' | 'config' | 'evidence'>

export type Step =
  | (StepBase & { type: 'approval'; config: ApprovalConfig; evidence: ApprovalEvidence })
  | (StepBase & { type: 'agent/pi'; config: StepConfig; evidence: AgentEvidence })
  | (StepBase & { type: 'agent/review'; config: StepConfig; evidence: ReviewEvidence })
  | (StepBase & { type: 'script'; config: StepConfig; evidence: ScriptEvidence })
  | (StepBase & { type: 'tests'; config: StepConfig; evidence: TestsEvidence })
  | (StepBase & { type: 'scan'; config: StepConfig; evidence: ScanEvidence })
  | (StepBase & { type: 'preview'; config: StepConfig; evidence: ServeEvidence })
  | (StepBase & { type: 'deploy'; config: StepConfig; evidence: ServeEvidence })
  | (StepBase & { type: 'guidance/conformance'; config: StepConfig; evidence: ConformanceEvidence })
  | (StepBase & { type: 'workflow/call'; config: StepConfig; evidence: CallEvidence })
  | (StepBase & { type: 'adr/require'; config: StepConfig; evidence: RequireEvidence })
  | (StepBase & { type: 'artifact/require'; config: StepConfig; evidence: RequireEvidence })
  | (StepBase & { type: 'scm/merged'; config: StepConfig; evidence: RequireEvidence })
  | (StepBase & { type: 'policy/current'; config: StepConfig; evidence: PolicyEvidence })
  | (StepBase & { type: 'reconcile'; config: StepConfig; evidence: EvidenceBase })

export type StepType = Step['type']
export type StepEvidence = Step['evidence']
// The step of one type, for predicates: `steps.find(isStep('approval'))`.
export type StepOf<T extends StepType> = Extract<Step, { type: T }>
export const isStep = <T extends StepType>(type: T) => (s: Step): s is StepOf<T> => s.type === type

export type Run = WireRun

// An event's payload is read by name on the page.
export type RunEvent = Omit<WireEvent, 'payload'> & { payload: Record<string, unknown> }
export type Artifact = WireArtifact

// A run snapshot as the page reads it: the run and its steps are always
// present in a snapshot (the Go side holds them by pointer), steps are typed
// by their type, and the state is one of the Doing words.
export type RunDetail = Omit<RunSnapshot, 'run' | 'steps' | 'events' | 'state'> & {
  run: Run; steps: Step[]; events: RunEvent[]; state?: Doing
}

export type AdrLite = ADRChangeSummary

export type Adr = ADRRecord

// The change page's payload (api.go handleGetChange): the change, its
// latest run in full, and the cockpit extras — every root run, the branch's
// commits, a diff stat, the latest change report, files being edited right
// now, and what the run is doing. Shared by the change page and the chat.
export type RunLite = ChangeRunSummary
export type ChangeEvidence = CockpitEvidence
// The change page's payload (httpapi ChangeCockpit), with its run detail as
// the page reads it, its state as a Doing word, and the committed comparison's
// outcomes named ('no-code': a shipped change with no code of its own, which
// shipped describes).
export type ChangeView = Omit<ChangeCockpit, 'detail' | 'state' | 'committedDiff'> & {
  detail: RunDetail | null
  state: Doing | ''
  committedDiff: { status: 'empty' | 'changed' | 'unavailable' | 'no-code'; message?: string; shipped?: ShippedComparison | null }
}

// A security finding (design/subsystems/findings.md): the normalized view over a scan
// report, with its disposition when a suppression matches.
// A finding's shape is the server's (wire.gen.ts).
export type Finding = WireFinding

// The blocking steps typed by their type (WriteJSON never sends null lists).
export type AdvanceResult = Omit<WireAdvanceResult, 'blocking'> & { blocking: Step[] }

export type Dashboard = DashboardView

export type GuidanceBlock = Block

export type GuidanceImport = Import

// A machine-written section of an agent step's steering. always: in every
// step on the project; otherwise only steps of the shape the note names.
export type SteeringSection = steeringSection

export type ProjectGuidance = GeneratedProjectGuidance

export type GuidanceCheck = {
  name: string; title?: string; expects: string; severity: string; evaluator: string
  sh?: string; rubric?: string; tool?: string; probe?: string
  scope?: 'tree' | 'diff'; files?: string[]
}

export type BlockView = GuidanceBlockDetail

// HttpError is a response the server gave with a failure status: the message
// is the server's words, and the status says whether asking again could help
// (a 5xx might pass; a 403 or 404 will not).
// code is the server's stable name for the failure, when it gives one (such
// as settings-moved), for a page that answers one failure specially.
// body is the whole response the server sent with the failure, for a page
// whose failure carries a receipt (a restore that partly failed says which
// items did).
export class HttpError extends Error {
  readonly status: number
  readonly code: string
  readonly body: unknown
  constructor(status: number, message: string, code = '', body: unknown = undefined) {
    super(message)
    this.name = 'HttpError'
    this.status = status
    this.code = code
    this.body = body
  }
}

async function failure(res: Response): Promise<HttpError> {
  const body = await res.json().catch(() => ({ error: res.statusText }))
  return new HttpError(res.status, (body as { error?: string }).error || res.statusText, res.headers.get('X-AppTown-Deny-Code') ?? '', body)
}

async function api<T>(path: string, opts?: RequestInit): Promise<T> {
  // A FormData body carries its own multipart content type (with the
  // boundary the browser picks); setting JSON over it breaks the upload.
  const headers: Record<string, string> = { 'X-AppTown-Actor': currentUser() }
  if (opts?.body instanceof Blob) headers['Content-Type'] = 'application/octet-stream'
  else if (!(opts?.body instanceof FormData)) headers['Content-Type'] = 'application/json'
  const res = await fetch(`/api/v1${path}`, { headers, ...opts })
  noteServed(res.headers.get(ENTRY_HEADER))
  if (!res.ok) throw await failure(res)
  if (res.status === 204) return undefined as T
  const text = await res.text()
  return (text ? JSON.parse(text) : undefined) as T
}

// A read may carry the query's cancellation signal, so a read nobody waits
// for any more (a page left, an identity changed) stops at the network.
export const get = <T,>(p: string, init?: { signal?: AbortSignal }) => api<T>(p, init)
// Every write is announced by path to whoever listens while it is sent, so an
// act can refresh the resources it changed (data.ts, useAction) without each
// page listing them.
export const writeListeners = new Set<(path: string) => void>()
const write = <T,>(p: string, init: RequestInit) => { writeListeners.forEach((l) => l(p)); return api<T>(p, init) }
export const post = <T,>(p: string, body?: unknown) =>
  write<T>(p, { method: 'POST', body: body instanceof FormData ? body : JSON.stringify(body ?? {}) })
// upload sends a file as the request's whole body (a Git bundle).
export const upload = <T,>(p: string, file: Blob) => write<T>(p, { method: 'POST', body: file })
export const patch = <T,>(p: string, body: unknown) =>
  write<T>(p, { method: 'PATCH', body: JSON.stringify(body) })
export const put = <T,>(p: string, body: unknown) =>
  write<T>(p, { method: 'PUT', body: JSON.stringify(body) })
export const del = <T,>(p: string, body?: unknown) =>
  write<T>(p, { method: 'DELETE', ...(body !== undefined ? { body: JSON.stringify(body) } : {}) })
// A plain-text resource (a diff), same door as everything else.
export async function getText(path: string, init?: { signal?: AbortSignal }): Promise<string> {
  const res = await fetch(`/api/v1${path}`, { headers: { 'X-AppTown-Actor': currentUser() }, signal: init?.signal })
  noteServed(res.headers.get(ENTRY_HEADER))
  if (!res.ok) throw await failure(res)
  return res.text()
}

export function currentUser(): string {
  return localStorage.getItem('apptown.user') || 'trial'
}
export function setCurrentUser(u: string) {
  localStorage.setItem('apptown.user', u)
}

export function findingFixPayload(f: {
  identifiers?: string[]; title: string; detail?: string; source: string; fingerprint: string
  package?: { name?: string; version?: string; fixedIn?: string } | null
  location?: { file: string; line?: number } | null
}) {
  const id = f.identifiers?.[0] ?? f.title
  const loc = f.location ? `${f.location.file}:${f.location.line ?? ''}` : (f.package ? `${f.package.name}@${f.package.version}` : '')
  const body = [
    `Fix the security finding ${id} reported by ${f.source}.`,
    loc ? `Location: ${loc}` : '',
    f.detail ? `Finding: ${f.detail}` : `Finding: ${f.title}`,
    f.package?.fixedIn ? `Known fix version: ${f.package.fixedIn}.` : '',
    `Make the minimal change that resolves the finding without changing behavior.`,
    `Verification: after this merges, re-running the ${f.source} scan must no longer report fingerprint ${f.fingerprint}.`,
  ].filter(Boolean).join('\n')
  return {
    title: `Fix ${id}${loc ? ` in ${loc}` : ''}`.slice(0, 120),
    body,
    intent: `Remediation of ${f.source} finding ${id} (fingerprint ${f.fingerprint}), started from the project's Security page.`,
  }
}

// Open a tab whose URL must be fetched first. Calling window.open AFTER an
// await is outside the user-gesture call stack and gets popup-blocked (the person
// sees an unexplained "pop-up blocked" bar) — so claim the tab synchronously in
// the click, show a friendly holding page, and steer it once the URL is
// known. On failure the tab closes and the error propagates to the caller.
export async function openAfter(fetchUrl: () => Promise<string>): Promise<void> {
  const tab = window.open('', '_blank')
  if (tab) {
    tab.document.write('<title>starting…</title><body style="font:16px system-ui;background:#F6F4EE;color:#1C1B18;display:flex;align-items:center;justify-content:center;height:100vh;margin:0">Starting the app…</body>')
  }
  try {
    // An empty answer is a place no tab can open (an address on AppTown
    // host only): the waiting tab closes and the page says where it is.
    const url = await fetchUrl()
    if (!url) tab?.close()
    else if (tab) tab.location.href = url
    else window.open(url, '_blank')
  } catch (e) {
    tab?.close()
    throw e
  }
}
