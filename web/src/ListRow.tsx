import { useId, useState, Children, isValidElement, type ReactElement, type ReactNode, type CSSProperties } from 'react'

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

// Table-backed inventories use the same row without a second phone layout.
export function TableListRow({ children, name, state, stateExtra, purpose, freshness, action, actionCell, labels, detail, detailAction, style }: {
  children: ReactNode; style?: CSSProperties; name: number; state?: number; stateExtra?: number; purpose?: number; freshness?: number; action?: ReactNode; actionCell?: number; labels: string[]; detail?: ReactNode; detailAction?: string
}) {
  const cells = Children.toArray(children).filter(isValidElement) as ReactElement<{ children?: ReactNode }>[]
  const content = (i?: number) => i === undefined ? null : cells[i]?.props.children
  const extras = cells.map((cell, i) => ({ cell, i })).filter(({ i }) => ![name, state, stateExtra, purpose, freshness, actionCell].includes(i))
  return <tr className="list-table-row" style={style}><td colSpan={cells.length}>
    <ListRow identity={<>{content(name)}{purpose !== undefined && <div className="muted small">{content(purpose)}</div>}</>} state={<>{content(state)}{content(stateExtra)}{freshness !== undefined && <div className="muted small">{content(freshness)}</div>}</>} action={action ?? content(actionCell)} detailAction={detailAction}
      detail={extras.length || detail ? <>{extras.length > 0 && <dl>{extras.map(({ cell, i }) => <div key={i}><dt>{labels[i]}</dt><dd>{cell.props.children}</dd></div>)}</dl>}{detail}</> : undefined} />
  </td></tr>
}
