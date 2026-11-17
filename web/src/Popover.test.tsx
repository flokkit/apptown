// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { Disclosure, Popover } from './Popover'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

class TestResizeObserver { observe() {} disconnect() {} }
;(globalThis as { ResizeObserver?: unknown }).ResizeObserver ??= TestResizeObserver

const cleanups: (() => void)[] = []
afterEach(() => { vi.useRealTimers(); cleanups.splice(0).forEach(c => c()) })

function mount() {
  const el = document.body.appendChild(document.createElement('div')), root = createRoot(el)
  act(() => root.render(<>
    <input aria-label="Name" />
    <Disclosure hover trigger={<button>status</button>}><a href="/details">details</a></Disclosure>
  </>))
  cleanups.push(() => { act(() => root.unmount()); el.remove() })
  const input = el.querySelector('input')!, trigger = el.querySelector('button')!, wrapper = trigger.parentElement!
  const hover = () => { const e = new MouseEvent('pointerover', { bubbles: true }); Object.defineProperty(e, 'pointerType', { value: 'mouse' }); act(() => { wrapper.dispatchEvent(e) }); act(() => { vi.advanceTimersByTime(1000) }) }
  return { input, trigger, hover, panel: () => document.body.querySelector('.popover') }
}

// A panel a resting mouse opens leaves focus where the person put it.
it('keeps focus in a field while hover opens a panel', () => {
  vi.useFakeTimers()
  const m = mount()
  m.input.focus()
  m.hover()
  expect(m.panel()).not.toBeNull()
  expect(document.activeElement).toBe(m.input)
})

// A panel opened by a click takes focus for the keyboard, and Escape hands it
// back to the button that opened it.
it('takes focus on click and returns it to the button on Escape', () => {
  const m = mount()
  act(() => { m.trigger.click() })
  expect(m.panel()?.contains(document.activeElement)).toBe(true)
  act(() => { document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })) })
  expect(m.panel()).toBeNull()
  expect(document.activeElement).toBe(m.trigger)
})

it.each([
  [undefined, 400, 200],
  ['end', 400, 200],
  ['start', 400, 400],
  ['start', 900, 616],
] as const)('positions %s alignment at %s within the viewport', (align, left, expected) => {
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    return this.classList.contains('popover') ? new DOMRect(0, 0, 400, 100) : new DOMRect(left, 100, 200, 40)
  })
  const el = document.body.appendChild(document.createElement('div')), root = createRoot(el)
  const anchor = { current: document.createElement('button') }
  try {
    act(() => root.render(<Popover anchor={anchor} onClose={() => {}} id="placement-test" align={align}><span>Progress</span></Popover>))
    expect((document.getElementById('placement-test') as HTMLElement).style.left).toBe(`${expected}px`)
  } finally { act(() => root.unmount()); el.remove(); vi.restoreAllMocks() }
})

// A panel taller than the room on either side of its button goes on the
// roomier side and scrolls there; it never sits over the button, which would
// take the button's clicks. A panel that fits above goes above.
it.each([
  ['too tall for either side', 300, 600, '346px', '414px'],
  ['fits only above', 600, 300, '294px', '586px'],
] as const)('keeps a panel %s off its button', (_case, anchorTop, natural, top, maxHeight) => {
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    return this.classList.contains('popover') ? new DOMRect(0, 0, 400, natural) : new DOMRect(100, anchorTop, 200, 40)
  })
  vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockImplementation(function (this: HTMLElement) {
    return this.classList.contains('popover') ? natural : 0
  })
  const el = document.body.appendChild(document.createElement('div')), root = createRoot(el)
  const anchor = { current: document.createElement('button') }
  try {
    act(() => root.render(<Popover anchor={anchor} onClose={() => {}} id="room-test"><span>Progress</span></Popover>))
    const panel = document.getElementById('room-test') as HTMLElement
    expect(window.innerHeight).toBe(768)
    expect([panel.style.top, panel.style.maxHeight]).toEqual([top, maxHeight])
  } finally { act(() => root.unmount()); el.remove(); vi.restoreAllMocks() }
})
