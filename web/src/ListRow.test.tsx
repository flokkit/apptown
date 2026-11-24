// @vitest-environment jsdom
import { act, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { expect, it } from 'vitest'
import { createPortal } from 'react-dom'
import { Link } from 'react-router-dom'
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

function Location() { const l = useLocation(); return <output>{l.pathname}{l.search}</output> }

it('opens from state text inside a containing dialog', () => {
  const el = document.createElement('div'), root = createRoot(el)
  try {
    act(() => root.render(<MemoryRouter initialEntries={['/list']}><Routes>
      <Route path="/list" element={<div role="dialog"><ListRow to="/item" identity={<Link to="/item">Item</Link>} state={<span>working</span>} /></div>} /><Route path="*" element={<Location />} />
    </Routes></MemoryRouter>))
    act(() => el.querySelector<HTMLElement>('.list-state span')!.click())
    expect(el.querySelector('output')?.textContent).toBe('/item')
  } finally { act(() => root.unmount()) }
})

it.each([false, true])('ignores plain content and controls in an inner dialog, including portals (%s)', portal => {
  const el = document.createElement('div'), root = createRoot(el), host = document.body.appendChild(document.createElement('div'))
  const dialog = <div role="dialog"><span>Dialog content</span><button>Act</button></div>
  function Item() {
    const [open, setOpen] = useState(false)
    return <ListRow to="/item" identity={<Link to="/item">Item</Link>} action={<button onClick={() => setOpen(true)}>Show dialog</button>} detail={open && (portal ? createPortal(dialog, host) : dialog)} />
  }
  try {
    act(() => root.render(<MemoryRouter initialEntries={['/list']}><Routes>
      <Route path="/list" element={<Item />} /><Route path="*" element={<Location />} />
    </Routes></MemoryRouter>))
    act(() => el.querySelector<HTMLElement>('.list-action button')!.click())
    for (const selector of ['span', 'button']) {
      act(() => (portal ? host : el).querySelector<HTMLElement>(`[role="dialog"] ${selector}`)!.click())
      expect(el.querySelector('.list-row')).not.toBeNull()
      expect(el.querySelector('output')).toBeNull()
    }
  } finally { act(() => root.unmount()); host.remove() }
})
