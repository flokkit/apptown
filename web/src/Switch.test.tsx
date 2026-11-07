// @vitest-environment jsdom
import { act, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it } from 'vitest'
import { Switch } from './Switch'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
it('supports a labelled, reversible immediate setting and a disabled state', () => {
 const flips: boolean[] = [], el = document.createElement('div'), root = createRoot(el)
 function Setting({ disabled = false }: { disabled?: boolean }) {
  const [on, setOn] = useState(false)
  return <Switch label="Evals" checked={on} disabled={disabled} onChange={next => { flips.push(next); setOn(next) }} />
 }
 try {
  act(() => root.render(<Setting />))
  const button = el.querySelector<HTMLButtonElement>('[role=switch]')!
  expect(button.getAttribute('aria-label')).toBe('Evals')
  expect(button.getAttribute('aria-checked')).toBe('false')
  act(() => button.click())
  expect(button.getAttribute('aria-checked')).toBe('true')
  expect(button.textContent).toBe('✓')
  act(() => button.click())
  expect(button.getAttribute('aria-checked')).toBe('false')
  act(() => root.render(<Setting disabled />))
  act(() => button.click())
  expect(flips).toEqual([true, false])
 } finally { act(() => root.unmount()) }
})
