import { ListRow } from '../../ListRow'
import './change-lists.css'
import { ChangeStatus } from '../../Status'
import { PRODUCT } from '../../brand'
import { Link } from 'react-router-dom'
import { useState } from 'react'
import { ChangeState, ChangeView, Doing, post } from '../../api'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'

// What a change chat shows of its change: its land verb (in the chat bar)
// and the confirmation over the input, its facts (in the ⋯ menu), and the
// changes a chat started. They take the change as the page read it; the
// page shows that read's status.

// The message Check and land hands to the in-session agent — the SAME thing as
// typing "check and land it" in the chat, so the button and the sentence share
// one code path and can't diverge. It names the button the person pressed and
// carries the confirmation (the click), so the agent doesn't re-ask, and points
// it at its negotiation protocol (steering).
export const LAND_REQUEST =
  '[apptown button: Check and land] Please check and land this change now. I pressed Check and land, so treat this as my ' +
  'confirmation — no need to ask again. Run the full checks first (apptown check), fix ' +
  'anything with a clear fix, then land it and follow it through. If something needs a ' +
  'decision only I can make, stop and ask me.'

// Where the change's landing stands, for the bar: the bar knows where the
// change is at, so it points at the NEXT action. When AppTown needs the
// person, the loudest thing on screen is the door to the decision.
export type LandStage = 'ready' | 'confirm' | 'handed' | 'landing' | 'review' | 'shipped'

export function useLanding(changeId: string, view: ChangeView | null, sessionId?: string) {
  const [confirming, setConfirming] = useState(false)
  const [landed, setLanded] = useState(false)
  const [handed, setHanded] = useState(false)
  const { run, busy, err } = useAction()
  const changeState: ChangeState | undefined = view?.change.state
  const doing: Doing | '' = view?.state ?? ''
  const stage: LandStage = changeState === 'shipped' ? 'shipped'
    : doing === 'waiting on you' ? 'review'
      : handed && changeState !== 'landing' ? 'handed'
        : landed || changeState === 'landing' ? 'landing'
          : confirming ? 'confirm' : 'ready'
  const land = async () => {
    // With a live chat, hand the land to the agent (it runs the full checks,
    // fixes what it can, and escalates real decisions) — the same as asking it
    // in the chat. Without one, the headless land workflow.
    const done = await run(async () => {
      if (sessionId) await post(`/console/${sessionId}/say`, { text: LAND_REQUEST })
      else await post(`/changes/${changeId}/land`, {})
      return true
    })
    setConfirming(false)
    if (!done) return
    if (sessionId) setHanded(true)
    else setLanded(true)
  }
  return { stage, busy, err, ask: () => setConfirming(true), cancel: () => setConfirming(false), land }
}
export type Landing = ReturnType<typeof useLanding>

// LandConfirm is the question Check and land asks, over the input, in full
// words; the bar's button may say only "Land".
export function LandConfirm({ landing, hasChat }: { landing: Landing; hasChat: boolean }) {
  if (landing.stage !== 'confirm') return landing.err ? <div className="error small chat-err">{landing.err}</div> : null
  return <div className="land-confirm" role="group" aria-label="Check and land?">
    <span><strong>Check and land?</strong> Your helper runs every check, then starts the landing. Nothing goes live until it is approved.</span>
    <button type="button" onClick={landing.cancel}>Not yet</button>
    <button type="button" className="primary" disabled={landing.busy} onClick={() => void landing.land()}>
      {landing.busy ? (hasChat ? 'Asking your helper…' : 'Starting…') : 'Check and land'}</button>
  </div>
}

// ChangeFacts is what the change holds so far, for the chat's ⋯ menu and
// the steps column's head: the page keeps its room for the conversation.
export function changeFacts(v: ChangeView): string {
  const run = v.detail?.run
  return ((v.editing ?? 0) > 0 ? `${v.editing} file${v.editing === 1 ? '' : 's'} being edited` : (v.diffstat || 'no edits yet')) +
    ((v.commits?.length ?? 0) > 0 ? ` · ${v.commits!.length} commit${v.commits!.length === 1 ? '' : 's'}` : '') +
    (run?.status === 'running' && v.stateDetail ? ` · ${v.stateDetail}` : '')
}

// The words Check and land's button carries in each stage, and its title.
export function landTitle(robotBusy: boolean): string {
  return robotBusy ? 'the helper is still working · check and land when it hands back' : `your helper runs ${PRODUCT}'s checks and starts the landing; nothing goes live until it is approved`
}

// The change state, REFLECTED not explained: every change this conversation
// has started, live. A person's sentence visibly becomes a change and moves
// through being built → needs you → shipped.
export type StartedChange = import('../../wire.gen').ConsoleChange

export function useStartedChanges(sessionId: string | null) {
  return useGet<{ changes: StartedChange[] }>(sessionId ? `/console/${sessionId}/changes` : null, { poll: 4000 })
}

// StartedChanges are rows at the top of the conversation: there is no time
// to place each at the turn that started it.
export function StartedChanges({ q }: { q: ReturnType<typeof useStartedChanges> }) {
  // Quiet while the first answer is on its way; a failure says so, and an
  // old answer (even an empty one) says it may be old, never "none".
  if (!q.data) return q.err ? <ReadStatus q={q} what="the changes this chat started" className="small" /> : null
  const changes = q.data.changes
  if (changes.length === 0) return <StaleNote q={q} />
  return (
    <div className="started-changes">
      <StaleNote q={q} />
      {changes.map((c) => (
        <ListRow key={c.changeId} to={`/changes/${c.changeId}`} className="row started-change-row" state={<ChangeStatus waiting={c.waitingOn} />} identity={<>
          <Link to={`/changes/${c.changeId}`} className="run-link"><strong className="small">{c.title}</strong></Link>
          <span className="muted small">started from this chat{c.detail ? ` · ${c.detail}` : ''}</span>
          </>} />
      ))}
    </div>
  )
}
