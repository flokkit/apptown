import type { ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import './styles/list-table.css'

export type ListColumn<T> = {
  key: string
  label: string
  render: (row: T) => ReactNode
  kind?: 'number' | 'technical' | 'time'
  // phone places the cell in a phone's stacked row: the name leads, the
  // action sits beside it, the state is the first value; any other column
  // is a value under its label, in column order.
  phone?: 'name' | 'state' | 'action'
  // wideOnly leaves the column out on phones, where another cell says it.
  wideOnly?: boolean
  // className marks the column's header and cells, e.g. for a page to drop
  // it when the table is narrow.
  className?: string
}

// Native column headers on wide screens. On phones each row is a stack: the
// name on its own line with the action beside it, then each value on its own
// line under its column's label, so no two values share a place and none
// squeezes the name. Desktop truncation keeps inventories compact; full facts
// stay on the linked record page.
// rowTo makes a whole row open its record; a link or button inside the row
// keeps its own act.
// A column fits its content when what it holds is short by kind: a status,
// a time, a number or the row's menu.
const fits = (c: { kind?: string; phone?: string }) => c.phone === 'state' || c.phone === 'action' || c.kind === 'time' || c.kind === 'number'

// A value names its column on a phone, where the headers are hidden; the
// name and the action need no label. The label is drawn by CSS from
// data-label, so the cell's own text stays its value.
const labelled = (c: { label: string; phone?: string }) => !!c.label && c.phone !== 'name' && c.phone !== 'action'

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
    <tbody>{rows.map(row => <tr key={rowKey(row)} id={rowId?.(row)} onClick={open(row) || undefined}>{columns.map(c => <td key={c.key} className={[c.kind, fits(c) && 'record-fit', c.phone === 'action' && 'record-action', `phone-${c.phone ?? 'value'}`, c.wideOnly && 'record-wide', c.className].filter(Boolean).join(' ')} data-label={labelled(c) ? c.label : undefined}>{c.render(row)}</td>)}</tr>)}</tbody>
  </table>
}
