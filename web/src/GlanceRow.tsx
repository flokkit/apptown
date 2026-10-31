import { useState, Children, cloneElement, isValidElement, type ReactElement, type ReactNode, type HTMLAttributes } from 'react'
import { useNarrow } from './chrome'

// Each list chooses its name, state and one fact. Secondary cells remain
// reachable on a phone without mounting a second copy of their controls.
export function GlanceRow({ children, name, state, fact, labels, summary, status, ...props }: HTMLAttributes<HTMLTableRowElement> & {
  name: number
  state?: number
  fact?: number
  labels: string[]
  status?: ReactNode
  summary?: ReactNode
}) {
  const narrow = useNarrow()
  if (!narrow) return <tr {...props}>{children}</tr>
  const cells = Children.toArray(children).filter(isValidElement) as ReactElement<{ children?: ReactNode; className?: string }>[]
  const chosen = [name, state, fact]
  const extras = cells.map((cell, i) => ({ cell, i })).filter(({ i }) => !chosen.includes(i))
  const cell = (i: number | undefined, className: string) => i === undefined ? null : cloneElement(cells[i], { className })
  return <tr {...props} className={`glance-row ${props.className || ''}`}>
    {cell(name, 'glance-name')}
    {status !== undefined ? <td className="glance-state">{status}</td> : cell(state, 'glance-state')}
    {summary !== undefined ? <td className="glance-fact">{summary}</td> : cell(fact, 'glance-fact')}
    {extras.length > 0 && <td className="glance-more"><details onClick={e => e.stopPropagation()}>
      <summary>Details</summary>
      <dl>{extras.map(({ cell, i }) => <div key={i}><dt>{labels[i]}</dt><dd>{cell.props.children}</dd></div>)}</dl>
    </details></td>}
  </tr>
}

export function PhoneDetails({ children }: { children: ReactNode }) {
  const narrow = useNarrow()
  const [open, setOpen] = useState(false)
  if (Children.toArray(children).length === 0) return null
  return <details className="phone-details" open={!narrow || open} onToggle={e => { if (narrow) setOpen(e.currentTarget.open) }}>
    <summary>Details</summary>{children}
  </details>
}
