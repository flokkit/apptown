// @vitest-environment jsdom
import { act, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, beforeEach, vi } from 'vitest'
beforeEach(() => vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} }))
import { DecisionChoices } from './DecisionChoices'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const cleanups: (() => void)[] = []
afterEach(() => cleanups.splice(0).forEach(clean => clean()))
function mount() {
  const accepted = vi.fn(), reworked = vi.fn(), abandoned = vi.fn(), chatted = vi.fn()
  function Form() {
    const [note, setNote] = useState('')
    return <DecisionChoices note={note} setNote={setNote} busy={false}
      accept={{ label: 'Accept and put live', run: () => accepted(note) }} conversation={{ href: '?tab=conversation', run: chatted }}
      rework={{ label: 'Rework with comment', run: () => reworked(note) }} abandon={{ label: 'Abandon change', run: abandoned }} />
  }
  const el = document.createElement('div'), root = createRoot(el)
  document.body.append(el)
  act(() => root.render(<Form />))
  cleanups.push(() => { act(() => root.unmount()); el.remove() })
  const click = (label: string) => act(() => [...el.querySelectorAll('button')].find(b => b.textContent === label)!.click())
  return { el, click, accepted, reworked, abandoned, chatted }
}
it('offers at most three equal buttons in order, one highlighted, abandonment last and red, without a note', () => {
  const { el, click, accepted } = mount()
  expect([...el.querySelectorAll('.ch-decision-choices button')].map(b => b.textContent)).toEqual(['Accept and put live', 'Send back…', 'Abandon change'])
  expect([...el.querySelectorAll('.ch-decision-choices button')].map(b => b.className)).toEqual(['primary', '', 'danger'])
  expect(el.querySelector('input, textarea, details')).toBeNull()
  // The decision box is closed to look around; it offers no conversation link.
  expect(el.textContent).not.toContain('Want to talk it through first?')
  click('Accept and put live')
  expect(accepted).toHaveBeenCalledWith('')
})
it('opens a focused comment step, disables sending empty text, cancels exactly and sends text', () => {
  const { el, click, reworked } = mount()
  const buttons = el.querySelector('.ch-decision-choices')!.innerHTML
  click('Send back…')
  expect(el.querySelector('.ch-decision-choices')).toBeNull()
  expect(el.querySelector('h3')?.textContent).toBe('What should change?')
  const field = el.querySelector('textarea')!
  expect(document.activeElement).toBe(field)
  expect([...el.querySelectorAll('button')].find(b => b.textContent === 'Send back')!.disabled).toBe(true)
  expect(el.querySelector('[role=alert]')).toBeNull()
  click('Cancel')
  expect(el.querySelector('.ch-decision-choices')!.innerHTML).toBe(buttons)
  click('Send back…')
  act(() => {
    const field = el.querySelector('textarea')!
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(field, 'Show the winter start time')
    field.dispatchEvent(new Event('input', { bubbles: true }))
  })
  click('Send back')
  expect(reworked).toHaveBeenCalledWith('Show the winter start time')
})
it('asks inline before abandoning and lets the viewer keep it', () => {
  const { el, click, abandoned } = mount()
  click('Abandon change')
  expect(abandoned).not.toHaveBeenCalled()
  expect(el.querySelectorAll('button')).toHaveLength(2)
  expect(el.querySelector('.ch-inline-confirm')?.textContent).toBe('Abandon this change? The branch is kept; the workspace is removed.AbandonKeep it')
  click('Keep it')
  expect(el.querySelector('.ch-inline-confirm')).toBeNull()
  click('Abandon change'); click('Abandon')
  expect(abandoned).toHaveBeenCalledTimes(1)
})
it('omits acts that are not offered', () => {
  const el = document.createElement('div'), root = createRoot(el)
  act(() => root.render(<DecisionChoices note="" setNote={() => {}} busy={false} conversation={{ href: '?tab=conversation' }} rework={{ label: 'Send back…', run: () => {} }} />))
  expect([...el.querySelectorAll('button')].map(b => b.textContent)).toEqual(['Send back…'])
  expect(el.querySelector('.danger, .primary')).toBeNull()
  act(() => root.unmount())
})
