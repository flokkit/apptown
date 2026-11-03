import { STATUS_LIGHT, Status } from '../../Status'
import { PRODUCT } from '../../brand'
import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { TryIt } from '../../TryIt'
import { AdvanceResult, ChangeView, Step, StepOf, Workflow, currentUser, isStep, post } from '../../api'
import { Screenshots } from '../../Screenshots'
import { QuietConfirm, QuietPrompt } from '../../QuietConfirm'
import { CloseIt } from '../../CloseIt'
import { DecisionForm, landingMeansFor } from '../../DecisionForm'
import { useAction, useGet } from '../../data'
import { StaleNote, type Gated } from '../../ReadGate'
import type { ConsoleOpened, ConsoleSessionList, MainConflict } from '../../wire.gen'
import { Terminal } from './Terminal'
import { SoFarNote } from './Head'
import { NextStep, isLandingApproval, nextStep, offered, rerunLabel } from './next'
import { disputeOf } from './Disagreement'
import { verificationSummary } from './Evidence'

// The change's state, said once, with ONE primary action for whoever is
// looking (approve, send back, land, take it to chat — or wait, naming who
// or what it waits on). Everything else a person can do here is a quiet
// secondary row. The panel reads the page's own payload through nextStep;
// when the page's last refresh failed, it says the answer may be old
// instead of presenting it as current.

// lastTranscriptStep: the most recently finished step that keeps a
// transcript (an agent or a review), for the folded terminal.
function lastTranscriptStep(steps: Step[]): Step | undefined {
  return [...steps]
    .filter((s) => (s.type === 'agent/pi' || s.type === 'agent/review') && s.status !== 'running' && s.status !== 'pending' && s.finishedAt)
    .sort((a, b) => (a.finishedAt! < b.finishedAt! ? 1 : -1))[0]
}


export function StatePanel({ view, onChange, reading, workTab }: {
  view: ChangeView; onChange: () => void; reading?: Gated
  // workTab: the address of the Work tab, at a step when given.
  workTab: (stepId?: string | null) => string
}) {
  // A live chat on this change is where its work happens while no run
  // goes; the project's chat list says whether its agent has the turn. An
  // unread or failed list claims nothing.
  const open = view.change.state === 'in_progress'
  const chats = useGet<ConsoleSessionList>(open ? `/projects/${view.projectSlug}/console` : null, { poll: 5000 })
  const live = chats.refreshFailed ? undefined
    : chats.data?.sessions.find((x) => x.changeId === view.change.id && x.state !== 'dead' && !!x.turn)
  const next = nextStep(view, { chat: live?.turn })
  const handover = handoverState(view, next)
  const stale = !!reading?.refreshFailed
  const light = stale ? 'unknown' : STATUS_LIGHT[view.statusLabel]
  const tone = ({pass:'ok', pending:'warn', fail:'bad', unknown:'unknown', '':'neutral'} as Record<string, string>)[light]
  // A failed check the agent can fix leads with what the check found, in
  // its own first words, instead of the step's recorded reason.
  const found = !next.recovery && fixable(view, next) && view.detail ? foundLine(view.detail) : ''
  return (
    <section className={`ch-state tone-${tone}`} aria-label="Where this change is">
      <div className="ch-state-words">
        <div className="ch-state-head">
          <span className={`ch-dot ${tone}`} aria-hidden="true" />
          <span className="ch-headline">{next.recovery ? next.headline : handover?.headline ?? next.headline}</span>
        </div>
        {next.sentBack && (
          <div className="ch-sentence">
            {next.sentBack.by === 'a check' ? 'A check sent it back' : `${next.sentBack.by} sent it back`}:{' '}
            <span className="ch-quote">“{firstLines(next.sentBack.note, 3)}”</span>
          </div>
        )}
        {found ? <div className="ch-sentence">{found}</div> : next.sentence && (next.recovery && next.sentence.length > 240
          ? <details className="ch-sentence"><summary>Full reason</summary>{next.sentence}</details>
          : <div className="ch-sentence">{next.sentence}</div>)}
        {!next.recovery && !found && !next.sentence && next.primary === 'take-to-chat' && view.detail && <FoundLine detail={view.detail} />}
        {next.waitingOn && !(next.primary === 'approve' && next.waitingOn === 'you') && <div className="ch-waiting">Waiting on: <strong>{next.waitingOn}</strong></div>}
        {handover && handover.actions.length > 0 && <ul>{handover.actions.map((action, i) => <li key={i}>{action}</li>)}</ul>}
        {handover && handover.attempts.length > 0 && <details className="ch-sentence">
          <summary>What the agent tried ({handover.attempts.length})</summary>
          <ol>{handover.attempts.map((attempt, i) => <li key={i}>{attempt}</li>)}</ol>
        </details>}
        {view.mainConflict && <MainConflictLine conflict={view.mainConflict} />}
        {reading && <StaleNote q={reading} />}
      </div>
      <Actions view={view} next={next} onChange={onChange} workTab={workTab} />
    </section>
  )
}

