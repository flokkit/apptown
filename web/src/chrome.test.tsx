// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { useChatViewport } from './chrome'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

afterEach(() => vi.unstubAllGlobals())

function Chat({ narrow }: { narrow: boolean }) {
  useChatViewport(narrow)
  return null
}

function fakeViewport() {
  const viewport = Object.assign(new EventTarget(), { height: 800, offsetTop: 0 })
  vi.stubGlobal('visualViewport', viewport)
  return viewport
}

it('tracks the visible chat height and focus panning on resize and scroll', () => {
  const viewport = fakeViewport()
  const root = createRoot(document.createElement('div'))
  const style = document.documentElement.style
  try {
    act(() => root.render(<Chat narrow />))
    expect(style.getPropertyValue('--viewport-h')).toBe('800px')
    viewport.height = 420
    act(() => viewport.dispatchEvent(new Event('resize')))
    expect(style.getPropertyValue('--viewport-h')).toBe('420px')
    viewport.height = 400
    viewport.offsetTop = 24
    act(() => viewport.dispatchEvent(new Event('scroll')))
    expect(style.getPropertyValue('--viewport-h')).toBe('400px')
    expect(style.getPropertyValue('--viewport-top')).toBe('24px')
  } finally { act(() => root.unmount()) }
})

it('releases both viewport listeners and values on desktop and when leaving chat', () => {
  const viewport = fakeViewport()
  const add = vi.spyOn(viewport, 'addEventListener')
  const remove = vi.spyOn(viewport, 'removeEventListener')
  const root = createRoot(document.createElement('div'))
  const style = document.documentElement.style
  const expectReleased = () => {
    expect(remove.mock.calls).toEqual(add.mock.calls)
    viewport.dispatchEvent(new Event('resize'))
    viewport.dispatchEvent(new Event('scroll'))
    expect(style.getPropertyValue('--viewport-h')).toBe('')
    expect(style.getPropertyValue('--viewport-top')).toBe('')
  }
  try {
    act(() => root.render(<Chat narrow={false} />))
    expect(add).not.toHaveBeenCalled()
    expect(style.getPropertyValue('--viewport-h')).toBe('')
    act(() => root.render(<Chat narrow />))
    expect(add.mock.calls.map(([event]) => event)).toEqual(['resize', 'scroll'])
    act(() => root.render(<Chat narrow={false} />))
    expectReleased()
    act(() => root.render(<Chat narrow />))
  } finally { act(() => root.unmount()) }
  expectReleased()
})

it('leaves the CSS viewport default in place when visualViewport is absent', () => {
  vi.stubGlobal('visualViewport', undefined)
  const root = createRoot(document.createElement('div'))
  try {
    act(() => root.render(<Chat narrow />))
    expect(document.documentElement.style.getPropertyValue('--viewport-h')).toBe('')
    expect(document.documentElement.style.getPropertyValue('--viewport-top')).toBe('')
  } finally { act(() => root.unmount()) }
})
