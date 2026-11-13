import type { ReactNode } from 'react'
import type { ChangeView } from '../../api'

export function ContinueDecision({ busy, run }: { busy: boolean; run: () => unknown }) {
  return <button className="primary" disabled={busy} onClick={run}>Continue</button>
}

export function viewerTurn(view: ChangeView): boolean {
  return view.waitingOn.you === 'yes' && ['approval', 'check', 'person'].includes(view.waitingOn.kind)
}

export function TurnDecision({ active, children, approvalWaiting = active, shown = true, ready, doneTitle }: { active: boolean; children: ReactNode; approvalWaiting?: boolean; shown?: boolean; ready?: ReactNode; doneTitle?: string }) {
  return <section id="decision" hidden={!shown} className={`ch-decision${active ? ' ch-your-turn' : ''}`}>
    {active && <div className="ch-turn-label">Your turn</div>}
    <h2>{approvalWaiting ? 'Your decision' : doneTitle ?? 'Next steps'}</h2>
    {ready}{children}
  </section>
}