// fixable: the change stopped on a check the agent can fix, and AppTown
// offers to send it back — a blocked working run or a stopped landing. A
// person's rejection, a stuck agent step and a rules change are not: they
// have their own words and acts.
export function fixable(view: ChangeView, next: NextStep): boolean {
  const s = next.step
  return !!s && !!view.detail && (next.primary === 'take-to-chat' || next.primary === 'send-back')
    && !['agent/pi', 'approval', 'policy/current'].includes(s.type) && !!offered(view.next, 'send-back')
}

function firstLines(s: string, n: number) {
  const lines = s.trim().split('\n')
  return lines.length <= n ? s.trim() : lines.slice(0, n).join(' ') + ' …'
}

// Actions: the primary act for this state, its supporting evidence, and the
// quiet secondary row. Every act the old banner offered is still here, in
// the state it belonged to.
function Actions({ view, next, onChange, workTab }: { view: ChangeView; next: NextStep; onChange: () => void; workTab: (stepId?: string | null) => string }) {
  const nav = useNavigate()
  const { run: act, busy, err } = useAction(onChange)
  const [advance, setAdvance] = useState<AdvanceResult | null>(null)
  const ch = view.change
  // The change's own id: the server always sends it (null is only for an
  // absent reference elsewhere in the same wire type).
  const changeId = ch.id!
  const d = view.detail
  const run = d?.run
  const step = next.step
  // "Take it to chat": the server stops the lane's run, copies its agent's
  // session into a new console and briefs it on where the lane stopped; we
  // land on that exact session with the opening question sent for us.
  const takeToChat = (s: Step) => act(async () => {
    const r = await post<ConsoleOpened>(`/projects/${view.projectSlug}/console`, { changeId: ch.id, continueStep: s.id })
    nav(`/projects/${view.projectSlug}/console?change=${ch.id}&session=${r.session.id}&kickoff=stuck`)
  })
  const rerun = (s: Step) => act(() => post(`/steps/${s.id}/run`))
  const abandon = <QuietConfirm label="Abandon" className="subtle" ask="Abandon this change? The branch is kept; the workspace is removed."
    onYes={() => act(() => post(`/changes/${ch.id}/abandon`, {}))} />
  const stopAttempt = run?.status === 'running' && ch.state !== 'landing' && (
    <QuietConfirm label="Stop this attempt" className="subtle" ask="Stop this attempt? Nothing is deleted; the change stays open."
      onYes={() => act(() => post(`/runs/${run.id}/abandon`, { reason: 'stopped from the change page' }))} />
  )
  const inChat = next.stamp === 'agent working' || next.stamp === 'in the chat'
  const chatHref = `/projects/${view.projectSlug}/console?change=${ch.id}`
  const chatTitle = "Talk to the project's helper about this change; what you do together is saved to the change."
  const chatLink = (primary: boolean) => primary
    ? <Link className="btn primary" to={chatHref} title={chatTitle}>{inChat ? 'Open the chat' : 'Chat about it'}</Link>
    : <button className="subtle" title={chatTitle} onClick={() => nav(chatHref)}>Chat about it</button>
  const lastTalker = d && run?.status === 'running' && !(step?.status === 'running') ? lastTranscriptStep(d.steps) : undefined

  // The quiet acts every stopped step shares: accept it anyway (an
  // override, with its meaning said before it is recorded; AppTown decides
  // who may), and its full record in the Work tab.
  const acceptAnyway = (s: Step, primary = false) => (
    <QuietPrompt label="Accept it anyway" className={primary ? 'primary' : 'subtle'} yes="Accept it anyway"
      ask={`Accept the change although “${s.name}” did not pass? This records an exception in your name, with your reason; the check's result stays on the record.`}
      fields={[{ key: 'reason', placeholder: 'Why (required)', required: true, width: 260 }]}
      onYes={(v) => act(() => post(`/steps/${s.id}/override`, { user: currentUser(), reason: v.reason.trim() }))} />
  )
  const record = (s: Step) => <button className="subtle" onClick={() => nav(workTab(s.id))}>See the check's record</button>

  let body: React.ReactNode = null
  let quiet: React.ReactNode = null
  if (next.recovery && step && d) {
    const chat = offered(d.next, 'rework')
    const agent = chat && d.steps.find((s) => s.id === chat.stepId)
    const sendBack = offered(view.next, 'send-back')
    const fix = () => act(() => post(`/changes/${changeId}/sendback`, { note: recoveryNote(d) }))
    const cls = (primary: boolean) => primary ? 'primary' : 'subtle'
    const send = sendBack && <button className={cls(next.primary === 'send-back')} disabled={busy} onClick={fix}>Fix it</button>
    const talk = agent && <button className={cls(next.primary === 'take-to-chat')} disabled={busy} onClick={() => takeToChat(agent)}>Take it to chat</button>
    body = <div className="row ch-primary">
      {next.primary === 'take-to-chat' ? <>{talk}{send}</> : <>{send}{talk}</>}
      {d.next.filter((a) => a.act === 'retry' || a.act === 'override').map((a) => {
        const target = d.steps.find((s) => s.id === a.stepId)
        if (!target) return null
        return <span key={`${a.act}-${a.stepId}`}>
          {a.act === 'retry'
            ? <button className={cls(next.primary === 'try-again' && target.id === step.id)} disabled={busy} onClick={() => rerun(target)}>{rerunLabel(target)}</button>
            : acceptAnyway(target, next.primary === 'override' && target.id === step.id)}
        </span>
      })}
      {offered(view.next, 'abandon') && abandon}
    </div>
    quiet = record(step)
  } else switch (next.primary) {
    case 'approve': {
      if (step?.type !== 'approval' || !d || !run) break
      const toLand = isLandingApproval(view, step)
      body = (
        <>
          <ApproveEvidence changeId={changeId} soFar={ch.summary || view.report} checks={verificationSummary(d.steps)}
            shortfall={ch.summary ? requestShortfall(ch) : undefined} unchecked={ch.summary ? requestUnchecked(ch) : undefined} boot={latestBoot(d.steps)} head={view.head}
            notApplicable={notApplicableHere(d.steps)} />
          {/* Two roles, two zones: the DECISION is the approver's —
              approve, or reject with the words that travel back to the agent.
              Going back to work is the AUTHOR's move: a quiet line below. */}
          <DecisionForm stepId={step.id!} onDone={onChange}
            means={step.config.means} alsoMeans={landingMeansFor(d, step)} instructions={step.config.instructions}
            approveLabel={toLand ? 'Approve and land' : 'Approve'} rejectLabel="Reject and send back"
            placeholder="Note (optional)" />
        </>
      )
      quiet = toLand && run.status === 'running'
        ? <KeepWorking runId={run.id!} view={view} />
        : <>
            <button className="subtle" onClick={() => takeToChat(step)}>Take it to chat</button>
            {stopAttempt}
          </>
      break
    }
    case 'take-to-chat':
    case 'send-back':
      // A failed check: the person's next step is almost always "have the
      // agent fix what the check found". Fix it sends the change back with
      // the findings as the note — in a blocked working run and in a
      // stopped landing alike; the rest is one click away.
      if (step && d && fixable(view, next)) {
        body = <FixIt changeId={changeId} note={fixNote(d)} onDone={onChange} />
        quiet = (
          <OtherOptions>
            <button className="subtle" title="Stops this attempt and opens the chat; the same agent continues, with you steering." onClick={() => takeToChat(step)}>Talk it through</button>
            {offered(d.next, 'retry', step.id) && <button className="subtle" onClick={() => rerun(step)}>Run the check again</button>}
            {acceptAnyway(step)}
            {record(step)}
            <QuietConfirm label="Close it" className="subtle" ask="Close this change? The branch is kept; the workspace is removed."
              onYes={() => act(() => post(`/changes/${ch.id}/abandon`, {}))} />
          </OtherOptions>
        )
        break
      }
    // falls through: a rejection, or a step the server will not send back
    case 'pick-up': {
      if (!step) break
      const primaryBtn = next.primary === 'pick-up'
        ? <button className="primary" disabled={busy} onClick={() => rerun(step)}>Pick it back up</button>
        : next.primary === 'take-to-chat'
          ? <button className="primary" disabled={busy} title="Stops this attempt and opens the chat; the same agent continues, with you steering." onClick={() => takeToChat(step)}>Take it to chat</button>
          : null
      body = (
        <>
          {primaryBtn && <div className="row ch-primary">{primaryBtn}</div>}
          {next.primary === 'send-back' && <SendBack changeId={changeId} onDone={onChange} primary />}
        </>
      )
      quiet = (
        <>
          {next.primary !== 'take-to-chat' && <button className="subtle" onClick={() => takeToChat(step)}>Take it to chat</button>}
          {next.primary !== 'pick-up' && offered(d?.next, 'retry', step.id) && <button className="subtle" onClick={() => rerun(step)}>{rerunLabel(step)}</button>}
          {step.type !== 'approval' && step.type !== 'agent/pi' && acceptAnyway(step)}
          {step.type === 'approval'
            ? <button className="subtle" onClick={() => nav(workTab(step.id))}>See the record</button>
            : record(step)}
          {next.primary === 'take-to-chat' && step.type !== 'agent/pi' && offered(view.next, 'send-back') && <SendBack changeId={changeId} onDone={onChange} />}
          {abandon}
          {stopAttempt}
        </>
      )
      break
    }
    case 'try-again':
      if (!step) break
      // AppTown offered this step's retry: that is what made it primary.
      body = (
        <div className="row ch-primary">
          <button className="primary" disabled={busy} onClick={() => rerun(step)}>{rerunLabel(step)}</button>
        </div>
      )
      quiet = <>{acceptAnyway(step)}{record(step)}{offered(view.next, 'abandon') && <CloseIt changeId={changeId} />}</>
      break
    case 'restart-landing':
      if (!run) break
      body = (
        <div className="row ch-primary">
          <button className="primary" disabled={busy} onClick={() => act(() => post(`/runs/${run.id}/restart-landing`, {}))}>Restart landing</button>
          <span className="muted small">lands this change again under the current rules</span>
        </div>
      )
      quiet = <>{step && <button className="subtle" onClick={() => nav(workTab(step.id))}>Which rules changed</button>}{abandon}</>
      break
    case 'continue':
      if (!run) break
      body = (
        <div className="row ch-primary">
          <button className="primary" disabled={busy} onClick={() => act(async () => setAdvance(await post<AdvanceResult>(`/runs/${run.id}/advance`)))}>Continue</button>
          {advance?.followUpsPending && <span className="muted small">Workflow finished. Follow-ups pending.</span>}
          {advance && !advance.advanced && !!advance.blocking?.length && (
            <span className="error small">Blocked by: {advance.blocking.map((b) => `${b.name} (${b.status})`).join(', ')}</span>
          )}
          {!!advance?.missing?.length && <span className="error small">Missing required checks: {advance.missing.join(', ')}.</span>}
          {advance?.advanced && <span className="muted small">Continued.</span>}
        </div>
      )
      quiet = <>{stopAttempt}</>
      break
    case 'override':
      break
    case 'open-decision':
      body = <div className="row ch-primary"><Link className="btn primary" to={workTab(next.step?.id ?? d?.pendingApproval)}>Open the decision</Link></div>
      quiet = <>{stopAttempt}</>
      break
    case 'land':
    case 'chat':
      body = (
        <div className="row ch-primary">
          {next.primary === 'land'
            ? <button className="primary" disabled={busy} onClick={() => act(() => post(`/changes/${ch.id}/land`, {}))}>{busy ? 'Starting…' : 'Check and land'}</button>
            : chatLink(true)}
          {ch.state === 'in_progress' && <TryIt changeId={changeId} className="" />}
          {view.editing > 0 && <span className="muted small">{view.editing} file{view.editing === 1 ? '' : 's'} being edited</span>}
        </div>
      )
      quiet = ch.state === 'in_progress' ? (
        <>
          {next.primary === 'land' ? chatLink(false) : offered(view.next, 'land') && <button className="subtle" disabled={busy} onClick={() => act(() => post(`/changes/${ch.id}/land`, {}))}>Check and land</button>}
          <RunWorkflow view={view} onRun={(name) => act(() => post(`/changes/${ch.id}/runs`, { workflow: name }))} />
          {abandon}
        </>
      ) : null
      break
    case 'open-release': {
      const rel = d ? [...d.artifacts].reverse().find((a) => a.kind === 'release' && a.ref) : undefined
      body = (
        <>
          {rel && <div className="row ch-primary"><a className="btn primary" href={rel.ref} target="_blank" rel="noreferrer" title={rel.ref}>Open the release ↗</a></div>}
          <ReleaseRows view={view} />
        </>
      )
      quiet = <TryIt changeId={changeId} label="View as of this change" className="subtle" />
      break
    }
    case 'none':
      if (ch.state === 'shipped') {
        body = <ReleaseRows view={view} />
        quiet = <TryIt changeId={changeId} label="View as of this change" className="subtle" />
      } else if (run?.status === 'running' && step?.status === 'running') {
        body = <Terminal stepId={step.id!} name={step.name} running startedAt={step.startedAt} defaultOpen />
        quiet = <>{stopAttempt}</>
      } else if (run?.status === 'running') {
        quiet = <>{next.tone === 'bad' && abandon}{stopAttempt}</>
      }
      break
  }

  return (
    <>
      {body && <div className="ch-state-body">{body}</div>}
      {lastTalker && (
        <div className="ch-state-body">
          <Terminal stepId={lastTalker.id!} name={lastTalker.name} running={false} startedAt={lastTalker.startedAt} finishedAt={lastTalker.finishedAt} />
        </div>
      )}
      {err && <div className="error ch-state-body">{err}</div>}
      {quiet && <div className="row ch-quiet">{quiet}</div>}
    </>
  )
}

