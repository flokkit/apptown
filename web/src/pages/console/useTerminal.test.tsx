// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act, useRef } from 'react'
import { createRoot } from 'react-dom/client'
import { useTerminal } from './useTerminal'

const terminal = vi.hoisted(() => ({ sizes: [] as number[], options: { fontSize: 0 }, fit: vi.fn(), focus: vi.fn(), type: (_: string) => {} }))

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
    loadAddon() {} open() {} focus = terminal.focus; reset() {} write() {} dispose() {}
    onData(f: (d: string) => void) { terminal.type = f; return { dispose() {} } }
    onResize() { return { dispose() {} } }
  },
}))
vi.mock('@xterm/addon-fit', () => ({ FitAddon: class { fit = terminal.fit } }))
vi.mock('@xterm/xterm/css/xterm.css', () => ({}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

afterEach(() => vi.unstubAllGlobals())

it('opens one socket per session, not one per render', async () => {
  terminal.focus.mockClear()
  const opened: string[] = []
  vi.stubGlobal('WebSocket', class {
    static OPEN = 1
    readyState = 0
    onopen?: () => void; onmessage?: () => void; onclose?: () => void
    constructor(url: string) { opened.push(url) }
    send() {} close() {}
  })
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  function T({ id, mayAct, n, visible = false }: { id: string; mayAct: boolean; n: number; visible?: boolean }) {
    const ref = useRef<HTMLDivElement>(null)
    const status = useTerminal(id, mayAct, ref, visible)
    return <div ref={ref} data-n={n}>{status}</div>
  }
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => { root.render(<T id="s1" mayAct n={1} />) })
  await act(async () => { root.render(<T id="s1" mayAct n={2} />) })
  expect(opened).toHaveLength(1)
  expect(terminal.focus).not.toHaveBeenCalled() // attaching must leave a draft focused
  await act(async () => { root.render(<T id="s1" mayAct n={2} visible />) })
  expect(terminal.focus).toHaveBeenCalledTimes(1)
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

it('never reports the previous session as attached while rendering a new session', async () => {
  let attach!: () => void
  vi.stubGlobal('WebSocket', class {
    onopen?: () => void
    constructor() { attach = () => this.onopen?.() }
    send() {} close() {}
  })
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  const seen: string[] = []
  function T({ id }: { id: string }) {
    const ref = useRef<HTMLDivElement>(null)
    const status = useTerminal(id, true, ref)
    seen.push(`${id}:${status}`)
    return <div ref={ref} />
  }
  const root = createRoot(document.createElement('div'))
  try {
    await act(async () => root.render(<T id="s1" />))
    await act(async () => attach())
    expect(seen).toContain('s1:attached')
    await act(async () => root.render(<T id="s2" />))
    expect(seen).not.toContain('s2:attached')
    await act(async () => attach())
    expect(seen).toContain('s2:attached')
  } finally { act(() => root.unmount()) }
})

// The terminal only shows until its person takes control: what they type
// before that is never sent (the server refuses it too), so Escape pressed
// to look around cannot stop the helper.
it('sends keystrokes only after taking control', async () => {
  const opened: string[] = []
  const sent: unknown[] = []
  vi.stubGlobal('WebSocket', class {
    static OPEN = 1
    readyState = 1
    onopen?: () => void
    constructor(url: string) { opened.push(url) }
    send(d: unknown) { if (typeof d !== 'string') sent.push(d) }
    close() {}
  })
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  function T({ control }: { control: boolean }) {
    const ref = useRef<HTMLDivElement>(null)
    useTerminal('s1', true, ref, true, control)
    return <div ref={ref} />
  }
  const root = createRoot(document.createElement('div'))
  try {
    await act(async () => root.render(<T control={false} />))
    terminal.type('\x1b')
    expect(sent).toHaveLength(0)
    expect(opened[0]).not.toContain('control')
    await act(async () => root.render(<T control />))
    expect(opened).toHaveLength(2)
    expect(opened[1]).toContain('/pty?control=1')
    terminal.type('\x1b')
    expect(sent).toHaveLength(1)
  } finally { act(() => root.unmount()) }
})
