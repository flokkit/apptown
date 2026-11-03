import { ReactNode, useEffect, useRef, useState } from 'react'
import { DecisionContextOnly } from '../../DecisionChoices'
import type { ChangeView } from '../../api'
import { checksRunning, nextStep, offered, viewerMayApprove } from './next'

// Only an eligible approval or an offered recovery needs the viewer now.
export function viewerTurn(view: ChangeView): boolean {
  const next = nextStep(view)
  const steps = view.detail?.steps ?? []
  const busy = view.state === 'blocked' ? steps.some(s => s.status === 'running') : checksRunning(steps, view.detail?.run.currentStage)
  if (['shipped', 'abandoned'].includes(view.change.state) || busy) return false
  if (view.state === 'blocked' && steps.some(s => s.stage === view.detail?.run.currentStage && s.type === 'agent/pi' && s.status === 'pending')
    && !steps.some(s => s.stage === view.detail?.run.currentStage && ['needs_human', 'error'].includes(s.status))) return false
  if (next.primary === 'approve') return !!next.step && viewerMayApprove(view, next.step)
  return ['blocked', 'waiting on you'].includes(view.state) && !!view.detail && (
    ['take-to-chat', 'send-back', 'pick-up', 'restart-landing', 'try-again', 'override'].includes(next.primary)
    || !!offered(view.next, 'send-back')
    || !!next.recovery && !!offered(view.next, 'abandon')
  )
}

export function TurnDecision({ active, actions, children, approvalWaiting = active, shown = true, ready }: { active: boolean; actions: string[]; children: ReactNode; approvalWaiting?: boolean; shown?: boolean; ready?: ReactNode }) {
  const card = useRef<HTMLElement>(null)
  const surface = useRef<HTMLDivElement>(null)
  const dialog = useRef<HTMLDialogElement>(null)
  const [open, setOpen] = useState(false)
  const actionKey = actions.join('|')
  useEffect(() => {
    const page = card.current?.closest<HTMLElement>('.ch-page')
    if (!active || !page || !surface.current) return
    // The button and the decision screen sit over the page content, never over the menu.
    const measure = () => {
      const bounds = page.getBoundingClientRect()
      surface.current?.style.setProperty('left', `${bounds.left}px`)
      surface.current?.style.setProperty('width', `${bounds.width}px`)
      dialog.current?.style.setProperty('--page-left', `${bounds.left}px`)
      dialog.current?.style.setProperty('--page-width', `${bounds.width}px`)
      page.style.paddingBottom = `${surface.current?.getBoundingClientRect().height ?? 0}px`
    }
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(page)
    observer.observe(surface.current)
    window.addEventListener('resize', measure)
    return () => { observer.disconnect(); window.removeEventListener('resize', measure); page.style.paddingBottom = '' }
  }, [active, actionKey])
  useEffect(() => {
    if (open && active) dialog.current?.showModal()
    else { dialog.current?.close(); setOpen(false) }
  }, [open, active])
  return <>
    {/* While it is the viewer's turn the decision lives in the Decide… screen; the page keeps an anchor for measuring. */}
    <section id="decision" ref={card} hidden={!shown || active} className={`ch-decision${active ? ' ch-your-turn' : ''}`}>
      {active && <div className="ch-turn-label">Your turn</div>}
      <h2>{approvalWaiting ? 'Your decision' : 'Next steps'}</h2>
      {ready}
      <DecisionContextOnly.Provider value={active}>{children}</DecisionContextOnly.Provider>
    </section>
    {active && <>
      <div ref={surface} style={{ visibility: open ? 'hidden' : undefined }} className="ch-turn-floater" aria-label="Your turn">
        <button className="ch-decide" onClick={() => setOpen(true)}>Decide…</button>
      </div>
      <dialog ref={dialog} className="ch-decision-screen" aria-label={approvalWaiting ? 'Accept this change?' : 'Next steps'} onClose={() => setOpen(false)}>
        {open && <><header><h2>{approvalWaiting ? 'Accept this change?' : 'Next steps'}</h2><button onClick={() => setOpen(false)}>Close</button></header>{ready}{children}</>}
      </dialog>
    </>}
  </>
}
