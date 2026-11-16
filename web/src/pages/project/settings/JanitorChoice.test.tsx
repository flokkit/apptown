// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { newQueryClient } from '../../../data'
import { settled } from '../../../settled'
import { JanitorChoice } from './JanitorChoice'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const cleanups: (() => void)[] = []
afterEach(() => { cleanups.splice(0).forEach(f => f()); vi.unstubAllGlobals() })
async function mount({ manage = true, instance = 'aggressive', choice = '', failed = false } = {}) {
  const sent: unknown[] = []
  const fetcher = vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') { sent.push(JSON.parse(String(init.body))); return new Response('{"ok":true}') }
    if (url.includes('/authz/me')) return new Response(JSON.stringify({ project: { 'project:manage-standing-approval': manage } }))
    if (failed) return new Response('{"error":"Cannot read choices"}', { status: 400 })
    return new Response(JSON.stringify({ queues: [], standing: { enabled: true, instanceLevel: instance, level: instance === 'off' ? 'off' : choice || instance, ...(choice ? { choice: { level: choice, person: { name: 'Sam' }, at: '2026-11-12T00:00:00Z' } } : {}) } }))
  })
  vi.stubGlobal('fetch', fetcher)
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  cleanups.push(() => { act(() => root.unmount()); client.clear() })
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><JanitorChoice slug="garden" /></MemoryRouter></QueryClientProvider>))
  await settled(client)
  return { el, sent, fetcher }
}
it('saves a level once on change and offers inheritance and Run now without Save', async () => {
  const { el, sent } = await mount()
  const select = el.querySelector('select')!
  expect(select.value).toBe('instance')
  expect(select.options[0].text).toBe('Same as everywhere (Including major)')
  expect([...el.querySelectorAll('button')].map(b => b.textContent)).toContain('Run now')
  expect(el.textContent).not.toContain('Save')
  await act(async () => { select.value = 'off'; select.dispatchEvent(new Event('change', { bubbles: true })) })
  expect(sent).toEqual([{ level: 'off' }])
})
it('disables the choice without permission', async () => {
  const { el } = await mount({ manage: false })
  expect(el.querySelector('select')!.disabled).toBe(true)
})
it('keeps a pinned choice visible and editable when dependencies are off everywhere', async () => {
  const { el } = await mount({ instance: 'off', choice: 'routine' })
  expect(el.querySelector('select')!.value).toBe('routine')
  expect(el.querySelector('select')!.disabled).toBe(false)
  expect(el.querySelector('a[href="/janitor/settings"]')?.textContent).toBe('Dependency updates are off everywhere')
  expect(el.textContent).toContain('Sam, ')
})
it('shows a failed read instead of an unread select', async () => {
  const { el } = await mount({ failed: true })
  expect(el.querySelector('select')).toBeNull()
  expect(el.textContent).toContain('unknown')
  await act(async () => el.querySelector<HTMLButtonElement>('.chip-button')!.click())
  expect(document.body.textContent || el.textContent).toContain('Cannot read choices')
})
