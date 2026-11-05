import { DeploySceneScope } from './DeployScene'
import { DeployResult } from './DeployResult'
import { Now, nowRunning } from './Now'
import { ContinueDecision, TurnDecision, viewerTurn } from './Turn'
import { HeldDecision, heldLabels } from './HeldDecision'
import { StaleNote } from '../../ReadGate'
import { InlineHelp } from '../../InlineHelp'
import { Link, useNavigate } from 'react-router-dom'
import { QuietConfirm } from '../../QuietConfirm'
import { DecisionContextOnly } from '../../DecisionChoices'
import { post, type AdvanceResult, type ChangeView, type Step } from '../../api'
import { Screenshots } from '../../Screenshots'
import { TryIt } from '../../TryIt'
import { DecisionForm, landingMeansFor } from '../../DecisionForm'
import { useAction, useGet } from '../../data'
import { fmtPrice } from '../../format'
import { TechnicalWords, useTechnicalDetail } from './TechnicalDetail'
import type { DeploymentView } from '../../wire.gen'
import type { ChatHistoryView, ApprovalQuestion } from '../../wire.gen'
import { AddsSection, contractUnread, contractChanged } from '../../DecisionForm'
import { shortDiffstat, agentReport, changeReport, firstLine } from './Head'
import { checkTypes, stepTitle, testsNote } from './Evidence'
import { latestBoot, requestShortfall, requestUnchecked } from './StatePanel'
import { DecisionChoices } from '../../DecisionChoices'
import { useState } from 'react'
import { nextStep, checksRunning, offered, reviewText } from './next'

const checkProblems = (s: Step) => s.checkReading?.checks?.filter(c => !['pass', 'not-applicable', 'skipped'].includes(c.verdict)) ?? []
const checkNotes = (steps: Step[]) => steps.filter(s => s.type !== 'approval' && s.status !== 'not_applicable').flatMap(s => checkProblems(s).filter(c => c.gates === false))

// Non-applicable checks count as fine here; advisory problems remain notes.
export function checkAttention(steps: Step[]) {
  return steps.filter(s => s.type !== 'approval' && s.status !== 'not_applicable' && (
    s.overrideBy || ['unknown', 'excepted'].includes(s.statusLabel) || s.checkReading?.unreadable || s.checkReading?.undecided?.length || s.checkReading?.checkErrors
    || checkProblems(s).some(c => c.gates !== false)
    || (s.type === 'tests' && s.status === 'pass' && !testsNote(s.evidence).plain)
    || (checkTypes.has(s.type) && s.status !== 'pass')
    || ['fail', 'error', 'needs_human', 'skipped'].includes(s.status)
  ))
}

const failedStep = (s: Step) => !s.overrideBy && (['fail', 'error', 'needs_human'].includes(s.status) || !!s.checkReading?.checks?.some(c => c.verdict === 'fail' && c.gates !== false))

// One verdict for the whole change: clear green when every check passed,
// clear red when one failed. Running and unknown stay distinct from both.
export function checksVerdict(steps: Step[]): { tone: 'ok' | 'bad' | 'unknown' | 'neutral'; text: string } | undefined {
  if (!steps.some(s => checkTypes.has(s.type) && !['pending', 'skipped'].includes(s.status))) return undefined
  const attention = checkAttention(steps)
  const failed = attention.filter(failedStep)
  if (failed.length) return { tone: 'bad', text: failed.length === 1 ? `${stepTitle(failed[0])} failed` : `${failed.length} checks failed` }
  if (attention.some(s => !s.overrideBy && !['running', 'pending'].includes(s.status))) return { tone: 'unknown', text: 'Checks not all known' }
  if (attention.some(s => !s.overrideBy && ['running', 'pending'].includes(s.status))) return { tone: 'neutral', text: 'Checks running' }
  const notes = checkNotes(steps).length
  return { tone: 'ok', text: `All checks passed${notes ? ` · ${notes} ${notes === 1 ? 'note' : 'notes'}` : ''}` }
}

