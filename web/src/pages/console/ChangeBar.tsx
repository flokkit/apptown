import { ListRow } from '../../ListRow'
import './change-lists.css'
import { Status } from '../../Status'
import { PRODUCT } from '../../brand'
import { Link } from 'react-router-dom'
import { useState } from 'react'
import { ChangeState, ChangeView, Doing, post } from '../../api'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { TryIt } from '../../TryIt'

// What a change chat shows of its change: the bar of its three verbs, the
// change's status card, and the changes a chat started. The bar and the card
// take the change as the page read it; the page shows that read's status.

// WorkshopActions: the workshop's three verbs as one bar of equal,
// big buttons right under the header — see it (Try it), inspect it (Go to
// change), ship it (Check and land, the same words as on the change page).
// Check and land waits for the robot: checks run on the work, not on the
// middle of it.
// The message Check and land hands to the in-session agent — the SAME thing as
// typing "check and land it" in the chat, so the button and the sentence share
// one code path and can't diverge. It names the button the person pressed and
// carries the confirmation (the click), so the agent doesn't re-ask, and points
// it at its negotiation protocol (steering).
export const LAND_REQUEST =
  'Please check and land this change now. I pressed Check and land, so treat this as my ' +
  'confirmation — no need to ask again. Run the full checks first (apptown check), fix ' +
  'anything with a clear fix, then land it and follow it through. If something needs a ' +
  'decision only I can make, stop and ask me.'

// compact is the chat's strip: ordinary-size buttons, and no Go to change,
// since the chat's title links to the change.
export function WorkshopActions({ changeId, view, robotBusy, sessionId, hideRestart = false, compact = false }: { changeId: string; view: ChangeView; robotBusy: boolean; sessionId?: string; hideRestart?: boolean; compact?: boolean }) {
  const [confirming, setConfirming] = useState(false)
  const [landed, setLanded] = useState(false)
  const [handed, setHanded] = useState(false)
  const { run, busy: landBusy, err } = useAction()
  // The bar knows where the change is at, so it can point at the NEXT action:
  // when AppTown needs the person,
  // the loudest thing on screen is the door to the decision.
  // The page reads the change once for this bar and ChangeContext, and says
  // there whether that read is old; the bar shows only on an answer.
  const changeState: ChangeState = view.change.state
  const doing: Doing | '' = view.state ?? ''
  const land = async () => {
    // With a live chat, hand the land to the agent (it runs the full checks,
    // fixes what it can, and escalates real decisions) — the same as asking it
    // in the chat. Without one, fall back to the headless land workflow.
    const done = await run(async () => {
      if (sessionId) await post(`/console/${sessionId}/say`, { text: LAND_REQUEST })
      else await post(`/changes/${changeId}/land`, {})
      return true
    })
    if (!done) { setConfirming(false); return }
    if (sessionId) setHanded(true)
    else setLanded(true)
  }
  if (changeState === 'shipped') {
    return (
      <div className="workshop-actions">
        <span className="small"><strong>Shipped to main.</strong></span>
        <Link to={`/changes/${changeId}`}><button>See the record</button></Link>
      </div>
    )
  }
  if (doing === 'waiting on you') {
    return (
      <div className="workshop-actions">
        <Link to={`/changes/${changeId}`}>
          <button className="finish robot-busy">Review and approve →</button>
        </Link>
        <span className="small">waiting for a person to approve</span>
      </div>
    )
  }
  if (handed && changeState !== 'landing') {
    return (
      <div className="workshop-actions">
        <span className="small"><strong>Your helper has the request to check and land</strong> — it runs the checks, fixes what it can, then starts the landing. Follow along in the chat below; it stops and asks if it needs a decision.</span>
      </div>
    )
  }
  if (landed || changeState === 'landing') {
    return (
      <div className="workshop-actions">
        <span className="small">The checks are running · <Link to={`/changes/${changeId}`}>watch them here</Link>. Nothing goes live until it is approved.</span>
      </div>
    )
  }
  return (
    <div className="workshop-actions">
      {!confirming ? (
        <>
          <TryIt changeId={changeId} hideRestart={hideRestart} hideBuilt={compact} className="primary" />
          {!compact && <Link className="go-change" to={`/changes/${changeId}`}><button>Go to change</button></Link>}
          <button className="finish" disabled={robotBusy}
            title={robotBusy ? 'the helper is still working · check and land when it hands back' : `your helper runs ${PRODUCT}'s checks and starts the landing; nothing goes live until it is approved`}
            onClick={() => setConfirming(true)}>✓ Check and land</button>
        </>
      ) : (
        <>
          <span className="small"><strong>Check and land?</strong> Your helper runs every check, then starts the landing. Nothing goes live until it is approved.</span>
          <button className="primary" disabled={landBusy} onClick={land}>{landBusy ? (sessionId ? 'Asking your helper…' : 'Starting…') : 'Check and land'}</button>
          <button className="subtle" onClick={() => setConfirming(false)}>Not yet</button>
        </>
      )}
      {err && <span className="error small">{err}</span>}
    </div>
  )
}

