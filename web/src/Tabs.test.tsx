// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { Tabs } from './Tabs'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('reveals the active item horizontally on mount and on selection without moving keyboard focus', () => {
  const box = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function(this: HTMLElement) {
    return { left: 0, right: this.matches('.active') ? 500 : 300 } as DOMRect
  })
  let resize = () => {}
  const observe = vi.fn(), disconnect = vi.fn()
  vi.stubGlobal('ResizeObserver', class {
    constructor(callback: () => void) { resize = callback }
    observe = observe
    disconnect = disconnect
  })
  const el = document.createElement('div'), root = createRoot(el)
  try {
    act(() => root.render(<Tabs aria-label="Sections"><a href="#one">One</a><a href="#two" className="active" aria-current="page">Two</a></Tabs>))
    expect(el.querySelector('.tabs')?.scrollLeft).toBe(200)
    expect(el.querySelector('a.active')?.getAttribute('aria-current')).toBe('page')
    const active = el.querySelector('a.active')
    const strip = el.querySelector('.tabs') as HTMLElement
    strip.scrollLeft = 75
    act(() => root.render(<Tabs aria-label="Sections"><a href="#one">One</a><a href="#two" className="active" aria-current="page">Two</a></Tabs>))
    expect(strip.scrollLeft).toBe(75)
    act(() => root.render(<Tabs><button className="active" role="tab" aria-selected="true">Other</button></Tabs>))
    expect(el.querySelector('.tabs')?.scrollLeft).toBe(275)
    expect(document.activeElement).not.toBe(active)
    strip.scrollLeft = 0
    act(() => resize())
    expect(strip.scrollLeft).toBe(200)
    expect(observe).toHaveBeenCalledTimes(1)
    expect(el.querySelector('[role=tab]')?.getAttribute('aria-selected')).toBe('true')
  } finally { act(() => root.unmount()); box.mockRestore(); vi.unstubAllGlobals() }
})
