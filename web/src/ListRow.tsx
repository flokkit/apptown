import type { ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'

type Props = { identity: ReactNode; state?: ReactNode; action?: ReactNode; detail?: ReactNode; detailAction?: string; listItem?: boolean; className?: string; to?: string }

// The same reading order on every list: purpose, current state, next action,
// then evidence. Domain components keep ownership of status and freshness.
// to makes the whole row open its record, as ListTable's rowTo does: the way
// into a list item is to click it, not a View button. A link or button inside
// the row keeps its own act; the identity keeps a real link for the keyboard.
export function ListRow(props: Props) {
  return props.to ? <LinkedRow {...props} to={props.to} /> : <Row {...props} />
}

function LinkedRow(props: Props & { to: string }) {
  const nav = useNavigate()
  return <Row {...props} onOpen={event => {
    if (!(event.target as Element).closest('a, button, input, select, textarea, [role=dialog]')) nav(props.to)
  }} />
}

function Row({ identity, state, action, detail, detailAction, listItem = false, className = '', onOpen }: Props & { onOpen?: (event: { target: EventTarget }) => void }) {
  return <div className={`list-row${onOpen ? ' list-row-link' : ''} ${className}`} role={listItem ? 'listitem' : undefined} onClick={onOpen}>
    <div className="list-identity">{identity}</div>
    <div className="list-state">{state}</div>
    <div className="list-action">{action}</div>
    {detail && <div className="list-detail muted small">{detailAction && <h3>{detailAction}</h3>}{detail}</div>}
  </div>
}
