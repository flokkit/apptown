// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act, useRef } from 'react'
import { createRoot } from 'react-dom/client'
import { useTerminal } from './useTerminal'

// The terminal's socket belongs to a session: a page that renders again with
// the same session keeps its connection; another session gets a new one.

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 80; rows = 24
    loadAddon() {} open() {} focus() {} reset() {} write() {} dispose() {}
    onData() { return { dispose() {} } }
    onResize() { return { dispose() {} } }
  },
}))
vi.mock('@xterm/addon-fit', () => ({ FitAddon: class { fit() {} } }))
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
