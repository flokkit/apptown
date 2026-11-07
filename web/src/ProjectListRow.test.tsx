import { withFeatures } from './features.fixture'
// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from './data'
import { settled } from './settled'
import { ProjectListRow, mayOpenApp } from './ProjectListRow'
import type { ProjectRow } from './wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const cleanup: (() => void)[] = []
afterEach(() => { cleanup.splice(0).forEach(f => f()); vi.unstubAllGlobals() })
function Location() { const loc = useLocation(); return <output>{loc.pathname}{loc.search}</output> }
async function row(chatAvailable = true, post?: () => Promise<Response>) {
  const fetch = vi.fn(withFeatures(async (_url: string, init?: RequestInit) => init?.method === 'POST'
    ? post ? post() : new Response(JSON.stringify({ change: { id: 'chat-one' } }))
    : new Response('[]')))
  vi.stubGlobal('fetch', fetch)
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  document.body.append(el)
  cleanup.push(() => { act(() => root.unmount()); el.remove(); client.clear() })
  const project = { slug: 'library', name: 'Library', canChange: true, chatAvailable, hasDeployments: false } as unknown as ProjectRow
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><ProjectListRow project={project} /><Location /></MemoryRouter></QueryClientProvider>))
  await settled(client)
  const click = async (selector: string) => {
    await act(async () => (el.querySelector(selector) as HTMLButtonElement).click())
    await settled(client)
  }
  return { el, fetch, click, main: () => el.querySelector('.project-action button') as HTMLButtonElement }
}

it('starts an ordinary chat change and navigates straight to its conversation', async () => {
  const h = await row()
  expect(h.el.textContent).not.toContain('describe what you want changed')
  await h.click('.project-action button')
  expect(h.fetch).toHaveBeenCalledWith('/api/v1/projects/library/changes', expect.objectContaining({
    method: 'POST', body: expect.any(String),
  }))
  const init = h.fetch.mock.calls.find(([, init]) => init?.method === 'POST')![1]!
  expect(JSON.parse(init.body as string)).toEqual({ title: expect.stringMatching(/^Chat change /), titleFromChat: true, type: 'feature', body: '', workflow: '' })
  expect(h.el.querySelector('output')?.textContent).toBe('/projects/library/console?change=chat-one')
  expect(h.el.querySelector('[role=dialog]')).toBeNull()
})

it('offers catalog and detailed paths, and opens the detailed form without creating a change', async () => {
  const h = await row()
  await h.click('[aria-label="Change: more options"]')
  const items = [...document.querySelectorAll('.popover .split-menu > button')]
  expect(items.map(b => b.firstChild?.textContent)).toEqual(['Catalog change', 'New change, detailed'])
  await act(async () => (items[1] as HTMLButtonElement).click())
  expect(h.el.querySelector('[role=dialog] textarea')).not.toBeNull()
  await h.click('[role=dialog] button[aria-label="close"]')
  await h.click('[aria-label="Change: more options"]')
  await act(async () => (document.querySelector('.popover .split-menu > button') as HTMLButtonElement).click())
  expect(h.el.querySelector('[role=dialog]')?.textContent).toContain('Catalog change')
  expect(h.fetch.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false)
})

it('opens the detailed form directly when chat is unavailable and keeps Catalog change in the menu', async () => {
  const h = await row(false)
  await h.click('[aria-label="Change: more options"]')
  expect([...document.querySelectorAll('.popover .split-menu > button')].map(b => b.firstChild?.textContent)).toEqual(['Catalog change'])
  await h.click('.project-action button')
  expect(h.el.querySelector('[role=dialog]')?.textContent).toContain('New change')
  expect(h.el.querySelector('[role=dialog] textarea')).not.toBeNull()
  expect(h.fetch.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false)
})

it('shows busy while creating, prevents duplicate creation and shows failure beside the row', async () => {
  let finish!: (r: Response) => void
  const pending = new Promise<Response>(resolve => { finish = resolve })
  const h = await row(true, () => pending)
  await h.click('.project-action button')
  expect(h.main().textContent).toBe('…')
  expect(h.main().disabled).toBe(true)
  await h.click('.project-action button')
  expect(h.fetch.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1)
  await act(async () => finish(new Response(JSON.stringify({ error: 'Cannot create change' }), { status: 409 })))
  await act(async () => { await new Promise(r => setTimeout(r, 0)) })
  expect(h.el.querySelector('.project-action .error')?.textContent).toBe('Cannot create change')
  expect(h.main().disabled).toBe(false)
  expect(h.main().textContent).toBe('Change')
  expect(h.el.querySelector('output')?.textContent).toBe('/')
})

it('offers Open app only for the server known-running answer', () => {
 for (const prodRunning of [true, false, null, undefined]) {
 const project = {prodRunning,observation:'running',stale:prodRunning == null} as unknown as ProjectRow
 expect(mayOpenApp(project)).toBe(prodRunning === true)
 expect(mayOpenApp(project,true)).toBe(false)
 }
})
