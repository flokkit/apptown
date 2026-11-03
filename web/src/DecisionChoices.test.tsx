// @vitest-environment jsdom
import { act, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { DecisionChoices } from './DecisionChoices'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const cleanups: (() => void)[] = []
afterEach(() => cleanups.splice(0).forEach(clean => clean()))
function mount() {
  const accepted = vi.fn(), reworked = vi.fn(), abandoned = vi.fn(), chatted = vi.fn()
  function Form() {
    const [note, setNote] = useState('')
    return <DecisionChoices note={note} setNote={setNote} busy={false}
      accept={{ label: 'Accept and put live', run: () => accepted(note) }} chat={{ label: 'Go to chat', run: chatted }}
      rework={{ label: 'Rework with comment', run: () => reworked(note) }} abandon={{ label: 'Abandon change', run: abandoned }} />
  }
  const el = document.createElement('div'), root = createRoot(el)
  document.body.append(el)
  act(() => root.render(<Form />))
  cleanups.push(() => { act(() => root.unmount()); el.remove() })
  const click = (label: string) => act(() => [...el.querySelectorAll('button')].find(b => b.textContent === label)!.click())
  return { el, click, accepted, reworked, abandoned, chatted }
}
it('offers four buttons in order with primary acceptance and danger abandonment and accepts with an optional note', () => {
  const { el, click, accepted } = mount()
  expect([...el.querySelectorAll('.ch-decision-choices button')].map(b => b.textContent)).toEqual(['Accept and put live', 'Go to chat', 'Rework with comment', 'Abandon change'])
  expect([...el.querySelectorAll('.ch-decision-choices button')].map(b => b.className)).toEqual(['primary', '', '', 'danger'])
  expect(el.querySelector('.subtle')).toBeNull()
  expect(el.querySelector('input')!.compareDocumentPosition(el.querySelector('.ch-decision-choices')!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  click('Accept and put live')
  expect(accepted).toHaveBeenCalledWith('')
})
it('requires a comment for rework, focuses the note and submits the entered comment', () => {
  const { el, click, reworked } = mount()
  click('Rework with comment')
  expect(reworked).not.toHaveBeenCalled()
  expect(el.querySelector('[role=alert]')?.textContent).toBe('Say what should change')
  const field = el.querySelector('input')!
  expect(document.activeElement).toBe(field)
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(field, 'Show the winter start time')
    field.dispatchEvent(new Event('input', { bubbles: true }))
  })
  click('Rework with comment')
  expect(reworked).toHaveBeenCalledWith('Show the winter start time')
})
it('asks once inline before abandoning and lets the viewer keep it', () => {
  const { el, click, abandoned } = mount()
  click('Abandon change')
  expect(abandoned).not.toHaveBeenCalled()
  expect(el.querySelector('.ch-inline-confirm')?.textContent).toBe('Abandon this change?AbandonKeep it')
  click('Keep it')
  expect(el.querySelector('.ch-inline-confirm')).toBeNull()
  click('Abandon change'); click('Abandon')
  expect(abandoned).toHaveBeenCalledTimes(1)
})
