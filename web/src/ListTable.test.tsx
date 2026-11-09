// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { expect, it } from 'vitest'
import { ListTable } from './ListTable'

type Part = { name: string; memory: string; note: string | null }

function cells() {
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><ListTable<Part> label="Parts" rows={[{ name: 'postgres', memory: '24.2 MB / 256 MB', note: null }]} rowKey={r => r.name} columns={[
    { key: 'name', label: 'Name', phone: 'name', render: r => r.name },
    { key: 'memory', label: 'Memory', render: r => r.memory },
    { key: 'note', label: 'Note', render: r => r.note },
    { key: 'more', label: '', phone: 'action', render: () => <button>Logs</button> },
  ]} /></MemoryRouter>)
  return [...el.querySelectorAll('tbody td')]
}

// On a phone a row is a stack: each value carries its column's label, so
// two values can never be read as one; the name and the action need none.
// The label is drawn from data-label, so a cell's text stays its value.
it('gives each value cell its column label', () => {
  const [name, memory, note, more] = cells()
  expect(name.hasAttribute('data-label')).toBe(false)
  expect(more.hasAttribute('data-label')).toBe(false)
  expect(memory.getAttribute('data-label')).toBe('Memory')
  expect(memory.textContent).toBe('24.2 MB / 256 MB')
  // A value that renders nothing leaves its cell empty, which phones hide
  // with its label rather than show a label beside nothing.
  expect(note.getAttribute('data-label')).toBe('Note')
  expect(note.childNodes).toHaveLength(0)
})
