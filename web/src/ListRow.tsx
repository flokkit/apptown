import type { ReactNode } from 'react'

// The same reading order on every list: purpose, current state, next action,
// then evidence. Domain components keep ownership of status and freshness.
export function ListRow({ identity, state, action, detail, detailAction, listItem = false, className = '' }: {
  identity: ReactNode; state?: ReactNode; action?: ReactNode; detail?: ReactNode; detailAction?: string; listItem?: boolean; className?: string
}) {
  return <div className={`list-row ${className}`} role={listItem ? 'listitem' : undefined}>
    <div className="list-identity">{identity}</div>
    <div className="list-state">{state}</div>
    <div className="list-action">{action}</div>
    {detail && <div className="list-detail muted small">{detailAction && <h3>{detailAction}</h3>}{detail}</div>}
  </div>
}