// RunWorkflow: one quiet door for fire-and-forget. The human verbs on a
// change are chat / try / check and land / abandon; everything mechanism-shaped lives
// behind one honestly named select.
function RunWorkflow({ view, onRun }: { view: ChangeView; onRun: (name: string) => void }) {
  const { data } = useGet<Workflow[]>(`/projects/${view.projectSlug}/workflows`)
  const workflows = data ?? []
  const [wf, setWf] = useState('')
  return (
    <select value={wf} aria-label="run a workflow on this change" title="run one of this project's workflows on this change, unattended" onChange={(e) => {
      const name = e.target.value
      setWf('')
      if (name) onRun(name)
    }}>
      <option value="">Run a workflow…</option>
      <optgroup label="Core">
        {workflows.filter((w) => w.tier === 'core').map((w) => <option key={w.name}>{w.name}</option>)}
      </optgroup>
      <optgroup label="Extras">
        {workflows.filter((w) => !w.system && w.tier !== 'core' && w.name !== 'land').map((w) => <option key={w.name}>{w.name}</option>)}
      </optgroup>
    </select>
  )
}

// ApproveEvidence: what the approver is deciding on, IN the decision — the
// distilled story of the change, a try-it, and the change's size. When no
// summary exists yet (a chat-built change whose distiller hasn't run), ask
// for one on the spot: the page's poll picks it up, so the panel fills in
// rather than asking a person to approve a blank.
// requestShortfall: what the change's summary says it does not do of what
// was asked, when the summary's writer read the work as falling short.
// 'yes' and 'unknown' say nothing here: unknown is not a warning, and it is
// never shown as met either.
export function requestShortfall(ch: { requestMet?: string; requestGap?: string }): string | undefined {
  if (ch.requestMet !== 'no' && ch.requestMet !== 'partly') return undefined
  const gap = ch.requestGap?.trim()
  if (!gap) return ch.requestMet === 'no' ? 'it does not do what was asked.' : 'it does only part of what was asked.'
  return /[.!?]$/.test(gap) ? gap : gap + '.'
}

