import { PRODUCT } from '../../brand'
import { Link } from 'react-router-dom'
import { useState } from 'react'
import { ChangeState, ChangeView, Doing, post } from '../../api'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { TryIt } from '../../TryIt'
import { changeStateLabel } from '../../states'

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
  'confirmation — no need to ask again. Run the full checks first (forge check), fix ' +
  'anything with a clear fix, then land it and follow it through. If something needs a ' +
  'decision only I can make, stop and ask me.'

export function WorkshopActions({ changeId, view, robotBusy, sessionId }: { changeId: string; view: ChangeView; robotBusy: boolean; sessionId?: string }) {
  const [confirming, setConfirming] = useState(false)
  const [landed, setLanded] = useState(false)
  const [handed, setHanded] = useState(false)
  const { run, busy: landBusy, err } = useAction()
  // The bar knows where the change is at, so it can point at the NEXT action:
  // when the forge needs the person,
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
        <Link to={`/changes/${changeId}`}><button className="btn-big">See the record</button></Link>
      </div>
    )
  }
  if (doing === 'waiting on you') {
    return (
      <div className="workshop-actions">
        <Link to={`/changes/${changeId}`}>
          <button className="finish robot-busy btn-big" style={{ fontWeight: 700 }}>Review and approve →</button>
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
        <span className="small">The checks are running — <Link to={`/changes/${changeId}`}>watch them here</Link>. Nothing goes live until it is approved.</span>
      </div>
    )
  }
  return (
    <div className="workshop-actions">
      {!confirming ? (
        <>
          <TryIt changeId={changeId} className="primary btn-big" />
          <Link className="go-change" to={`/changes/${changeId}`}><button className="btn-big">Go to change</button></Link>
          <button className="finish btn-big" disabled={robotBusy}
            title={robotBusy ? 'the helper is still working — check and land when it hands back' : `your helper runs ${PRODUCT}'s checks and starts the landing; nothing goes live until it is approved`}
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
    <div className="card" style={{ marginBottom: 8, padding: '8px 12px' }}>
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
        <span style={{ flex: 1 }} />
      </div>
      {soFar && (
        <details style={{ marginTop: 4 }}>
          <summary className="muted small">what's in it so far</summary>
          <div className="small" style={{ whiteSpace: 'pre-wrap', maxHeight: 160, overflowY: 'auto', marginTop: 4 }}>
            {soFar.slice(0, 1200)}
          </div>
        </details>
      )}
    </div>
  )
}

// Where the change stands, as one chip: the status card and the chat's
// folded line (Console.tsx) say it in the same words.
export function changeChip(v: ChangeView) {
  const doing = v.state ?? ''
  return v.change.state === 'shipped' ? <span className="chip pass">shipped ✓</span>
    : doing === 'waiting on you' ? <span className="chip pending">needs you</span>
    : doing === 'blocked' ? <span className="chip fail">blocked</span>
    : v.detail?.run?.status === 'running' ? <span className="chip running">in progress</span>
    : (v.editing ?? 0) > 0 ? <span className="chip running">editing</span>
    : (v.commits?.length ?? 0) > 0 ? <span className="chip">saved, not landed yet</span>
    : <span className="chip">nothing changed yet</span>
}

// The change state, REFLECTED not explained: every change this conversation
// has started, live. A person's sentence visibly becomes a change and moves
// through being built → needs you → shipped.
export type StartedChange = { changeId: string | null; title: string; state: ChangeState; closedAs?: string; doing: Doing | ''; detail: string }

export function StartedChanges({ sessionId }: { sessionId: string }) {
  const q = useGet<{ changes: StartedChange[] }>(`/console/${sessionId}/changes`, { poll: 4000 })
  // Quiet while the first answer is on its way; a failure says so, and an
  // old answer (even an empty one) says it may be old, never "none".
  if (!q.data) return q.err ? <ReadStatus q={q} what="the changes this chat started" className="small" /> : null
  const changes = q.data.changes
  if (changes.length === 0) return <StaleNote q={q} />
  const chip = (c: StartedChange) => {
    if (c.state === 'shipped') return <span className="chip pass">shipped ✓</span>
    if (c.state === 'abandoned') return <span className="chip">{changeStateLabel(c.state, undefined, c.closedAs)}</span>
    if (c.doing === 'waiting on you') return <span className="chip pending">needs you</span>
    if (c.doing === 'blocked') return <span className="chip fail">blocked</span>
    if (c.state === 'landing') return <span className="chip pending">landing</span>
    return <span className="chip running">in progress</span>
  }
  return (
    <div style={{ marginBottom: 8 }}>
      <StaleNote q={q} />
      {changes.map((c) => (
        <Link key={c.changeId} to={`/changes/${c.changeId}`} className="card row"
          style={{ display: 'flex', padding: '8px 12px', marginBottom: 4 }}>
          {chip(c)}
          <strong className="small">{c.title}</strong>
          <span className="muted small">started from this chat{c.detail ? ` · ${c.detail}` : ''}</span>
          <span style={{ flex: 1 }} />
          <span className="muted small">open →</span>
        </Link>
      ))}
    </div>
  )
}