// The chrome around a change-scoped chat reflects the change as it grows:
// title, what's-in-it-so-far, the diff so far, the latest run's state, and
// a try-it link when a preview is up. Polls in place — same layout, fresher
// facts; the conversation's effects are visible without leaving the chat.
export function ChangeContext({ view: v }: { view: ChangeView }) {
  const run = v.detail?.run
  const chip = changeChip(v)
  const soFar = v.change.summary || v.report
  return (
    <div className="card mb-2" style={{ padding: '8px 12px' }}>
      <div className="row">
        {chip}
        <strong className="small">{v.change.title}</strong>
        <span className="muted small">
          {(v.editing ?? 0) > 0
            ? `${v.editing} file${v.editing === 1 ? '' : 's'} being edited`
            : (v.diffstat || 'no edits yet')}
          {(v.commits?.length ?? 0) > 0 && ` · ${v.commits!.length} commit${v.commits!.length === 1 ? '' : 's'}`}
          {run?.status === 'running' && v.stateDetail ? ` · ${v.stateDetail}` : ''}
        </span>
        <span className="grow" />
      </div>
      {soFar && <p><Link to={`/changes/${v.change.id}/records/summary`}>What was made</Link></p>}
    </div>
  )
}

// ChangeFacts is what the change holds so far, for the chat's ⋯ menu: the
// conversation's page keeps its room for the conversation.
export function ChangeFacts({ view: v }: { view: ChangeView }) {
  const run = v.detail?.run
  return <span className="muted small console-facts">
    {(v.editing ?? 0) > 0
      ? `${v.editing} file${v.editing === 1 ? '' : 's'} being edited`
      : (v.diffstat || 'no edits yet')}
    {(v.commits?.length ?? 0) > 0 && ` · ${v.commits!.length} commit${v.commits!.length === 1 ? '' : 's'}`}
    {run?.status === 'running' && v.stateDetail ? ` · ${v.stateDetail}` : ''}
  </span>
}

// Where the change stands, as one chip: the status card and the chat's
// folded line (Console.tsx) say it in the same words.
export function changeChip(v: ChangeView) {
  return <Status word={v.statusLabel} help={v.stateDetail || undefined} />
}

// The change state, REFLECTED not explained: every change this conversation
// has started, live. A person's sentence visibly becomes a change and moves
// through being built → needs you → shipped.
export type StartedChange = { changeId: string | null; title: string; state: ChangeState; closedAs?: string; stateLabel: string; statusLabel: import('../../wire.gen').DisplayStatus; doing: Doing | ''; detail: string }

export function StartedChanges({ sessionId }: { sessionId: string }) {
  const q = useGet<{ changes: StartedChange[] }>(`/console/${sessionId}/changes`, { poll: 4000 })
  // Quiet while the first answer is on its way; a failure says so, and an
  // old answer (even an empty one) says it may be old, never "none".
  if (!q.data) return q.err ? <ReadStatus q={q} what="the changes this chat started" className="small" /> : null
  const changes = q.data.changes
  if (changes.length === 0) return <StaleNote q={q} />
  const chip = (c: StartedChange) => <Status word={c.statusLabel} help={c.detail || undefined} />
  return (
    <div className="mb-2">
      <StaleNote q={q} />
      {changes.map((c) => (
        <ListRow key={c.changeId} to={`/changes/${c.changeId}`} className="card row mb-1 started-change-row" state={chip(c)} identity={<>
          <Link to={`/changes/${c.changeId}`} className="run-link"><strong className="small">{c.title}</strong></Link>
          <span className="muted small">started from this chat{c.detail ? ` · ${c.detail}` : ''}</span>
          </>} />
      ))}
    </div>
  )
}
