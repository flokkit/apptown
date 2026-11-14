// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import { EarlierChats } from './Conversation'
import { Bubbles } from '../console/Bubbles'
import { getKey, newQueryClient } from '../../data'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('identifies imported history even when it is the first and only earlier session', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/changes/X/transcript'), { sessions: [{ id: 'moved', imported: true, ended: true, createdAt: '2026-10-01T09:00:00Z', actor: 'sam', messages: [{ i: 0, role: 'helper', text: 'Earlier imported words' }] }], unreadable: 0 })
  try {
    const html = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter><EarlierChats changeId="X" currentOpenedAt="2026-10-02T09:00:00Z" /></MemoryRouter></QueryClientProvider>)
    expect(html).toContain('Earlier imported words')
    expect(html).toContain('moved from another AppTown, history only')
  } finally { client.clear() }
})

it.each([false, true])('shows failed transcript reads with retained history=%s', async (retained) => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'transcript unavailable' }), { status: 400 })))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  if (retained) client.setQueryData(getKey('/changes/X/transcript'), { sessions: [{ id: 'old', ended: true, imported: false, createdAt: '2026-10-01T09:00:00Z', actor: 'sam', messages: [{ i: 0, role: 'helper', text: 'Retained earlier words' }] }], unreadable: 0 })
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><EarlierChats changeId="X" /></MemoryRouter></QueryClientProvider>))
    await act(async () => { await client.refetchQueries({ queryKey: getKey('/changes/X/transcript') }) })
    await vi.waitFor(() => expect(el.querySelector('.chip.unknown')).not.toBeNull())
    if (retained) {
      expect(el.textContent).toContain('Retained earlier words')
      expect(el.querySelector('.stale-note')).not.toBeNull()
    }
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

it('follows asynchronous history resizing only while the reader follows the bottom', async () => {
  let resize: ResizeObserverCallback | undefined
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('ResizeObserver', class {
    constructor(callback: ResizeObserverCallback) { resize = callback }
    observe() {}
    disconnect() {}
  })
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><Bubbles sessionId={null} before={<div>Earlier words</div>} /></QueryClientProvider>))
    const box = el.querySelector<HTMLDivElement>('.chat-scroll')!
    Object.defineProperties(box, { scrollHeight: { configurable: true, value: 1000 }, clientHeight: { configurable: true, value: 200 } })
    expect(resize).toBeDefined()
    act(() => resize!([], {} as ResizeObserver))
    expect(box.scrollTop).toBe(1000)
    box.scrollTop = 100
    act(() => box.dispatchEvent(new Event('scroll')))
    act(() => resize!([], {} as ResizeObserver))
    expect(box.scrollTop).toBe(100)
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
