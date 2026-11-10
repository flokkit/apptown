// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { Status } from './Status'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('keeps a status inside a link to the link: its details are the title, with no control of its own', () => {
  const el = document.createElement('div'), root = createRoot(el)
  try {
    act(() => root.render(<a href="/deployment"><Status word="unreachable" help="The app may still be running." /></a>))
    expect(el.querySelector('details, summary, button')).toBeNull()
    expect(el.querySelector('.chip')?.getAttribute('title')).toBe('The app may still be running.')
    expect(el.querySelector('a')?.getAttribute('href')).toBe('/deployment')
    expect(el.querySelector('[role="status"]')).toBeNull()
  } finally { act(() => root.unmount()) }
})

// Hovering a status with a mouse opens the same details panel a click does,
// and leaving closes it; there is no separate browser tooltip.
it('opens the same details on hover as on click, and shows no browser tooltip', async () => {
  const el = document.body.appendChild(document.createElement('div')), root = createRoot(el)
  try {
    act(() => root.render(<Status word="failed" help="Tests did not pass." />))
    const button = el.querySelector<HTMLButtonElement>('button.chip-button')!
    expect(button.hasAttribute('title')).toBe(false)
    const wrapper = button.parentElement!
    const pointer = (type: string) => { const e = new MouseEvent(type, { bubbles: type === 'pointerover' || type === 'pointerout' }) as MouseEvent & { pointerType: string }; Object.defineProperty(e, 'pointerType', { value: 'mouse' }); return e }
    vi.useFakeTimers()
    act(() => { wrapper.dispatchEvent(pointer('pointerover')) })
    // A mouse passing over does not open it; one resting a second does.
    act(() => { vi.advanceTimersByTime(900) })
    expect(document.body.querySelector('.status-word-detail')).toBeNull()
    act(() => { vi.advanceTimersByTime(100) })
    expect(document.body.querySelector('.status-word-detail')?.textContent).toBe('Tests did not pass.')
    vi.useRealTimers()
    act(() => { button.click() })
    expect(document.body.querySelector('.status-word-detail')?.textContent).toBe('Tests did not pass.')
  } finally { vi.useRealTimers(); act(() => root.unmount()); el.remove() }
})

// Details that only repeat the word are not details: the chip is plain, with
// nothing to open.
it('opens nothing when the details only repeat the word', () => {
  const el = document.createElement('div'), root = createRoot(el)
  try {
    act(() => root.render(<><Status word="running" help="running" /><Status word="failed" help="Failed" /><Status word="passed" help="" /></>))
    expect(el.querySelector('.chip-button')).toBeNull()
    expect(el.querySelectorAll('.chip').length).toBe(3)
  } finally { act(() => root.unmount()) }
})
