import type { ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import './styles/list-table.css'

export type ListColumn<T> = {
  key: string
  label: string
  render: (row: T) => ReactNode
  kind?: 'number' | 'technical' | 'time'
  phone: 'name' | 'state' | 'action' | 'detail' | 'extra' | 'time'
  // wideOnly leaves the column out on phones, where another cell says it.
  wideOnly?: boolean
  // className marks the column's header and cells, e.g. for a page to drop
  // it when the table is narrow.
  className?: string
}

// Native column headers on wide screens; the same cells form two groups on
// phones, where free text wraps within its cell. Desktop truncation keeps
// inventories compact; full facts stay on the linked record page.
// rowTo makes a whole row open its record; a link or button inside the row
// keeps its own act.
// A column fits its content when what it holds is short by kind: a status,
// a time, a number or the row's menu.
const fits = (c: { kind?: string; phone: string }) => c.phone === 'state' || c.phone === 'action' || c.kind === 'time' || c.kind === 'number'

// rowId gives a row an element id, for a page that links to one row by #hash.
export function ListTable<T>({ label, columns, rows, rowKey, rowTo, rowId, className = '' }: {
  label: string; columns: ListColumn<T>[]; rows: T[]; rowKey: (row: T) => string; rowTo?: (row: T) => string; rowId?: (row: T) => string; className?: string
}) {
  const nav = useNavigate()
  const open = (row: T) => rowTo && ((event: { target: EventTarget }) => {
    if (!(event.target as Element).closest('a, button, input, select, [role=dialog]')) nav(rowTo(row))
  })
  return <table className={`record-table${rowTo ? ' record-table-links' : ''} ${className}`} aria-label={label}>
    <thead><tr>{columns.map(c => <th key={c.key} scope="col" className={[c.kind, fits(c) && 'record-fit', c.phone === 'action' && 'record-action', c.wideOnly && 'record-wide', c.className].filter(Boolean).join(' ')}>{c.label}</th>)}</tr></thead>
    <tbody>{rows.map(row => <tr key={rowKey(row)} id={rowId?.(row)} onClick={open(row) || undefined}>{columns.map(c => <td key={c.key} className={[c.kind, fits(c) && 'record-fit', c.phone === 'action' && 'record-action', `phone-${c.phone}`, c.wideOnly && 'record-wide', c.className].filter(Boolean).join(' ')}>{c.render(row)}</td>)}</tr>)}</tbody>
  </table>
}