export function CheckSummary({ steps, contractNeedsAttention = false, technical = false }: { steps: Step[]; contractNeedsAttention?: boolean; technical?: boolean }) {
  const checks = steps.filter(s => s.type !== 'approval' && (checkTypes.has(s.type) || s.status === 'not_applicable'))
  const attention = checkAttention(steps)
  if (!steps.some(s => checkTypes.has(s.type) && !['pending', 'skipped'].includes(s.status))) return <span>Not checked yet. Checks run when you press Check and land in the chat.</span>
  const running = attention.filter(s => !s.overrideBy && ['running', 'pending'].includes(s.status))
  const unfinished = running.map(stepTitle)
  const notes = checkNotes(steps)
  return <div className="ch-check-summary">
    {!!unfinished.length && <p>Still running: {unfinished.join(', ')}</p>}
    {!attention.length && !unfinished.length && !contractNeedsAttention && <span>{checks.length ? <><span className="ch-mark ok">✓</span> All checks passed</> : 'No verification checks recorded.'}</span>}
    {attention.filter(s => !running.includes(s)).map((s, i) => {
      const problems = checkProblems(s).filter(c => c.gates !== false).map(c => c.detail || `${c.check}: ${c.verdict}`)
      const recorded = s.overrideBy ? `An exception was recorded by ${s.overrideBy}: ${s.overrideReason}` : s.checkReading?.unreadable ? `The result could not be read: ${s.checkReading.unreadable}` : ['unknown', 'excepted'].includes(s.statusLabel) ? `The result is ${s.statusLabel}: ${s.evidence.reason || 'The recorded result needs review.'}` : s.evidence.reason
      const log = (s.type === 'script' || s.type === 'tests') ? s.evidence.log?.trim().split('\n')[0] : ''
      const rejected = s.type === 'agent/review' && !s.overrideBy && ['fail', 'error', 'needs_human'].includes(s.status)
      const review = rejected ? reviewText(s).replace(/\s+/g, ' ').trim() : ''
      const excerpt = review.length > 240 ? review.slice(0, 239) + '…' : review
      const reason = (recorded || problems[0] || log || '').split(/(?<=[.!?])\s+/)[0]
      const countUnknown = s.type === 'tests' && s.status === 'pass' && !s.overrideBy && !s.checkReading?.unreadable && !['unknown', 'excepted'].includes(s.statusLabel) && !s.evidence.testCounts && !s.evidence.testReportUnreadable
      return <p className="ch-check-exception" key={s.id ?? i}>{rejected ? <><strong className="ch-bad-text"><span className="ch-mark bad">✗</span> Review failed</strong> {excerpt}</> : <>{stepTitle(s)}{countUnknown ? <>: number unknown{reason && ` · ${reason}`}</> : <> {s.overrideBy ? 'excepted' : ['fail', 'error', 'needs_human'].includes(s.status) || s.checkReading?.checks?.some(c => c.verdict === 'fail' && c.gates !== false) ? 'failed' : s.status === 'skipped' ? 'not run' : 'unknown'}{reason && `: ${reason}`}</>}</>}
        {s.type === 'tests' && !countUnknown && !testsNote(s.evidence).plain && <> {testsNote(s.evidence).note}</>}
        {!!s.checkReading?.undecided?.length && <> {s.checkReading.undecided.join(', ')}: unknown.</>}
        {!!s.checkReading?.checkErrors && <> {s.checkReading.checkErrors} checks could not run.</>}
        <TechnicalWords shown={technical}>{(s.type === 'script' || s.type === 'tests') && s.evidence.command}</TechnicalWords>
      </p>
    })}
    {notes.map((c, i) => <p key={`note-${i}`}>Note: {c.detail || c.note || 'An advisory check needs review.'}</p>)}
    {contractNeedsAttention && <p className="ch-check-exception">Changes to the app contract need checking.</p>}
  </div>
}

