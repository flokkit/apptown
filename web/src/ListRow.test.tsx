// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { expect, it } from 'vitest'
import { ListRow } from './ListRow'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function mount() {
  const el = document.body.appendChild(document.createElement('div')), root = createRoot(el)
  act(() => root.render(<MemoryRouter initialEntries={['/list']}><Routes>
    <Route path="/list" element={<ListRow to="/item" identity={<span>Item <a href="/elsewhere" onClick={e => e.preventDefault()}>source</a></span>} state={<span>working</span>} />} />
    <Route path="/item" element={<p>the item</p>} />
  </Routes></MemoryRouter>))
  return { el, done: () => { act(() => root.unmount()); el.remove() } }
}

// The way into a list item is to click it: anywhere on the row opens it.
it('opens its record when the row is clicked', () => {
  const m = mount()
  try {
    act(() => { m.el.querySelector<HTMLElement>('.list-state span')!.click() })
    expect(m.el.textContent).toBe('the item')
  } finally { m.done() }
})

// A link inside the row keeps its own act.
it('leaves a link inside the row to its own destination', () => {
  const m = mount()
  try {
    act(() => { m.el.querySelector<HTMLElement>('a')!.click() })
    expect(m.el.textContent).toContain('Item')
  } finally { m.done() }
})