// requestUnchecked: when the summary's writer could not check all of the
// work against the request, what it could not check and what would settle it.
// This is unknown, not a shortfall: the page says "could not check", never "falls short".
export function requestUnchecked(ch: { requestMet?: string; requestGap?: string }): string | undefined {
  if (ch.requestMet !== 'unknown') return undefined
  return ch.requestGap?.trim() || undefined
}

// notApplicableHere: every check and step of the run that applies to
// nothing this app declares, each with its reason, so the person approving
// sees them in the decision and never reads them as passed.
export function notApplicableHere(steps: Step[]): { name: string; why: string }[] {
  const out: { name: string; why: string }[] = []
  for (const s of steps) {
    if (s.status === 'not_applicable') {
      out.push({ name: s.name, why: (s.type === 'scan' ? s.evidence.notApplicable : '') || s.evidence?.reason || '' })
    }
    for (const c of s.checkReading?.checks ?? []) {
      if (c.verdict === 'not-applicable') out.push({ name: c.check, why: c.detail ?? '' })
    }
  }
  return out
}

// latestBoot is the run's newest preview step: its screenshots are what the
// person approving sees of the app. Only this run's: never an earlier one's.
export function latestBoot(steps: Step[]): StepOf<'preview'> | undefined {
  const boots = steps.filter(isStep('preview')).filter((s) => s.status !== 'running' && s.evidence?.screenshots)
  return boots.length ? boots[boots.length - 1] : undefined
}