export function ChangeOverview({ view, onChange, workTab, steps, extraErr, current, shown = true }: {
  view: ChangeView; onChange: () => void; workTab: (step?: string | null, run?: string | null) => string
  steps: Step[]; extraErr?: string; current: boolean; shown?: boolean
}) {
  const nav = useNavigate()
  const { run: act, busy: actionBusy, err: actionErr } = useAction(onChange)
  const turn = viewerTurn(view) && current && !extraErr && nextStep(view).primary !== 'open-decision'
  const landingDeploy = [...steps].reverse().find((s): s is Extract<Step, { type: 'deploy' }> => s.type === 'deploy' && !['pending', 'skipped', 'not_applicable'].includes(s.status) && !!view.runs?.some(r => r.id === s.runId && r.role === 'landing'))
  const resultOnOverview = landingDeploy && !turn
  const deployDecision = landingDeploy && nextStep(view).step?.id === landingDeploy.id
  const ch = view.change
  const d = view.detail
  const next = nextStep(view)
  const approval = next.primary === 'approve' && next.step?.type === 'approval' ? next.step : undefined
  const busy = checksRunning(d?.steps ?? [], d?.run.currentStage)
  const deployments = useGet<DeploymentView[]>(`/projects/${view.projectSlug}/deployments`)
  const deciding = approval && turn && !busy && current && !extraErr && deployments.current
  const question = useGet<ApprovalQuestion>(approval ? `/steps/${approval.id}/question` : null)
  const summary = changeReport({ ...view, report: '' }, true)
  const agent = agentReport(changeReport(view) ?? '')
  const made = summary || agent
  const brief = useGet<{ brief: string }>(approval && turn ? `/steps/${approval.id}/brief` : null)
  const briefParts = approvalBrief(brief.data?.brief)
  const transcript = useGet<ChatHistoryView>(!made ? `/changes/${ch.id}/transcript` : null, { poll: 3000 })
  const reply = transcript.data?.sessions?.flatMap(s => s.messages).filter(m => m.role === 'helper' && m.text?.trim()).at(-1)?.text
  const [advance, setAdvance] = useState<AdvanceResult | null>(null)
  const [note, setNote] = useState('')
  const review = [...steps].reverse().find(s => s.type === 'agent/review')
  const boot = latestBoot(steps)
  const saved = steps.flatMap(s => s.type === 'approval' ? (s.evidence.decisions ?? []).filter(d => d.adds) : []).at(-1)
  const adds = approval ? (question.current ? question.data?.adds ?? undefined : undefined) : saved?.adds
  const shortfall = requestShortfall(ch)
  const unchecked = requestUnchecked(ch)
  const [technical] = useTechnicalDetail()
  const destination = deployments.current ? deployments.data?.find(e => e.feed === 'landing') : undefined
  const report = made?.split(/\n+/).map(s => s.replace(/^\s*(?:[-*•]|\d+[.)])\s+/, '').trim()).filter(s => s && !s.startsWith('#')).flatMap(s => s.split(/(?<=[.!?])\s+/)).slice(0, 4).map(line => firstLine(line))
  const checks = steps.filter(s => checkTypes.has(s.type))
  const checked = checks.some(s => !['pending', 'skipped'].includes(s.status))
  const detail = workTab()
  const trailer = (href: string, text: string) => <Link className="ch-trailer" to={href}>{text}</Link>
  return <DeploySceneScope key={ch.id}><div className="ch-overview">
    {resultOnOverview && <DeployResult key={landingDeploy.id} view={view} step={landingDeploy} hidden={!shown} />}
    <div hidden={!shown}><Now view={view} /></div>
    <section hidden={!shown} className="ch-change-card" aria-label="The change">
    <div className="ch-overview-line"><div className="ch-row-label">Preview</div><div className="ch-value ch-preview">
      {!!boot?.evidence.screenshots?.shots?.length && <Screenshots hero unfolded shots={boot?.evidence.screenshots} head={view.head} at={boot?.finishedAt} />}
      <TryIt changeId={ch.id!} label="Open it and try it" />
      {!boot?.evidence.screenshots?.shots?.length && <p className="small muted">{boot?.evidence.screenshots?.unavailable || 'No screenshots were recorded for this preview.'}</p>}
    </div>{trailer(detail + '#preview', `${boot?.evidence.screenshots?.commit ? `built from ${boot.evidence.screenshots.commit.slice(0, 7)} · ` : ''}logs →`)}</div>
    <div className="ch-overview-line ch-request"><div className="ch-row-label">You asked</div><div className="ch-value">{firstLine(ch.body || ch.title)}</div>{trailer(detail + '#request-text', (ch.body || ch.title) !== firstLine(ch.body || ch.title) ? 'full request →' : 'request →')}</div>
    <div className="ch-overview-line"><div className="ch-row-label">{summary ? 'What was made' : 'The agent says'}</div><div className="ch-value">{report?.length ? <ul>{report.map((line, i) => <li key={i}>{line}</li>)}</ul> : reply ? <p className="ch-chat-excerpt">{reply.split('\n').slice(0, 4).join('\n')}</p> : <p className="muted">{transcript.err ? `The conversation could not be read: ${transcript.err}` : transcript.data?.unreadable ? 'Some of the conversation could not be read.' : !transcript.data ? 'Reading the conversation…' : 'No report from the agent'}</p>}
      {!summary && <StaleNote q={transcript} />}<TechnicalWords shown={technical}>{d && `${d.run.workflowName} v${d.run.workflowVersion} · run ${d.run.number}`} · {ch.branch} · {view.head}{steps.flatMap(s => s.type === 'agent/pi' && s.evidence.model ? [` · ${s.evidence.model}`] : []).join('')}{view.usage && ` · ${fmtPrice(view.usage.costMicros, view.usage.unpriced)}`}</TechnicalWords>
    </div>{!summary && reply ? trailer(`/changes/${ch.id}?tab=conversation`, 'full conversation →') : trailer(`/changes/${ch.id}?tab=changes`, `${shortDiffstat(view.diffstat).replace(/^(.*) in (\d+ files?)$/, '$2 $1') || 'Code'}${view.head ? ` · ${view.head.slice(0, 7)}` : ''} →`)}</div>
    <div className={`ch-overview-line${checksVerdict(steps)?.tone === 'bad' ? ' ch-line-bad' : ''}`}><div className="ch-row-label">Checks</div><div className="ch-value"><CheckSummary technical={technical} steps={steps} contractNeedsAttention={contractUnread(adds)} /><TechnicalWords shown={technical}>{checks.filter(s => s.status !== 'not_applicable').map(s => s.name).join(' · ')}</TechnicalWords></div>{checked && trailer(detail + '#checks', 'details →')}</div>
    {review?.type === 'agent/review' && <div className="ch-overview-line"><div className="ch-row-label">Reviewer</div><p className="ch-value">{review.status === 'running' || review.status === 'pending' ? 'working' : review.checkReading?.unreadable ? 'could not be read' : review.evidence.verdict === 'approve' ? 'approves' : ['fail', 'error', 'needs_human'].includes(review.status) && !review.overrideBy ? <strong className="ch-bad-text"><span className="ch-mark bad">✗</span> Rejected</strong> : review.evidence.verdict || review.status} — {firstLine(review.evidence.reviewerSummary || review.evidence.summary || review.evidence.reason || 'No review conclusion recorded.').split(/(?<=[.!?])\s+/)[0]}</p>{trailer(workTab(undefined, review.runId) + '#review', 'full review →')}</div>}
    {checked && contractChanged(adds) && adds && <div className="ch-overview-line"><div className="ch-row-label">App contract</div><div className="ch-value"><InlineHelp.Provider value={false}><AddsSection adds={adds} /></InlineHelp.Provider></div><span /></div>}
    </section>
    <TurnDecision outcome={resultOnOverview ? <DeployResult key={landingDeploy.id} view={view} step={landingDeploy} /> : undefined} progress={nowRunning(view) ? <Now view={view} /> : undefined} progressTitle={progressTitle(view, steps)} doneTitle={landingDeploy?.status === 'pass' ? 'Deploy complete' : landingDeploy && ['fail', 'error'].includes(landingDeploy.status) ? 'Deploy stopped' : undefined} approvalWaiting={!!approval && !!turn} active={!!turn} shown={shown && (!nowRunning(view) || !!turn)} ready={<>{briefParts?.length ? <>
        {briefParts.map((paragraph, i) => <p key={i}>{paragraph}</p>)}
      </> : approval && turn ? <p>You asked: {firstLine(ch.body || ch.title)}</p> : null}{turn && !next.recovery && <CheckSummary steps={steps} contractNeedsAttention={contractUnread(adds)} />}</>} actions={approval ? [destination ? 'Accept and put live' : 'Accept', ...(offered(view.next, 'send-back') || offered(d?.next, 'rework') ? ['Send back…'] : []), ...(offered(view.next, 'abandon') ? ['Abandon change'] : [])] : heldLabels(view)}>
      {next.sentBack && <p>{next.sentBack.by} sent it back: “{next.sentBack.note}”</p>}
      {view.mainConflict && <p>{view.mainConflict.mainBranch} conflicts with this change: {view.mainConflict.files.join(', ')}. <Link to={detail} onClick={e => e.currentTarget.closest('dialog')?.close()}>Open Details for the catch-up command</Link>.</p>}
      {extraErr && <p className="error">The work this landing carries could not be read: {extraErr}</p>}
      {shortfall && <p>The summary says this change falls short of the request: {shortfall}</p>}
      {unchecked && view.waitingOn.kind !== 'closed' && <p>The summary could not check all of the request: {unchecked}</p>}
      {steps.filter(s => s.type === 'workflow/call' && s.evidence.childRunId).map(s => <ChildOverview key={s.id} step={s} onChange={onChange} />)}
      {!landingDeploy && <p>{view.waitingOn.says}{view.stateDetail && ` · ${view.stateDetail}`}</p>}
      {landingDeploy && !resultOnOverview && <DeployResult key={landingDeploy.id} view={view} step={landingDeploy} />}
      {approval && (!current || extraErr) && <p>Reading the current change before you can accept.</p>}
      {approval && !deployments.current && <p className={deployments.err ? 'error' : 'muted'}>{deployments.err ? `The deployment consequence could not be read: ${deployments.err}` : 'Reading where accepting puts this change…'}</p>}
      {deciding && d && <>
        <p>{destination ? destination.url ? `This puts it live at ${destination.url} for everyone.` : 'This puts it live for everyone; the deployment has no address yet.' : 'This adds it to the app’s main version.'}</p>
        <DecisionForm overview externalBusy={actionBusy}
          conversation={{ href: `/changes/${ch.id}?tab=conversation`, run: () => nav(`/changes/${ch.id}?tab=conversation`) }}
          canSendBack={!!(offered(view.next, 'send-back') || offered(d.next, 'rework'))}
          abandon={offered(view.next, 'abandon') ? { label: 'Abandon change', run: () => act(() => post(`/changes/${ch.id}/abandon`, {})) } : undefined} stepId={approval.id!} means={approval.config.means} alsoMeans={landingMeansFor(d, approval)} instructions={approval.config.instructions} onDone={onChange}
          approveLabel={destination ? 'Accept and put live' : 'Accept'} />
      </>}
      {turn && view.waitingOn.kind !== 'approval' && <HeldDecision view={view} onChange={onChange} resultShown={!!deployDecision} />}
      <DecisionContextOnly.Consumer>{contextOnly => !contextOnly && !turn && current && !extraErr && d && !['shipped', 'abandoned'].includes(ch.state) && <div className="row">
        {offered(d.next, 'advance') && <ContinueDecision busy={actionBusy} run={() => act(async () => setAdvance(await post<AdvanceResult>(`/runs/${d.run.id}/advance`)))} />}
        {advance?.followUpsPending && <span>Workflow finished. Follow-ups pending.</span>}
        {advance && !advance.advanced && !!advance.blocking?.length && <span className="error">Blocked by: {advance.blocking.map(b => `${b.name} (${b.status})`).join(', ')}</span>}
        {!!advance?.missing?.length && <span className="error">Missing required checks: {advance.missing.join(', ')}.</span>}
        {advance?.advanced && <span>Continued.</span>}
        {!next.recovery && d.run.status === 'running' && ch.state !== 'landing' && <QuietConfirm disabled={actionBusy} label="Stop this attempt" ask="Stop this attempt? Nothing is deleted; the change stays open."
          onYes={() => act(() => post(`/runs/${d.run.id}/abandon`, { reason: 'stopped from the change page' }))} />}
      </div>}</DecisionContextOnly.Consumer>
      {actionErr && <p className="error">{actionErr}</p>}
      {!approval && !turn && !deciding && !['shipped', 'abandoned'].includes(ch.state) && <DecisionChoices note={note} setNote={setNote} busy={actionBusy || !current}
        accept={d && offered(view.next, 'land') ? { label: 'Check and land', run: () => act(() => post(`/changes/${ch.id}/land`, {})) } : undefined}
        conversation={{ href: `/changes/${ch.id}?tab=conversation`, run: () => nav(`/changes/${ch.id}?tab=conversation`) }}
        chat={!d ? { label: 'Continue chat', run: () => nav(`/projects/${view.projectSlug}/console?change=${ch.id}`) } : undefined}
        rework={offered(view.next, 'send-back') ? { label: 'Rework with comment', run: () => act(() => post(`/changes/${ch.id}/sendback`, { note })) } : undefined}
        abandon={offered(view.next, 'abandon') ? { label: 'Abandon change', run: () => act(() => post(`/changes/${ch.id}/abandon`, {})) } : undefined} />}
      {question.err && <p className="error">Changes to the app contract could not be read: {question.err}</p>}
    </TurnDecision>
    <footer hidden={!shown} className="small muted"><Link to={`/changes/${ch.id}?tab=changes`}>{view.diffstat?.match(/\d+ files? changed/)?.[0] || 'Code'} → Code</Link> · <Link to={detail}>How it was done → Details</Link></footer>
  </div></DeploySceneScope>
}

