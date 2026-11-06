import { useId, useState, type ReactNode } from 'react'

// The same reading order on every list: purpose, current state, next action,
// then evidence. Domain components keep ownership of status and freshness.
export function ListRow({ identity, state, action, detail, detailAction, listItem = false, className = '' }: {
  identity: ReactNode; state?: ReactNode; action?: ReactNode; detail?: ReactNode; detailAction?: string; listItem?: boolean; className?: string
}) {
  const [open, setOpen] = useState(false)
  const detailId = useId()
  return <div className={`list-row ${className}`} role={listItem ? 'listitem' : undefined}>
    <div className="list-identity">{identity}</div>
    <div className="list-state">{state}</div>
    <div className="list-action">{action}{detailAction && <button aria-expanded={open} aria-controls={detailId} onClick={() => setOpen(!open)}>{detailAction}</button>}</div>
    {detailAction ? open && <div id={detailId} className="list-detail">{detail}</div> : detail && <div className="list-detail muted small">{detail}</div>}
  </div>
}
