import { useContext, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { type ChangeView, currentUser, post } from '../../api'
import type { ConsoleOpened } from '../../wire.gen'
import { useAction } from '../../data'
import { DecisionChoices, DecisionGrid, DecisionContextOnly, type DecisionChoice } from '../../DecisionChoices'
import { QuietConfirm, QuietPrompt } from '../../QuietConfirm'
import { TechnicalWords, useTechnicalDetail } from './TechnicalDetail'
import { stepTitle } from './Evidence'
import { recoveryNote } from './StatePanel'
import { nextStep, offered, rerunLabel } from './next'

export function heldLabels(view: ChangeView): string[] {
  const next = nextStep(view), d = view.detail
  if (next.recovery && d) return [
    ...d.next.flatMap(a => a.act === 'override' && d.steps.some(s => s.id === a.stepId && s.type !== 'approval') ? ['Accept it anyway'] : []),
    ...(offered(d.next, 'rework') ? ['Take it to chat'] : []),
    ...(offered(view.next, 'send-back') ? ['Fix it'] : []),
    ...d.next.flatMap(a => {
      const target = d.steps.find(s => s.id === a.stepId)
      return target && a.act === 'retry' ? [rerunLabel(target)] : []
    }),
    ...(d.run.status === 'running' && view.change.state !== 'landing' ? ['Stop this attempt'] : []),
    ...(offered(view.next, 'abandon') ? ['Abandon change'] : []),
  ]
  return [
    ...(['take-to-chat', 'send-back', 'pick-up'].includes(next.primary) ? ['Go to chat'] : []),
    ...(offered(view.next, 'send-back') ? ['Rework with comment'] : []),
    ...(offered(view.next, 'abandon') ? ['Abandon change'] : []),
  ]
}

export function HeldDecision({ view, onChange }: { view: ChangeView; onChange: () => void }) {
  const [technical] = useTechnicalDetail()
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
    const acceptance = d.next.find(a => a.act === 'override' && d.steps.some(s => s.id === a.stepId && s.type !== 'approval'))
    const fix = offered(view.next, 'send-back') && <button className={!acceptance && next.primary === 'send-back' ? 'primary' : undefined} disabled={busy}
      onClick={() => run(() => post(`/changes/${view.change.id}/sendback`, { note: recoveryNote(d) }))}>Fix it</button>
    const chat = agent && <button className={!acceptance && next.primary === 'take-to-chat' ? 'primary' : undefined} disabled={busy} onClick={takeToChat}>Take it to chat</button>
    return <>
      <p>{next.headline}</p>
      {next.sentence && <p className="ch-review-text">{next.sentence}</p>}
      <TechnicalWords shown={technical}>{next.step?.name}</TechnicalWords>
      {!contextOnly && <DecisionGrid>
        {d.next.map(a => {
          const target = d.steps.find(s => s.id === a.stepId)
          if (!target || a.act !== 'override' || target.type === 'approval') return null
          return <QuietPrompt key={`override-${target.id}`} disabled={busy} label="Accept it anyway" yes="Accept it anyway"
            className={a === acceptance ? 'primary' : undefined}
            ask={`Accept the change although ${stepTitle(target).toLowerCase()} did not pass? This records an exception in your name, with your reason; the check's result stays on the record.`}
            fields={[{ key: 'reason', placeholder: 'Why (required)', required: true }]}
            onYes={v => run(() => post(`/steps/${target.id}/override`, { user: currentUser(), reason: v.reason.trim() }))} />
        })}
        {chat}{fix}
        {d.next.map(a => {
          const target = d.steps.find(s => s.id === a.stepId)
          if (!target || a.act !== 'retry') return null
          return <button key={`retry-${target.id}`} className={!acceptance && next.primary === 'try-again' && next.step?.id === target.id ? 'primary' : undefined} disabled={busy}
            onClick={() => run(() => post(`/steps/${target.id}/run`))}>{rerunLabel(target)}</button>
        })}
        {labels.includes('Stop this attempt') && <QuietConfirm className="" label="Stop this attempt" disabled={busy} ask="Stop this attempt? Nothing is deleted; the change stays open." onYes={() => run(() => post(`/runs/${d.run.id}/abandon`, { reason: 'stopped from the change page' }))} />}
        {offered(view.next, 'abandon') && <QuietConfirm label="Abandon change" yes="Abandon" no="Keep it" className="danger" disabled={busy}
          ask="Abandon this change? The branch is kept; the workspace is removed." onYes={() => run(() => post(`/changes/${view.change.id}/abandon`, {}))} />}
      </DecisionGrid>}
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