// Called workflows keep their own checks and meanings. Their required
// decisions are visible on Overview as well as reachable in the run record.
function ChildOverview({ step, onChange }: { step: Step; onChange: () => void }) {
  const childId = step.type === 'workflow/call' ? step.evidence.childRunId : undefined
  const trace = useGet<{ run: { currentStage: string; status: string }; steps: Step[] }>(childId ? `/runs/${childId}/trace` : null, { poll: 3000 })
  const steps = trace.data?.steps ?? []
  const waiting = steps.find(s => s.type === 'approval' && s.status === 'pending' && s.stage === trace.data?.run?.currentStage)
  const question = useGet<ApprovalQuestion>(waiting ? `/steps/${waiting.id}/question` : null)
  const saved = steps.flatMap(s => s.type === 'approval' ? (s.evidence.decisions ?? []).filter(d => d.adds) : []).at(-1)
  const adds = waiting ? (question.current ? question.data?.adds ?? undefined : undefined) : saved?.adds
  if (!childId) return null
  const busy = checksRunning(steps, trace.data?.run?.currentStage)
  const failed = steps.some(s => s.stage === trace.data?.run?.currentStage && checkTypes.has(s.type) && ['fail', 'error', 'needs_human'].includes(s.status) && !s.overrideBy)
  return <div className="ch-child-overview">
    <h3>{step.name.replace(/[-_]/g, ' ')}</h3>
    {trace.err && <p className="error">The called workflow’s checks could not be read: {trace.err}</p>}
    {!trace.data && !trace.err && <p>Reading the called workflow’s checks…</p>}
    {trace.data && <><CheckSummary steps={steps} contractNeedsAttention={contractUnread(adds)} /></>}
    {busy && !steps.some(s => s.type === 'workflow/call' && s.status === 'running') && <p>Checks are running; you can accept when they finish.</p>}
    {failed && <p>Checks need attention before you can accept.</p>}
    {trace.current && !busy && !failed && trace.data?.run?.status === 'running' && steps.filter(s => s.type === 'approval' && s.status === 'pending' && s.stage === trace.data?.run?.currentStage).map(s => s.type === 'approval' && <DecisionForm key={s.id} stepId={s.id!} means={s.config.means} instructions={s.config.instructions} onDone={onChange} approveLabel="Accept" rejectLabel="Send back with a note" />)}
  </div>
}

// Approval prose is plain text; paragraph breaks are its only structure.
export function approvalBrief(text?: string): string[] | undefined {
  const paragraphs = text?.trim().split(/\n\s*\n/).map(p => p.trim()).filter(Boolean)
  return paragraphs?.length ? paragraphs : undefined
}

// The progress screen's title: what the running work is doing now.
export function progressTitle(view: ChangeView, steps: Step[]): string {
  if (steps.some(s => s.type === 'deploy' && s.status === 'running')) return 'Deploying'
  return view.runs?.find(r => r.id === view.detail?.run.id)?.role === 'landing' ? 'Landing' : 'Working'
}