export function ApproveEvidence({ changeId, soFar, checks, shortfall, unchecked, boot, head, notApplicable }: {
  changeId: string; shortfall?: string; unchecked?: string; boot?: StepOf<'preview'>; head?: string
  // soFar: what the change did, from its summary or else the agent's report
  // (a summary is asked for when there is neither); checks: what the run's
  // checks did, always said beside it.
  soFar?: string; checks?: string
  notApplicable?: { name: string; why: string }[]
}) {
  const asked = useRef(false)
    useEffect(() => {
    if (soFar || asked.current) return
    asked.current = true
    post(`/changes/${changeId}/distill`, {}).catch(() => { /* A missing summary makes no claim about the change. */ })
  }, [soFar, changeId])
  return (
    <div className="ch-approve-evidence">
      {soFar && <SoFarNote text={soFar} />}
      {checks && <div className="ch-sentence">Checks: {checks}</div>}
      <div className="row" style={{ marginTop: 8 }}>
        <TryIt changeId={changeId} label="Try it first" className="subtle" />
      </div>
      {boot && <Screenshots shots={boot.evidence.screenshots} head={head} at={boot.finishedAt ?? undefined} />}
      {notApplicable && notApplicable.length > 0 && (
        <details className="small" style={{ marginTop: 8 }}>
          <summary>Not applicable here: {notApplicable.length} {notApplicable.length === 1 ? 'check' : 'checks'}, not passed</summary>
          <ul>{notApplicable.map((c, i) => <li key={i}><strong>{c.name}</strong>{c.why ? <span className="muted"> · {c.why}</span> : null}</li>)}</ul>
        </details>
      )}
      {unchecked && !shortfall && (
        <div className="ch-unchecked" role="status">
          <strong>The summary could not check all of the request:</strong> {unchecked}
        </div>
      )}
      {shortfall && (
        <div className="ch-shortfall" role="alert">
          <strong>The summary says this change falls short of the request:</strong> {shortfall}{' '}
          <span className="muted">Read it and try it before you approve; rejecting sends your words back to the agent.</span>
        </div>
      )}
    </div>
  )
}

