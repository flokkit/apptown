import { useState, Children, cloneElement, isValidElement, type ReactElement, type ReactNode, type HTMLAttributes } from 'react'
import { useNarrow } from './chrome'

// A row is its summary. Evidence opens beneath it, with the same click and
// keyboard behaviour at either width. Links and actions keep their own meaning.
export function GlanceRow({ children, name, state, fact, labels, summary, status, detail, inline = false, ...props }: HTMLAttributes<HTMLTableRowElement> & {
  name: number
  state?: number
  fact?: number
  labels: string[]
  status?: ReactNode
  summary?: ReactNode
  detail?: ReactNode
  inline?: boolean
}) {
  const narrow = useNarrow()
  const [open, setOpen] = useState(false)
  const cells = Children.toArray(children).filter(isValidElement) as ReactElement<{ children?: ReactNode; className?: string }>[]
  const chosen = [name, state, fact]
  const extras = cells.map((cell, i) => ({ cell, i })).filter(({ i }) => !chosen.includes(i))
  const expandable = !!detail || (narrow && !inline && extras.length > 0)
  const cell = (i: number | undefined, className: string) => i === undefined ? null : cloneElement(cells[i], { className: `${className} ${cells[i].props.className || ''}` })
  const more = <dl>{extras.map(({ cell, i }) => <div key={i}><dt>{labels[i]}</dt><dd>{cell.props.children}</dd></div>)}</dl>
  return <>
    <tr {...props} className={`${narrow ? 'glance-row ' : ''}${expandable ? 'expandable-row ' : ''}${props.className || ''}`}
      tabIndex={expandable ? 0 : props.tabIndex} aria-expanded={expandable ? open : undefined}
      onClick={e => {
        props.onClick?.(e)
        if (expandable && !(e.target as Element).closest('a,button,input,select,textarea,summary')) setOpen(!open)
      }}
      onKeyDown={e => {
        props.onKeyDown?.(e)
        if (expandable && e.target === e.currentTarget && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); setOpen(!open) }
      }}>
      {!narrow ? children : <>
        {cell(name, 'glance-name')}
        {status !== undefined ? <td className="glance-state">{status}</td> : cell(state, 'glance-state')}
        {summary !== undefined ? <td className="glance-fact">{summary}</td> : cell(fact, 'glance-fact')}
        {inline && extras.length > 0 && <td className="glance-more">{more}</td>}
      </>}
    </tr>
    {expandable && open && <tr className="row-expansion"><td colSpan={cells.length}>
      {narrow && !inline && extras.length > 0 && more}{detail}
    </td></tr>}
  </>
}

export function PhoneDetails({ children }: { children: ReactNode }) {
  const narrow = useNarrow()
  const [open, setOpen] = useState(false)
  if (Children.toArray(children).length === 0) return null
  return <details className="phone-details" open={!narrow || open} onToggle={e => { if (narrow) setOpen(e.currentTarget.open) }}>
    <summary>Details</summary>{children}
  </details>
}
