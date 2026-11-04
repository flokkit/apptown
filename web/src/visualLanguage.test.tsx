// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { newQueryClient } from './data'
import { settled } from './settled'
import { ProjectListRow } from './ProjectListRow'
import { SidebarNav } from './App'
import Packs from './pages/Packs'
import WorkflowLibrary from './pages/WorkflowLibrary'
import { Tabs } from './Tabs'
import type { ReactNode } from 'react'
import type { ProjectRow } from './wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const cleanup: (() => void)[] = []
afterEach(() => { cleanup.splice(0).forEach(f => f()); vi.unstubAllGlobals(); vi.restoreAllMocks() })
async function mount(node: ReactNode, path = '/projects/app') {
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  client.setDefaultOptions({ queries: { retry: false } })
  cleanup.push(() => { act(() => root.unmount()); client.clear() })
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}>{node}</MemoryRouter></QueryClientProvider>))
  await settled(client)
  return el
}

it('offers the ordinary change form only with project creation authority and a current reading', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response('[]')))
  const p = { slug: 'app', name: 'Library', hasDeployments: false, canChange: true } as ProjectRow
  const el = await mount(<><ProjectListRow project={p} /><ProjectListRow project={{ ...p, canChange: false }} /><ProjectListRow project={p} refreshFailed /></>)
  const buttons = [...el.querySelectorAll<HTMLButtonElement>('.project-action button')]
  expect(buttons.map(b => b.textContent)).toEqual(['Change', '▾'])
  await act(async () => buttons[0].click())
  expect(el.querySelector('[role=dialog]')?.textContent).toContain('New change')
  expect(el.querySelector('[role=dialog] textarea')).not.toBeNull()
})

it.each([false, true])('omits unavailable project destinations (Chat ready: %s)', async ready => {
  const calls: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    calls.push(`${init?.method || 'GET'} ${url}`)
    return new Response(JSON.stringify(url.includes('/authz/me') ? { instance: {}, project: { 'change:create': true, 'change:invoke-lane': true } }
      : init?.method === 'POST' ? { change: { id: 'change-one' } } : { name: 'Library', chatAvailable: ready }))
  }))
  const el = await mount(<SidebarNav />)
  expect(el.textContent).not.toContain('Janitor')
  expect(el.querySelector('a[href$="/janitor"]')).toBeNull()
  const chat = [...el.querySelectorAll('button')].find(b => b.textContent === 'Chat')
  expect(!!chat).toBe(ready)
  if (chat) { await act(async () => chat.click()); expect(calls).toContain('POST /api/v1/projects/app/changes') }
  expect(el.querySelector('a[href$="/console"]')).toBeNull()
})

it('leads pack rows with the name and one purpose line, and says nothing about the normal case', async () => {
  const description = 'Shared rules for teams with an existing app. A longer explanation follows.'
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify([{ name: 'conventions-only', description, origin: 'stock', version: 2, requires: [], declaration: { guidance: [], workflows: [], policies: [] } }]))))
  const el = await mount(<Packs />, '/packs')
  expect(el.querySelector('.list-identity')?.firstElementChild?.textContent).toBe('conventions-only')
  expect(el.querySelector('.list-identity')?.textContent).toContain('Shared rules for teams with an existing app.')
  expect(el.querySelector('.list-identity')?.textContent).not.toContain('A longer explanation')
  expect(el.querySelector('.list-state')?.textContent).toBe('')
  expect(el.querySelector('details')).toBeNull()
  expect(el.querySelector('button[aria-label="More actions for conventions-only"]')).not.toBeNull()
})

it('marks both overflowing tab edges as the person scrolls', async () => {
  vi.spyOn(HTMLElement.prototype, 'scrollWidth', 'get').mockReturnValue(700)
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(300)
  const el = await mount(<Tabs><button>One</button><button>Two</button></Tabs>)
  expect(el.querySelector('.tab-strip.more-tabs')).not.toBeNull()
  const strip = el.querySelector('.tabs')!
  await act(async () => { strip.scrollLeft = 200; strip.dispatchEvent(new Event('scroll')) })
  expect(el.querySelector('.tab-strip.previous-tabs.more-tabs')).not.toBeNull()
  await act(async () => { strip.scrollLeft = 400; strip.dispatchEvent(new Event('scroll')) })
  expect(el.querySelector('.tab-strip.previous-tabs:not(.more-tabs)')).not.toBeNull()
})


it('keeps workflow purpose and suitability visible before maintenance', async () => {
  const description = 'Review an ordinary change. For teams that approve their work.'
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.includes('system-workflows') ? { bindings: [], candidates: [] } : [{ name: 'stock/review', description, tier: 'core', latestVersion: 2, usedBy: [], stages: [], yaml: 'name: stock/review', origin: 'stock' }]))))
  const el = await mount(<WorkflowLibrary />, '/workflows')
  // The name leads, with its purpose under it; the rest of the description
  // and the copy action wait until the row is opened or its ⋯ menu.
  expect(el.querySelector('.list-identity')?.textContent).toContain('stock/review')
  expect(el.querySelector('.list-identity')?.textContent).toContain('Review an ordinary change.')
  expect(el.textContent).not.toContain('For teams that approve their work.')
  await act(async () => (el.querySelector('.list-row-open') as HTMLElement).click())
  expect(el.textContent).toContain(description)
  await act(async () => (el.querySelector('.more-menu-button') as HTMLElement).click())
  expect([...el.querySelectorAll('[role=menuitem]')].map((b) => b.firstChild?.textContent)).toContain('Copy')
})
