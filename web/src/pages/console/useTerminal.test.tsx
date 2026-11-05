// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act, useRef } from 'react'
import { createRoot } from 'react-dom/client'
import { useTerminal } from './useTerminal'

const terminal = vi.hoisted(() => ({ sizes: [] as number[], options: { fontSize: 0 }, fit: vi.fn() }))

// The terminal's socket belongs to a session: a page that renders again with
// the same session keeps its connection; another session gets a new one.

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 80; rows = 24
    options: { fontSize: number }
    constructor(options: { fontSize: number }) {
      terminal.sizes.push(options.fontSize)
      this.options = terminal.options = options
    }
    loadAddon() {} open() {} focus() {} reset() {} write() {} dispose() {}
    onData() { return { dispose() {} } }
    onResize() { return { dispose() {} } }
  },
}))
vi.mock('@xterm/addon-fit', () => ({ FitAddon: class { fit = terminal.fit } }))
vi.mock('@xterm/xterm/css/xterm.css', () => ({}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

afterEach(() => vi.unstubAllGlobals())

it('opens one socket per session, not one per render', async () => {
  const opened: string[] = []
  vi.stubGlobal('WebSocket', class {
    static OPEN = 1
    readyState = 0
    onopen?: () => void; onmessage?: () => void; onclose?: () => void
    constructor(url: string) { opened.push(url) }
    send() {} close() {}
  })
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  function T({ id, mayAct, n }: { id: string; mayAct: boolean; n: number }) {
    const ref = useRef<HTMLDivElement>(null)
    const status = useTerminal(id, mayAct, ref)
    return <div ref={ref} data-n={n}>{status}</div>
  }
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => { root.render(<T id="s1" mayAct n={1} />) })
  await act(async () => { root.render(<T id="s1" mayAct n={2} />) })
  expect(opened).toHaveLength(1)
  await act(async () => { root.render(<T id="s2" mayAct n={3} />) })
  expect(opened).toHaveLength(2)
  expect(opened[1]).toContain('/api/v1/console/s2/pty')
  act(() => root.unmount())
})


it('resolves the rem token through an element and refits on size changes without reconnecting', async () => {
  const opened: string[] = []
  vi.stubGlobal('WebSocket', class {
    static OPEN = 1
    readyState = 1
    constructor(url: string) { opened.push(url) }
    send() {} close() {}
  })
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  let pixels = 13
  const computed = vi.spyOn(window, 'getComputedStyle').mockImplementation(element => ({
    fontSize: (element as HTMLElement).style.fontSize === 'var(--fs-sm)' ? `${pixels}px` : '14px',
    getPropertyValue: (name: string) => name === '--fs-sm' ? '0.9285714286rem' : '',
  }) as CSSStyleDeclaration)
  function T() {
    const ref = useRef<HTMLDivElement>(null)
    useTerminal('s1', true, ref)
    return <div ref={ref} />
  }
  const el = document.createElement('div')
  const root = createRoot(el)
  terminal.sizes.length = 0
  terminal.fit.mockClear()
  try {
    await act(async () => root.render(<T />))
    expect(terminal.sizes).toEqual([13])
    const initialFits = terminal.fit.mock.calls.length
    for (const [size, font] of [['medium', 104 / 7], ['large', 117 / 7], ['small', 13]] as const) {
      pixels = font
      await act(async () => { document.documentElement.dataset.size = size })
      expect(terminal.options.fontSize).toBe(font)
    }
    expect(terminal.fit.mock.calls.length).toBe(initialFits + 3)
    expect(opened).toHaveLength(1)
    act(() => root.unmount())
    expect(el.querySelector('[hidden]')).toBeNull()
    const fits = terminal.fit.mock.calls.length
    await act(async () => { document.documentElement.dataset.size = 'large' })
    expect(terminal.fit.mock.calls.length).toBe(fits)
  } finally {
    computed.mockRestore()
    document.documentElement.removeAttribute('data-size')
  }
})