// KeepWorking: the AUTHOR's move during a landing — quiet, confirmed in
// place, no reason demanded (the record gets a standard note). Stops the
// landing and reopens the chat.
export function KeepWorking({ runId, view }: { runId: string; view: ChangeView }) {
  const nav = useNavigate()
  const [confirming, setConfirming] = useState(false)
  const { run, busy, err } = useAction()
  return (
    <span className="row" style={{ gap: 8 }}>
      {!confirming ? (
        <>{' '}
          <button className="subtle" onClick={() => setConfirming(true)}>Stop the landing and go back to the chat</button>
        </>
      ) : (
        <><span className="small">Stop the landing and go back to the chat?</span>
          <button className="subtle" disabled={busy} onClick={async () => {
            const done = await run(async () => { await post(`/runs/${runId}/abandon`, { reason: 'returned to the change chat to keep working' }); return true })
            if (done) nav(`/projects/${view.projectSlug}/console?change=${view.change.id}`)
            else setConfirming(false)
          }}>{busy ? 'Stopping…' : 'Yes, stop it'}</button>
          <button className="subtle" onClick={() => setConfirming(false)}>Stay</button>
        </>
      )}
      {err && <span className="error small"> {err}</span>}
    </span>
  )
}

// SendBack: a person sends a change back to work with words. A wedged
// landing is abandoned so the change can land again afterwards; the change's
// agent works from the note, and a change no agent works on is back in
// progress for its person to push a fix.
export function SendBack({ changeId, onDone, primary }: { changeId: string; onDone: () => void; primary?: boolean }) {
  const [open, setOpen] = useState(false)
  const [note, setNote] = useState('')
  const { run, busy, err } = useAction(() => { setOpen(false); setNote(''); onDone() })
  if (!open) {
    return primary
      ? <div className="row ch-primary"><button className="primary" onClick={() => setOpen(true)}>Send it back with a note</button></div>
      : <button className="subtle" onClick={() => setOpen(true)}>Send it back with a note</button>
  }
  return (
    <div className="ch-sendback">
      <textarea rows={3} value={note} onChange={(e) => setNote(e.target.value)} aria-label="what to change"
        placeholder="Say what to change." />
      <div className="row" style={{ marginTop: 6 }}>
        <button className="primary" disabled={busy || !note.trim()} onClick={() => run(() => post(`/changes/${changeId}/sendback`, { note }))}>{busy ? 'Sending…' : 'Send it back'}</button>
        <button className="subtle" onClick={() => setOpen(false)}>Cancel</button>
        {err && <span className="error small">{err}</span>}
      </div>
    </div>
  )
}

