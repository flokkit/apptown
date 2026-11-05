// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { SizeSwitch } from './TopBar'
import { applySize, bootSize, currentSize } from './size'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

beforeEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('data-size')
  history.replaceState(null, '', '/')
})
afterEach(() => vi.restoreAllMocks())

it('boots at Medium with no saved choice or an invalid choice', () => {
  bootSize()
  expect(currentSize()).toBe('medium')
  expect(document.documentElement.dataset.size).toBe('medium')
  localStorage.setItem('apptown-size', 'huge')
  history.replaceState(null, '', '/?size=tiny')
  bootSize()
  expect(currentSize()).toBe('medium')
})

it.each(['small', 'medium', 'large'] as const)('restores the saved %s size before rendering', size => {
  applySize(size)
  document.documentElement.removeAttribute('data-size')
  bootSize()
  expect(currentSize()).toBe(size)
})

it.each(['small', 'medium', 'large'] as const)('uses the %s URL hint for this load without changing the saved choice', size => {
  localStorage.setItem('apptown-size', 'small')
  history.replaceState(null, '', `/?size=${size}`)
  bootSize()
  expect(currentSize()).toBe(size)
  expect(localStorage.getItem('apptown-size')).toBe('small')
  history.replaceState(null, '', '/')
  bootSize()
  expect(currentSize()).toBe('small')
})

it('ignores an invalid hint and keeps the stored size', () => {
  localStorage.setItem('apptown-size', 'large')
  history.replaceState(null, '', '/?size=huge')
  bootSize()
  expect(currentSize()).toBe('large')
})

it('boots and switches when browser storage is unavailable', () => {
  vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
  vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked') })
  bootSize()
  expect(currentSize()).toBe('medium')
  applySize('large')
  expect(currentSize()).toBe('large')
})

it('shows the applied URL choice and saves each button choice for the next boot', async () => {
  history.replaceState(null, '', '/?size=large')
  bootSize()
  const el = document.createElement('div')
  const root = createRoot(el)
  try {
    await act(async () => root.render(<SizeSwitch />))
    const buttons = [...el.querySelectorAll('button')]
    expect(buttons.map(button => button.textContent)).toEqual(['Small', 'Medium', 'Large'])
    expect(buttons.map(button => button.getAttribute('aria-pressed'))).toEqual(['false', 'false', 'true'])
    for (const [index, size] of (['small', 'medium', 'large'] as const).entries()) {
      await act(async () => buttons[index].click())
      expect(currentSize()).toBe(size)
      expect(buttons.map(button => button.getAttribute('aria-pressed'))).toEqual(buttons.map((_, i) => String(i === index)))
      history.replaceState(null, '', '/')
      document.documentElement.removeAttribute('data-size')
      bootSize()
      expect(currentSize()).toBe(size)
    }
  } finally { act(() => root.unmount()) }
})
