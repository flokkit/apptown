import { ReactNode, createContext, useContext, useEffect, useRef, useState } from 'react'
import { DecisionContextOnly } from '../../DecisionChoices'
import type { ChangeView } from '../../api'

const OpenDecision = createContext(() => {})

export function ContinueDecision({ busy, run }: { busy: boolean; run: () => unknown }) {
  const open = useContext(OpenDecision)
  return <button className="primary" disabled={busy} onClick={() => { open(); run() }}>Continue</button>
}

// Only an eligible approval or an offered recovery needs the viewer now.
export function viewerTurn(view: ChangeView): boolean {
  return view.waitingOn.you === 'yes' && ['approval', 'check', 'person'].includes(view.waitingOn.kind)
}

export function TurnDecision({ active, actions, children, approvalWaiting = active, shown = true, ready, progress, progressTitle, doneTitle, outcome }: { active: boolean; actions: string[]; children: ReactNode; approvalWaiting?: boolean; shown?: boolean; ready?: ReactNode; progress?: ReactNode; progressTitle?: string; doneTitle?: string; outcome?: ReactNode }) {
  const card = useRef<HTMLElement>(null)
  const surface = useRef<HTMLDivElement>(null)
  const dialog = useRef<HTMLDialogElement>(null)
  const [open, setOpen] = useState(false)
  const actionKey = actions.join('|')
  useEffect(() => {
    const page = card.current?.closest<HTMLElement>('.ch-page')
    if ((!active && !open) || !page || !surface.current) return
    // The button and the decision screen sit over the page content, never over the menu.
    const measure = () => {
      const bounds = page.getBoundingClientRect()
      surface.current?.style.setProperty('left', `${bounds.left}px`)
      surface.current?.style.setProperty('width', `${bounds.width}px`)
      dialog.current?.style.setProperty('--page-left', `${bounds.left}px`)
      dialog.current?.style.setProperty('--page-width', `${bounds.width}px`)
      page.style.paddingBottom = `${active ? surface.current?.getBoundingClientRect().height ?? 0 : 0}px`
    }
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(page)
    observer.observe(surface.current)
    window.addEventListener('resize', measure)
    return () => { observer.disconnect(); window.removeEventListener('resize', measure); page.style.paddingBottom = '' }
  }, [active, open, actionKey])
  useEffect(() => {
    if (open) dialog.current?.showModal()
    else { dialog.current?.close(); setOpen(false) }
  }, [open])
  // The screen is named for what it shows: the decision, the work in progress, or what comes next.
  const title = approvalWaiting ? 'Accept this change?' : progress && progressTitle ? progressTitle : doneTitle ?? 'Next steps'
  return <OpenDecision.Provider value={() => setOpen(true)}>
    {/* While it is the viewer's turn the decision lives in the Decide… screen; the page keeps an anchor for measuring. */}
    <section id="decision" ref={card} hidden={!shown || active || open} className={`ch-decision${active ? ' ch-your-turn' : ''}`}>
      {active && <div className="ch-turn-label">Your turn</div>}
      <h2>{approvalWaiting ? 'Your decision' : doneTitle ?? 'Next steps'}</h2>
      {ready}
      <DecisionContextOnly.Provider value={active || open}>{children}</DecisionContextOnly.Provider>
    </section>
    {(active || open) && <>
      <div hidden={!active} ref={surface} style={{ visibility: open ? 'hidden' : undefined }} className="ch-turn-floater" aria-label="Your turn">
        <button className="ch-decide" onClick={() => setOpen(true)}>Decide…</button>
      </div>
      <dialog ref={dialog} className="ch-decision-screen" aria-label={title} onClose={() => setOpen(false)}>
        {open && <><header><h2>{title}</h2><button onClick={() => setOpen(false)}>Close</button></header>{progress}<div hidden={!!progress}>{!active && outcome}{ready}{children}</div></>}
      </dialog>
    </>}
  </OpenDecision.Provider>
}
