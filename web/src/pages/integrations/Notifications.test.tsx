// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { newQueryClient } from '../../data'
import { Notifications } from './Notifications'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const roots: ReturnType<typeof createRoot>[] = []
afterEach(() => { for (const r of roots.splice(0)) act(() => r.unmount()); document.body.innerHTML = ''; vi.unstubAllGlobals() })
async function mount() {
  const el = document.createElement('div'); document.body.appendChild(el)
  const root = createRoot(el); roots.push(root)
  await act(async () => { root.render(<QueryClientProvider client={newQueryClient()}><MemoryRouter><Notifications /></MemoryRouter></QueryClientProvider>) })
  return el
}
const empty = { channels: [], deliveries: [] }
it('keeps loading, denied, failed and empty distinct', async () => {
  let answer: (r: Response) => void = () => {}
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => { answer = resolve })))
  // Catalog is independent, so only defer the notifications read.
  vi.stubGlobal('fetch', vi.fn((url: string) => url.endsWith('/catalog') ? Promise.resolve(new Response(JSON.stringify({ catalog: [] }))) : new Promise<Response>(resolve => { answer = resolve })))
  const el = await mount(); expect(el.textContent).toContain('Loading…'); expect(el.textContent).not.toContain('No notification channels')
  await act(async () => answer(new Response(JSON.stringify(empty))))
  await vi.waitFor(() => expect(el.textContent).toContain('No notification channels'))
  for (const status of [403, 500]) {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(new Response(JSON.stringify({ error: 'unavailable' }), { status }))))
    const page = await mount()
    await vi.waitFor(() => expect(page.textContent).toContain(status === 403 ? 'Not available to you' : 'unknown'), { timeout: 5000 })
    expect(page.textContent).not.toContain('No notification channels')
  }
})
it('sends a test and refreshes delivery history', async () => {
  const calls: string[] = []; let sent = false
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const path = url.replace(/^.*\/api\/v1/, ''); calls.push(`${init?.method ?? 'GET'} ${path}`)
    if (path.endsWith('/test')) { sent = true; return new Response(JSON.stringify({ state: 'sent' })) }
    if (path.endsWith('/catalog')) return new Response(JSON.stringify({ catalog: [] }))
    return new Response(JSON.stringify({ channels: [{ name: 'phone', label: 'Phone', kind: 'webhook', format: 'ntfy', digestHour: 0, state: sent ? 'sent' : '' }], deliveries: sent ? [{ id: 'one', channel: 'phone', kind: 'test', title: 'AppTown test notification', state: 'sent', attempts: 1, reason: '', createdAt: '2026-11-13T12:00:00Z' }] : [] }))
  }))
  const el = await mount(); await vi.waitFor(() => expect(el.textContent).toContain('Send test'))
  expect(el.textContent).toContain('00:00')
  await act(async () => { [...el.querySelectorAll('button')].find(b => b.textContent === 'Send test')!.click() })
  await vi.waitFor(() => expect(el.textContent).toContain('AppTown test notification'))
  // A delivery is an event: its time is a date, shown even when the server's
  // clock runs ahead of the reader's.
  expect(el.textContent).toContain('13 Nov')
  expect(calls).toContain('POST /notifications/channels/phone/test')
  expect(calls.filter(c => c === 'GET /notifications').length).toBeGreaterThan(1)
})
it('adds a channel through the existing form and refreshes channels', async () => {
  let saved = false
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') { saved = true; return new Response('{}') }
    if (url.endsWith('/catalog')) return new Response(JSON.stringify({ catalog: [{ capability: 'notifier', provider: 'webhook', title: 'Webhook', description: 'Attention notices', multi: true, managed: false, fields: [], presets: [{ name: 'phone', title: 'ntfy', icon: 'feedback', description: 'Phone notices', suggest: 'phone', config: { format: 'ntfy' } }] }] }))
    return new Response(JSON.stringify({ channels: saved ? [{ name: 'phone', label: 'Phone', kind: 'webhook', format: 'ntfy', digestHour: 8, state: '' }] : [], deliveries: [] }))
  }))
  const el = await mount()
  await vi.waitFor(() => expect(el.textContent).toContain('Add channel'))
  await act(async () => { [...el.querySelectorAll('button')].find(b => b.textContent === 'Add channel')!.click() })
  await vi.waitFor(() => expect(document.body.textContent).toContain('Phone notices'))
  await act(async () => { (document.querySelector('.cat-tile') as HTMLElement).click() })
  await vi.waitFor(() => expect(document.body.textContent).toContain('Save'))
  expect(document.body.textContent).not.toContain('Test connection')
  await act(async () => { [...document.querySelectorAll('button')].find(b => b.textContent === 'Save')!.click() })
  await vi.waitFor(() => expect(el.textContent).toContain('Send test'))
  expect(saved).toBe(true)
})