// MainConflictLine: main AppTown could not merge into the change before a
// step, while the branch still does not contain it, and how to catch up.
// It goes once the change has caught up.
export function MainConflictLine({ conflict }: { conflict: MainConflict }) {
  return (
    <div className="ch-sentence">
      {conflict.mainBranch} has moved and {PRODUCT} could not merge it in before step {conflict.step}: {conflict.files.join(', ')}{' '}
      {conflict.files.length === 1 ? 'conflicts' : 'conflict'}. Catch up in the change's chat:{' '}
      <code style={{ overflowWrap: 'anywhere' }}>{conflict.catchUp}</code>
    </div>
  )
}

// fixNote is what Fix it sends back: each failed check and what it found,
// in the same words the page shows under "What failed".
export function fixNote(d: NonNullable<ChangeView['detail']>): string {
  const lines = disputeOf(d).map((v) => {
    const found = [v.text, ...v.findings, v.log].filter(Boolean).join('\n')
    return found ? `The “${v.step.name}” check did not pass:\n${found}` : `The “${v.step.name}” check did not pass.`
  })
  return [...lines, 'Fix what it found, then the checks run again.'].join('\n\n')
}

function FixIt({ changeId, note, onDone }: { changeId: string; note: string; onDone: () => void }) {
  const { run, busy, err } = useAction(onDone)
  return (
    <div className="row ch-primary">
      <button className="primary" disabled={busy} title="Sends the change back to the agent with what the check found." onClick={() => run(() => post(`/changes/${changeId}/sendback`, { note }))}>{busy ? 'Sending…' : 'Fix it'}</button>
      <span className="muted small">the agent gets what the check found and works on it</span>
      {err && <span className="error small">{err}</span>}
    </div>
  )
}

// Recovery carries the recorded failure and requested actions in full to the agent.
export function recoveryNote(d: NonNullable<ChangeView['detail']>): string {
  return d.steps.filter((s) => s.stage === d.run.currentStage && ['fail', 'error', 'needs_human'].includes(s.status))
    .map((s) => {
      const ev = s.evidence as { reason?: string; actions?: string[]; summary?: string; findings?: string[]; log?: string; logTail?: string }
      const words = [...new Set([ev.reason, ev.summary, ...(ev.findings ?? []), ev.log, ev.logTail, ...(ev.actions ?? [])].filter(Boolean))]
      return `${s.name}:\n${words.join('\n')}`
    })
    .join('\n\n')
}

