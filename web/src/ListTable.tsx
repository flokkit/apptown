import type { ReactNode } from 'react'
import './styles/list-table.css'

export type ListColumn<T> = {
  key: string
  label: string
  render: (row: T) => ReactNode
  kind?: 'number' | 'technical' | 'time'
  phone: 'name' | 'state' | 'action' | 'detail' | 'extra' | 'time'
}

// Native column headers on wide screens; the same cells occupy two lines on
// phones. Truncated facts stay available on the linked record's page.
export function ListTable<T>({ label, columns, rows, rowKey, className = '' }: {
  label: string; columns: ListColumn<T>[]; rows: T[]; rowKey: (row: T) => string; className?: string
}) {
  return <table className={`record-table ${className}`} aria-label={label}>
    <thead><tr>{columns.map(c => <th key={c.key} scope="col" className={[c.kind, c.phone === 'action' && 'record-action'].filter(Boolean).join(' ')}>{c.label}</th>)}</tr></thead>
    <tbody>{rows.map(row => <tr key={rowKey(row)}>{columns.map(c => <td key={c.key} className={[c.kind, c.phone === 'action' && 'record-action', `phone-${c.phone}`].filter(Boolean).join(' ')}>{c.render(row)}</td>)}</tr>)}</tbody>
  </table>
}
