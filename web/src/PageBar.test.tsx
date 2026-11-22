// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, expect, it, vi } from 'vitest'
// @ts-expect-error -- this package has no Node types; only these reads are used.
import { readdirSync, readFileSync } from 'node:fs'
import App from './App'
import { newQueryClient } from './data'
import { withFeatures } from './features.fixture'
import { settled } from './settled'
import { stubViewportWidth } from './viewport.fixture'

// The one bar (design/subsystems/ui-coherence.md): the page's own header row
// is the only bar. The shell owns its menu button, search and attention
// lights; the page on screen fills it with its title and actions.

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
afterEach(() => { vi.unstubAllGlobals(); document.body.innerHTML = '' })

// pending: paths whose answer never arrives, so their page stays loading.
function serve(pending: string[] = []) {
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string) => {
    const path = String(url).replace('/api/v1', '')
    if (pending.includes(path)) return new Promise<Response>(() => {})
    if (path === '/auth/me') return new Response(JSON.stringify({ mode: 'internal', user: 'alice', role: 'admin' }))
    if (path === '/authz/me') return new Response(JSON.stringify({ instance: { 'platform:manage-deploy-targets': true } }))
    if (path === '/attention') return new Response(JSON.stringify({ items: [], sources: [] }))
    if (path === '/projects/p') return new Response(JSON.stringify({ slug: 'p', name: 'Pantry' }))
    return new Response('{}')
  })))
}

async function open(path: string, width: number, pending: string[] = []) {
  serve(pending)
  stubViewportWidth(width)
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  document.body.append(el)
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><App /></MemoryRouter></QueryClientProvider>))
  if (!pending.length) await settled(client)
  return { el, done: () => { act(() => root.unmount()); client.clear() } }
}

const bar = (el: HTMLElement) => el.querySelector<HTMLElement>('header.page-bar')!
const menuButton = (el: HTMLElement) => bar(el).querySelector<HTMLButtonElement>('.page-bar-lead button[aria-label="open the menu"]')

it.each([390, 1000, 1440])('draws one bar, the page heading with search and the attention lights, and no shell bar (%spx)', async width => {
  const v = await open('/attention', width)
  try {
    await vi.waitFor(() => expect(bar(v.el).querySelector('h1')?.textContent).toBe('Attention'))
    expect(v.el.querySelector('.topbar')).toBeNull()
    expect(v.el.querySelectorAll('header')).toHaveLength(1)
    expect(v.el.querySelectorAll('h1')).toHaveLength(1)
    expect(bar(v.el).querySelector('.page-bar-trail button[aria-label="Search"]')).not.toBeNull()
    expect(bar(v.el).querySelector('.page-bar-trail a[href^="/attention"]')).not.toBeNull()
    // The wordmark is Home, at the top of the nav.
    expect(v.el.querySelector('#sidebar .sidebar-head a.wordmark')?.getAttribute('href')).toBe('/')
  } finally { v.done() }
})

it.each([[1000, true], [1100, true], [1101, false], [1440, false]])('puts the menu button first in the bar when the nav is folded (%spx: %s)', async (width, folded) => {
  const v = await open('/attention', width)
  try {
    await vi.waitFor(() => expect(bar(v.el).querySelector('h1')).not.toBeNull())
    expect(!!menuButton(v.el)).toBe(folded)
    if (folded) expect(bar(v.el).querySelector('button')).toBe(menuButton(v.el))
    expect(v.el.querySelector('#sidebar')?.classList.contains('folded')).toBe(folded)
  } finally { v.done() }
})

it('keeps the menu and search in the bar while the page is still reading', async () => {
  const v = await open('/projects/p', 390, ['/projects/p'])
  try {
    await vi.waitFor(() => expect(menuButton(v.el)).not.toBeNull())
    expect(v.el.querySelector('h1')).toBeNull()
    expect(bar(v.el).querySelector('button[aria-label="Search"]')).not.toBeNull()
  } finally { v.done() }
})

it('opens search from the bar with ⌘K', async () => {
  const v = await open('/attention', 1440)
  try {
    const search = bar(v.el).querySelector<HTMLButtonElement>('button[aria-label="Search"]')!
    expect(search.getAttribute('aria-expanded')).toBe('false')
    act(() => { window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true, bubbles: true })) })
    expect(search.getAttribute('aria-expanded')).toBe('true')
    expect(document.activeElement?.getAttribute('aria-label')).toBe('find')
  } finally { v.done() }
})

it('offers the page above this one first in the folded menu', async () => {
  const v = await open('/maintenance/standards/reviews/r1', 1000)
  try {
    await vi.waitFor(() => expect(menuButton(v.el)).not.toBeNull())
    act(() => menuButton(v.el)!.click())
    expect(menuButton(v.el)!.getAttribute('aria-expanded')).toBe('true')
    expect(v.el.querySelector('#sidebar')?.classList.contains('open')).toBe(true)
    const links = [...v.el.querySelectorAll<HTMLAnchorElement>('#sidebar a:not([hidden])')].filter(a => !a.closest('.sidebar-head'))
    expect([links[0].getAttribute('href'), links[0].textContent]).toEqual(['/maintenance/standards/reviews', '‹Reviews'])
    // ✕ closes it again.
    act(() => v.el.querySelector<HTMLButtonElement>('#sidebar button[aria-label="close the menu"]')!.click())
    expect(v.el.querySelector('#sidebar')?.classList.contains('open')).toBe(false)
  } finally { v.done() }
})

it('does not repeat a parent the menu already links to', async () => {
  const v = await open('/projects/p/memory/notes', 1000)
  try {
    await vi.waitFor(() => expect(v.el.querySelector('#sidebar a.nav[href="/projects/p/memory"]')).not.toBeNull())
    expect(v.el.querySelector('#sidebar a.sidebar-parent')?.hasAttribute('hidden')).toBe(true)
  } finally { v.done() }
})

it('has no parent link on a wide screen, where the nav shows the project', async () => {
  const v = await open('/projects/p/memory/notes', 1440)
  try {
    await vi.waitFor(() => expect(v.el.querySelector('.sidebar-project strong')?.textContent).toBe('Pantry'))
    expect(v.el.querySelector('a.sidebar-parent')).toBeNull()
    // Projects moved from the old top bar into the project's menu.
    expect(v.el.querySelector('#sidebar a.nav[href="/projects"]')?.textContent).toBe('Projects')
  } finally { v.done() }
})

// A convention check: where headings are built, not which page fills the bar
// (the tab and dialog tests above and beside the pages cover that).
it('builds every h1 in PageBar', () => {
  const scan = (dir: string): string[] => readdirSync(dir, { withFileTypes: true }).flatMap((e: { name: string; isDirectory(): boolean }) =>
    e.isDirectory() ? scan(`${dir}/${e.name}`) : /\.tsx$/.test(e.name) && !e.name.includes('.test.') ? [`${dir}/${e.name}`] : [])
  const drawing = scan('src').filter(f => f !== 'src/PageBar.tsx' && /<h1[\s>]/.test(readFileSync(f, 'utf8')))
  expect(drawing).toEqual([])
})
