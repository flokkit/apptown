import { useContext, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { type ChangeView, currentUser, post } from '../../api'
import type { ConsoleOpened } from '../../wire.gen'
import { useAction } from '../../data'
import { DecisionChoices, DecisionContextOnly, type DecisionChoice } from '../../DecisionChoices'
import { QuietConfirm, QuietPrompt } from '../../QuietConfirm'
import { recoveryNote } from './StatePanel'
import { nextStep, offered, rerunLabel } from './next'

export function heldLabels(view: ChangeView): string[] {
  const next = nextStep(view), d = view.detail
  if (next.recovery && d) return [
    ...(next.primary === 'take-to-chat' && offered(d.next, 'rework') ? ['Take it to chat'] : []),
    ...(offered(view.next, 'send-back') ? ['Fix it'] : []),
    ...(next.primary !== 'take-to-chat' && offered(d.next, 'rework') ? ['Take it to chat'] : []),
    ...d.next.flatMap(a => {
      const target = d.steps.find(s => s.id === a.stepId)
      return target && (a.act === 'retry' || (a.act === 'override' && target.type !== 'approval')) ? [a.act === 'retry' ? rerunLabel(target) : 'Accept it anyway'] : []
    }),
    ...(offered(view.next, 'abandon') ? ['Abandon change'] : []),
  ]
  return [
    ...(['take-to-chat', 'send-back', 'pick-up'].includes(next.primary) ? ['Go to chat'] : []),
    ...(offered(view.next, 'send-back') ? ['Rework with comment'] : []),
    ...(offered(view.next, 'abandon') ? ['Abandon change'] : []),
  ]
}

export function HeldDecision({ view, onChange }: { view: ChangeView; onChange: () => void }) {
  const [note, setNote] = useState('')
  const { run, busy, err } = useAction(onChange)
  const nav = useNavigate()
  const contextOnly = useContext(DecisionContextOnly)
  const next = nextStep(view), d = view.detail
  const agent = next.recovery ? d?.steps.find(s => s.id === offered(d.next, 'rework')?.stepId) : next.step
  const labels = heldLabels(view)
  const choice = (label: string, action: () => unknown): DecisionChoice | undefined => labels.includes(label) ? { label, run: action } : undefined
  const takeToChat = () => run(async () => {
    const r = await post<ConsoleOpened>(`/projects/${view.projectSlug}/console`, { changeId: view.change.id, continueStep: agent?.id })
    nav(`/projects/${view.projectSlug}/console?change=${view.change.id}&session=${r.session.id}&kickoff=stuck`)
  })
  if (next.recovery && d) {
    const fix = offered(view.next, 'send-back') && <button className={next.primary === 'send-back' ? 'primary' : undefined} disabled={busy}
      onClick={() => run(() => post(`/changes/${view.change.id}/sendback`, { note: recoveryNote(d) }))}>Fix it</button>
    const chat = agent && <button className={next.primary === 'take-to-chat' ? 'primary' : undefined} disabled={busy} onClick={takeToChat}>Take it to chat</button>
    return <>
      <p>{next.headline}</p>
      {next.sentence && <p>{next.sentence}</p>}
      {!contextOnly && <div className="ch-decision-choices">
        {next.primary === 'take-to-chat' ? <>{chat}{fix}</> : <>{fix}{chat}</>}
        {d.next.map(a => {
          const target = d.steps.find(s => s.id === a.stepId)
          if (!target) return null
          if (a.act === 'retry') return <button key={`retry-${target.id}`} className={next.primary === 'try-again' && next.step?.id === target.id ? 'primary' : undefined} disabled={busy}
            onClick={() => run(() => post(`/steps/${target.id}/run`))}>{rerunLabel(target)}</button>
          if (a.act !== 'override' || target.type === 'approval') return null
          return <QuietPrompt key={`override-${target.id}`} disabled={busy} label="Accept it anyway" yes="Accept it anyway"
            className={next.primary === 'override' && next.step?.id === target.id ? 'primary' : ''}
            ask={`Accept the change although “${target.name}” did not pass? This records an exception in your name, with your reason; the check's result stays on the record.`}
            fields={[{ key: 'reason', placeholder: 'Why (required)', required: true }]}
            onYes={v => run(() => post(`/steps/${target.id}/override`, { user: currentUser(), reason: v.reason.trim() }))} />
        })}
        {offered(view.next, 'abandon') && <QuietConfirm label="Abandon change" yes="Abandon" no="Keep it" className="danger" disabled={busy}
          ask="Abandon this change? The branch is kept; the workspace is removed." onYes={() => run(() => post(`/changes/${view.change.id}/abandon`, {}))} />}
      </div>}
      {err && <p className="error">{err}</p>}
    </>
  }
  return <>
    <DecisionChoices note={note} setNote={setNote} busy={busy}
      chat={choice('Go to chat', takeToChat)}
      rework={choice('Rework with comment', () => run(() => post(`/changes/${view.change.id}/sendback`, { note })))}
      abandon={choice('Abandon change', () => run(() => post(`/changes/${view.change.id}/abandon`, {})))} />
    {err && <p className="error">{err}</p>}
  </>
}