// OtherOptions: the acts besides the primary one, one click away, in one
// quiet style on one line.
function OtherOptions({ children }: { children: React.ReactNode }) {
  const [open, setOpen] = useState(false)
  return open
    ? <>{children}</>
    : <button className="subtle" aria-expanded={false} onClick={() => setOpen(true)}>Other options</button>
}

// foundLine says, in the check's own first words, what it found: the
// answer to "what is wrong" sits under the headline, not two clicks down.
export function foundLine(detail: NonNullable<ChangeView['detail']>): string {
  const v = disputeOf(detail)[0]
  return (v?.text || v?.findings[0] || v?.log || '').trim().split('\n')[0]
}

function FoundLine({ detail }: { detail: NonNullable<ChangeView['detail']> }) {
  const first = foundLine(detail)
  return first ? <div className="ch-sentence">{first}</div> : null
}

// ReleaseRows: the releases a landed change went out in, one row each.
// None recorded is no row at all: the headline already says it landed.
function ReleaseRows({ view }: { view: ChangeView }) {
  const releases = view.evidence?.releases ?? []
  if (releases.length === 0) return null
  return (
    <div className="ch-release-rows">
      {releases.map((r) => (
        <Link key={r.number} className="card row" style={{ display: 'flex' }} to={`/projects/${view.projectSlug}/releases/${r.number}`}>
          <span className="row-status"><Status word={r.statusLabel} /></span>
          <span style={{ flex: 1, minWidth: 0 }}>Release #{r.number} <span className="muted small">to {r.deployment}</span></span>
        </Link>
      ))}
    </div>
  )
}

// Rework events keep each attempt's reason even after the step is checked
// again. A person's retry starts a fresh allowance; history remains visible.
function handoverState(view: ChangeView, next: NextStep): { headline: string; actions: string[]; attempts: string[] } | undefined {
  const d = view.detail
  if (!d || d.run.status === 'abandoned' || next.tone === 'unknown' || next.primary === 'open-decision') return undefined
  const steps = d.steps.filter((s) => s.stage === d.run.currentStage)
  const events = d.events ?? []
  const attempts = events.filter((e) => e.kind === 'step.rework').map((e) => {
    const reason = typeof e.payload.reason === 'string' && !e.payload.reason.includes('evidence fed back') && !e.payload.reason.includes('their words fed back')
      ? e.payload.reason : 'The reason was not kept.'
    return `${String(e.payload.step ?? 'Check')}: ${reason}`
  })
  const lastRetry = events.map((e) => e.kind).lastIndexOf('step.retried')
  const rounds = events.slice(lastRetry + 1).filter((e) => e.kind === 'step.rework').length
  const held = steps.filter((s) => (s.status === 'needs_human' || s.status === 'error'))
  const actions = held.flatMap((s) => s.evidence?.actions ?? [])
  const busy = steps.some((s) => s.type === 'agent/pi' && (s.status === 'pending' || s.status === 'running'))
  if (busy && !held.length) return { headline: `The agent is working on it${rounds ? ` (round ${Math.min(rounds, 3)} of 3)` : ''}`, actions, attempts }
  if (held.length) return { headline: `Waiting for you: ${held[0].name} needs your help`, actions, attempts }
  const failed = steps.find((s) => s.status === 'fail')
  if (failed && rounds >= 3) return { headline: `Waiting for you: the agent tried 3 times; ${failed.name} still fails: ${failed.evidence?.reason || 'read what the check found'}`, actions, attempts }
  if (failed) return { headline: `Waiting for you: ${failed.name} failed: ${failed.evidence?.reason || 'read what the check found'}`, actions, attempts }
  if (next.primary === 'approve') return { headline: `Waiting for ${next.waitingOn === 'you' ? 'you' : next.waitingOn || 'a person'}: approve this change`, actions, attempts }
  if (d.run.status === 'done') return { headline: 'Passed', actions, attempts }
  if (next.primary === 'continue') return { headline: 'Waiting for you: continue to the next stage', actions, attempts }
  return { headline: `${PRODUCT} is running the checks.`, actions, attempts }
}
