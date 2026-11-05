import { PRODUCT } from '../../brand'
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { type ChangeView, currentUser, post } from '../../api'
import type { ConsoleOpened } from '../../wire.gen'
import { useFailureAdvice } from './advice'
import { useAction } from '../../data'
import { DecisionChoices, type DecisionChoice } from '../../DecisionChoices'
import { TechnicalWords, useTechnicalDetail } from './TechnicalDetail'
import { stepTitle } from './Evidence'
import { recoveryNote } from './StatePanel'
import { nextStep, offered, rerunLabel } from './next'

export function forwardAct(view: ChangeView) {
  const d = view.detail, next = nextStep(view)
  const acts = (d?.next ?? []).filter(a => {
    const s = d?.steps.find(s => s.id === a.stepId)
    return a.act === 'advance' || !!s && (
      a.act === 'override' && s.type !== 'approval' && s.status === 'fail'
      || a.act === 'retry' && (s.status === 'error' || ['artifact/require', 'adr/require', 'scm/merged'].includes(s.type)))
  })
  const preferred = next.primary === 'continue' ? 'advance' : next.primary === 'try-again' || next.primary === 'pick-up' ? 'retry' : next.primary
  return acts.find(a => a.act === preferred && (!next.step || a.stepId === next.step.id)) ?? acts[0]
}
export function heldLabels(view: ChangeView): string[] {
  const a = forwardAct(view), s = view.detail?.steps.find(s => s.id === a?.stepId)
  return [
    ...(a ? [a.act === 'override' ? 'Accept anyway' : a.act === 'advance' ? 'Continue' : rerunLabel(s!)] : []),
    ...(offered(view.next, 'send-back') || offered(view.detail?.next, 'rework') ? ['Send back…'] : []),
    ...(offered(view.next, 'abandon') ? ['Abandon change'] : []),
  ]
}
export function HeldDecision({ view, onChange, resultShown = false }: { view: ChangeView; onChange: () => void; resultShown?: boolean }) {
  const advice = useFailureAdvice(view)
  const [technical] = useTechnicalDetail()
  const [note, setNote] = useState(view.detail ? recoveryNote(view.detail) : '')
  const { run, busy, err } = useAction(onChange)
  const nav = useNavigate()
  const next = nextStep(view), d = view.detail, a = forwardAct(view)
  const target = d?.steps.find(s => s.id === a?.stepId)
  const agent = offered(d?.next, 'rework')
  const takeToChat = () => run(async () => {
    const r = await post<ConsoleOpened>(`/projects/${view.projectSlug}/console`, { changeId: view.change.id, continueStep: agent!.stepId })
    nav(`/projects/${view.projectSlug}/console?change=${view.change.id}&session=${r.session.id}&kickoff=stuck`)
  })
  const accept: DecisionChoice | undefined = a ? {
    label: a.act === 'override' ? 'Accept anyway' : a.act === 'advance' ? 'Continue' : rerunLabel(target!),
    run: () => run(() => post(a.act === 'advance' ? `/runs/${d!.run.id}/advance` : `/steps/${target!.id}/run`)),
    ...(a.act === 'override' ? { prompt: {
      ask: `Accept the change although ${stepTitle(target!).toLowerCase()} did not pass? This records an exception in your name, with your reason; the check's result stays on the record.`,
      run: (reason: string) => run(() => post(`/steps/${target!.id}/override`, { user: currentUser(), reason })),
    } } : {}),
  } : undefined
  return <>
    {advice.text && <div><small>{PRODUCT}'s summary</small><p>{advice.text}</p></div>}
    {next.recovery && !resultShown && <><p>{next.headline}</p>{next.sentence && <p className="ch-review-text">{next.sentence}</p>}<TechnicalWords shown={technical}>{next.step?.name}</TechnicalWords></>}
    <DecisionChoices showConversation={!resultShown} note={note} setNote={setNote} busy={busy} accept={accept} highlight={['send-back', 'rework'].includes(advice.recommend) && heldLabels(view).includes('Send back…') ? 'rework' : undefined}
      conversation={{ href: `/changes/${view.change.id}?tab=conversation`, ...(agent ? { run: takeToChat } : { run: () => nav(`/changes/${view.change.id}?tab=conversation`) }) }}
      rework={heldLabels(view).includes('Send back…') ? { label: 'Send back…', run: () => run(() => post(offered(view.next, 'send-back') ? `/changes/${view.change.id}/sendback` : `/steps/${agent!.stepId}/run`, offered(view.next, 'send-back') ? { note } : { feedback: note })) } : undefined}
      abandon={offered(view.next, 'abandon') ? { label: 'Abandon change', run: () => run(() => post(`/changes/${view.change.id}/abandon`, {})) } : undefined} />
    {err && <p className="error">{err}</p>}
  </>
}
