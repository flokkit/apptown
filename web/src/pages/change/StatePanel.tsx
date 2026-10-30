import { PRODUCT } from '../../brand'
import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { TryIt } from '../../TryIt'
import { AdvanceResult, ChangeView, Step, StepOf, Workflow, isStep, post } from '../../api'
import { Screenshots } from '../../Screenshots'
import { QuietConfirm } from '../../QuietConfirm'
import { CloseIt } from '../../CloseIt'
import { DecisionForm, landingMeansFor } from '../../DecisionForm'
import { useAction, useGet } from '../../data'
import { StaleNote, type Gated } from '../../ReadGate'
import type { ConsoleOpened, ConsoleSessionList, MainConflict } from '../../wire.gen'
import { Terminal } from './Terminal'
import { SoFarNote } from './Head'
import { NextStep, isLandingApproval, nextStep, offered, rerunLabel } from './next'

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
  const stale = !!reading?.refreshFailed
  const tone = stale ? 'unknown' : next.tone
  return (
    <section className={`ch-state tone-${tone}`} aria-label="Where this change is">
      <div className="ch-state-main">
        <span className={`ch-dot ${tone}`} aria-hidden />
        <div className="ch-state-words">
          <div className="ch-state-head">
            <span className="ch-headline">{next.headline}</span>
            {stale && <span className="chip stale">not current</span>}
          </div>
          {next.sentBack && (
            <div className="ch-sentence">
              {next.sentBack.by === 'a check' ? 'A check sent it back' : `${next.sentBack.by} sent it back`}:{' '}
              <span className="ch-quote">“{firstLines(next.sentBack.note, 3)}”</span>
            </div>
          )}
          {next.sentence && <div className="ch-sentence">{next.sentence}</div>}
          {next.waitingOn && <div className="ch-waiting">Waiting on: <strong>{next.waitingOn}</strong></div>}
          {view.mainConflict && <MainConflictLine conflict={view.mainConflict} />}
          {reading && <StaleNote q={reading} />}
        </div>
      </div>
      <Actions view={view} next={next} onChange={onChange} workTab={workTab} />
    </section>
  )
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
  const abandon = <QuietConfirm label="Abandon" ask="Abandon this change? The branch is kept; the workspace is removed."
    onYes={() => act(() => post(`/changes/${ch.id}/abandon`, {}))} />
  const stopAttempt = run?.status === 'running' && ch.state !== 'landing' && (
    <QuietConfirm label="Stop this attempt" ask="Stop this attempt? Nothing is deleted; the change stays open."
      onYes={() => act(() => post(`/runs/${run.id}/abandon`, { reason: 'stopped from the change page' }))} />
  )
  const inChat = next.stamp === 'agent working' || next.stamp === 'in the chat'
  const chatLink = (primary: boolean) => (
    <Link className={primary ? 'btn primary' : 'btn'} to={`/projects/${view.projectSlug}/console?change=${ch.id}`}
      title="Talk to the project's helper about this change; what you do together is saved to the change.">
      {inChat && primary ? 'Open the chat' : 'Chat about it'}
    </Link>
  )
  const lastTalker = d && run?.status === 'running' && !(step?.status === 'running') ? lastTranscriptStep(d.steps) : undefined

  let body: React.ReactNode = null
  let quiet: React.ReactNode = null
  switch (next.primary) {
    case 'approve': {
      if (step?.type !== 'approval' || !d || !run) break
      const toLand = isLandingApproval(view, step)
      body = (
        <>
          <ApproveEvidence changeId={changeId} soFar={ch.summary || view.report} diffstat={view.diffstat}
            shortfall={ch.summary ? requestShortfall(ch) : undefined} unchecked={ch.summary ? requestUnchecked(ch) : undefined} boot={latestBoot(d.steps)} head={view.head}
            notApplicable={notApplicableHere(d.steps)} />
          {/* Two roles, two zones: the DECISION is the approver's —
              approve, or reject with the words that travel back to the agent.
              Going back to work is the AUTHOR's move: a quiet line below. */}
          <DecisionForm stepId={step.id!} onDone={onChange}
            means={step.config.means} alsoMeans={landingMeansFor(d, step)} instructions={step.config.instructions}
            approveLabel={toLand ? 'Approve and land' : 'Approve'} rejectLabel="Reject and send back"
            placeholder="say why (optional; a rejection sends your words back to the agent)" />
        </>
      )
      quiet = toLand && run.status === 'running'
        ? <KeepWorking runId={run.id!} view={view} />
        : <span className="muted small">Not ready to decide?{' '}
            <button className="subtle" onClick={() => takeToChat(step)}>Take it to chat</button>
            <span> stops this attempt; the same agent continues with you.</span> {stopAttempt}
          </span>
      break
    }
    case 'take-to-chat':
    case 'send-back':
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
          <Link className="small" to={workTab(step.id)}>Override or see the full record</Link>
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
      quiet = <><Link className="small" to={workTab(step.id)}>Override or see the full record</Link>{offered(view.next, 'abandon') && <CloseIt changeId={changeId} />}</>
      break
    case 'restart-landing':
      if (!run) break
      body = (
        <div className="row ch-primary">
          <button className="primary" disabled={busy} onClick={() => act(() => post(`/runs/${run.id}/restart-landing`, {}))}>Restart landing</button>
          <span className="muted small">lands this change again under the current rules</span>
        </div>
      )
      quiet = <>{step && <Link className="small" to={workTab(step.id)}>Which rules changed</Link>}{abandon}</>
      break
    case 'continue':
      if (!run) break
      body = (
        <div className="row ch-primary">
          <button className="primary" disabled={busy} onClick={() => act(async () => setAdvance(await post<AdvanceResult>(`/runs/${run.id}/advance`)))}>Continue</button>
          {advance?.followUpsPending && <span className="muted small">Workflow completed. Follow-up work is queued or running.</span>}
          {advance && !advance.advanced && !!advance.blocking?.length && (
            <span className="error small">Blocked by: {advance.blocking.map((b) => `${b.name} (${b.status})`).join(', ')}</span>
          )}
          {!!advance?.missing?.length && <span className="error small">Missing required checks: {advance.missing.join(', ')}.</span>}
          {advance?.advanced && <span className="muted small">Moved on.</span>}
        </div>
      )
      quiet = <>{stopAttempt}</>
      break
    case 'open-decision':
      body = <div className="row ch-primary"><Link className="btn primary" to={workTab()}>Open the decision</Link></div>
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
        <div className="row ch-primary">
          {rel && <a className="btn primary" href={rel.ref} target="_blank" rel="noreferrer" title={rel.ref}>Open the release ↗</a>}
          <TryIt changeId={changeId} label="▶ View as of this change" className="" />
        </div>
      )
      break
    }
    case 'none':
      if (ch.state === 'shipped') {
        body = <div className="row ch-primary"><TryIt changeId={changeId} label="▶ View as of this change" className="" /></div>
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

export function ApproveEvidence({ changeId, soFar, diffstat, shortfall, unchecked, boot, head, notApplicable }: {
  changeId: string; soFar?: string; diffstat?: string; shortfall?: string; unchecked?: string; boot?: StepOf<'preview'>; head?: string
  notApplicable?: { name: string; why: string }[]
}) {
  const asked = useRef(false)
  const [askErr, setAskErr] = useState('')
  useEffect(() => {
    if (soFar || asked.current) return
    asked.current = true
    post(`/changes/${changeId}/distill`, {}).catch((e) => setAskErr((e as Error).message))
  }, [soFar, changeId])
  return (
    <div className="ch-approve-evidence">
      {soFar
        ? <div><div className="ch-label">What you'd be shipping</div><SoFarNote text={soFar} /></div>
        : askErr
          ? <div className="error small">The summary of what got built could not be written: {askErr}</div>
          : <div className="muted small">Writing the summary of what got built…</div>}
      <div className="row" style={{ marginTop: 8 }}>
        <TryIt changeId={changeId} label="▶ Try it first" className="" />
        {diffstat && <span className="muted small">{diffstat}</span>}
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
    <span className="muted small">
      {!confirming ? (
        <>Not ready to decide?{' '}
          <button className="subtle" onClick={() => setConfirming(true)}>Keep working</button>
          <span> stops the landing and reopens the chat; nothing is lost.</span>
        </>
      ) : (
        <>Stop the landing and go back to the chat?{' '}
          <button className="subtle" disabled={busy} onClick={async () => {
            const done = await run(async () => { await post(`/runs/${runId}/abandon`, { reason: 'returned to the change chat to keep working' }); return true })
            if (done) nav(`/projects/${view.projectSlug}/console?change=${view.change.id}`)
            else setConfirming(false)
          }}>{busy ? 'Stopping…' : 'Yes, back to the chat'}</button>
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
      ? <div className="row ch-primary"><button className="primary" onClick={() => setOpen(true)}>Send it back with a note</button>
          <span className="muted small">the landing stops and the change goes back to work with your words</span></div>
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
