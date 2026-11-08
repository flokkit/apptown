// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { Disclosure } from './Popover'

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
