// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it } from 'vitest'
import { Status } from './Status'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('opens and closes help by tapping inside a linked row without navigating', () => {
  const el = document.createElement('div')
  const root = createRoot(el)
  act(() => root.render(<a href="/deployment"><Status word="unreachable" help="AppTown can't reach Club host since 31 Oct, 00:00. The app may still be running." /></a>))
  const details = el.querySelector('details')!
  const summary = el.querySelector('summary')!
  expect(details.open).toBe(false)
  const tap = new MouseEvent('click', { bubbles: true, cancelable: true })
  act(() => summary.dispatchEvent(tap))
  expect(tap.defaultPrevented).toBe(true)
  expect(details.open).toBe(true)
  expect(details.textContent).toContain('The app may still be running.')
  act(() => summary.click())
  expect(details.open).toBe(false)
  expect(el.querySelector('[role="status"]')).toBeNull()
  act(() => root.unmount())
})

it('keeps one help open beside its chip and dismisses on outside taps or Escape', () => {
  const el = document.createElement('div')
  document.body.append(el)
  const root = createRoot(el)
  act(() => root.render(<><Status word="unknown" help="No runtime reading." /><Status word="unreachable" help="The host did not answer." /><button>Elsewhere</button></>))
  const summaries = el.querySelectorAll('summary')
  const details = el.querySelectorAll('details')
  const viewport = window.innerWidth
  Object.defineProperty(window, 'innerWidth', { value: 390, configurable: true })
  details[1].getBoundingClientRect = () => ({ left: 300 }) as DOMRect
  act(() => summaries[0].click())
  expect(details[0].open).toBe(true)
  act(() => summaries[1].click())
  expect(details[0].open).toBe(false)
  expect(details[1].open).toBe(true)
  expect(details[1].querySelector('div')!.style.left).toBe('-284px')
  act(() => el.querySelector('button')!.dispatchEvent(new Event('pointerdown', { bubbles: true })))
  expect(details[1].open).toBe(false)
  act(() => summaries[0].click())
  act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
  expect(details[0].open).toBe(false)
  expect(document.activeElement).toBe(summaries[0])
  act(() => root.unmount())
  el.remove()
  Object.defineProperty(window, 'innerWidth', { value: viewport, configurable: true })
})
