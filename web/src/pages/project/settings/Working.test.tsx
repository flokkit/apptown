// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../../../data'
import { GuidanceTab } from './Working'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const block = (name: string, position: number, summary?: string) => ({ position, blockId: `00000000-${String(position).padStart(4, '0')}-7000-8000-000000000000`, name, version: 1, latestVersion: 1, disabled: false, bytes: 10, summary })
const guidanceAt = (revision: number, ...imports: ReturnType<typeof block>[]) => ({ revision, imports, generated: [], effective: { hash: 'abcdef0123456789', markdown: '', bytes: 0 } })
const guidance = (...imports: ReturnType<typeof block>[]) => guidanceAt(4, ...imports)
const path = '/projects/demo/guidance'

// Everything the tab reads besides the project's guidance, in the shapes the
// server sends when a project has adopted nothing.
function otherReads(url: string): Response {
  if (url.endsWith('/manifest')) return new Response(JSON.stringify({ repo: { backed: false }, packs: [], workflows: [], guidance: [], guidanceHash: '', policies: [], bindings: [] }))
  if (url.endsWith('/adoption')) return new Response(JSON.stringify({ packs: [], additions: [], exclusions: [], conditions: [] }))
  if (url.endsWith('/guidance/blocks')) return new Response(JSON.stringify([{ id: '00000000-0003-7000-8000-000000000000', name: 'plain-words', tags: '', latestVersion: 1, origin: 'stock', createdAt: '', updatedAt: '', bytes: 10 }]))
  if (url.endsWith('/lanes')) return new Response(JSON.stringify({ lanes: [], system: [] }))
  if (url.endsWith('/policies')) return new Response(JSON.stringify({ policies: [] }))
  return new Response('{}')
}

function mount(client: ReturnType<typeof newQueryClient>) {
  const el = document.createElement('div')
  const root = createRoot(el)
  act(() => { root.render(<QueryClientProvider client={client}><MemoryRouter><GuidanceTab slug="demo" /></MemoryRouter></QueryClientProvider>) })
  return { el, unmount: () => act(() => root.unmount()) }
}


describe("a project's way of working", () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // The packs are the one choice; what they give the project is read-only,
  // one line each, opening to the list.
  it('shows its packs and what they give it, with no editing', async () => {
    const client = newQueryClient()
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.endsWith('/adoption')) return new Response(JSON.stringify({ current: { createdAt: '2026-11-01T00:00:00Z', createdBy: 'sam', reason: 'created', digest: 'd' }, packs: [{ pack: 'django', version: 1, recommended: 1, reason: '' }, { pack: 'postgres', version: 1, recommended: 1, reason: '' }], additions: [], exclusions: [], conditions: [] }))
      if (url.endsWith(path)) return new Response(JSON.stringify(guidance(block('house-style', 1), block('prose', 2))))
      if (url.endsWith('/lanes')) return new Response(JSON.stringify({ lanes: [{ name: 'quick-change', tier: 'core', description: 'small', origin: 'stock', version: 1, selected: true, runsCount: 2 }, { name: 'research', tier: 'extra', description: 'look', origin: 'stock', version: 1, selected: false, runsCount: 0 }], system: [] }))
      return otherReads(url)
    }))
    const { el, unmount } = mount(client)
    try {
      await vi.waitFor(() => expect(el.textContent).toContain('This project uses: django · postgres'))
      expect(el.textContent).toContain('2 instructions its coding helpers follow')
      // Every workflow the project can run, not only the ones once ticked.
      expect(el.textContent).toContain('2 workflows')
      expect(el.querySelector('input[type=checkbox]')).toBeNull()
      expect([...el.querySelectorAll('button')].some((b) => /Save|Add|remove/.test(b.textContent ?? ''))).toBe(false)
      expect(el.querySelector('a[href="/guidance/house-style"]')).not.toBeNull()
    } finally {
      unmount()
      client.clear()
    }
  })

  // The words under a block's name are its pinned version's plain summary,
  // as the server sends it; the name stays the row's identity and link.
  it("shows each guidance block's summary under its name", async () => {
    const client = newQueryClient()
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.endsWith(path)) return new Response(JSON.stringify(guidance(block('house-style', 1, 'Write short functions.'))))
      return otherReads(url)
    }))
    const { el, unmount } = mount(client)
    try {
      await vi.waitFor(() => expect(el.querySelector('a[href="/guidance/house-style"]')).not.toBeNull())
      const item = el.querySelector('a[href="/guidance/house-style"]')!.closest('li')!
      expect(item.textContent).toContain('house-style')
      expect(item.textContent).toContain('Write short functions.')
    } finally {
      unmount()
      client.clear()
    }
  })
})

it('keeps an unreadable manifest distinct from an empty setup', async () => {
  const { SetupFacts } = await import('./SetupFacts')
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'Setup is unavailable' }), { status: 403 })))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><SetupFacts slug="demo" /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.textContent).toContain('Not available to you: project setup.'))
    expect(el.textContent).not.toContain('No packs')
    expect(el.querySelector('dl')).toBeNull()
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

it('lists current adopted packs and moved workflow and policy facts once', async () => {
  const client = newQueryClient()
  // Creation applied two packs in one audit subject. A later adoption replaced
  // one: membership comes from adoption, never that historical subject.
  const packs = ['frontend-only', 'dependency-updates', 'apptown']
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.endsWith('/manifest')) return Response.json({ repo: { backed: true, defaultBranch: 'main', remote: 'https://example.com/app.git' }, packs: [{ name: 'frontend-only, old-pack', at: '2026-10-01' }], workflows: [{ name: 'implement', version: 2, origin: 'stock' }], policies: [{ name: 'Review required' }], guidance: [{}], guidanceHash: 'compiled-hash', bindings: [{ capability: 'model', binding: 'helper', version: 3, configHash: 'config-hash' }] })
    if (url.endsWith('/adoption')) return Response.json({ current: { revision: 2, createdAt: '2026-11-01T00:00:00Z', createdBy: 'sam', reason: 'Monthly maintenance adopted', digest: 'digest' }, packs: packs.map(pack => ({ pack, version: 1, recommended: 1, platform: pack === 'apptown', reason: '' })), additions: [], exclusions: [], conditions: [] })
    if (url.endsWith('/lanes')) return Response.json({ lanes: [{ name: 'implement', description: 'Build the requested change', tier: 'core', version: 2, origin: 'stock' }, { name: 'legacy-build', description: 'Build with the retained workflow', tier: 'extra', version: 7, origin: 'pack:frontend-only', withdrawn: true }], system: [{ name: 'maintenance', description: 'Maintain the project', tier: 'system', version: 4, origin: 'custom' }] })
    if (url.endsWith('/policies')) return Response.json({ policies: [{ id: 'review', name: 'Review required', description: 'A person accepts', origin: 'stock' }, { id: 'limit', name: 'Limit size', rule: 'changes < 100', origin: 'custom' }], violations: [] })
    if (url.endsWith(path)) return Response.json(guidance())
    return otherReads(url)
  }))
  const { el, unmount } = mount(client)
  try {
    await vi.waitFor(() => expect(el.textContent).toContain('compiled-hash'))
    await vi.waitFor(() => expect(el.textContent).toContain('maintenance'))
    const setup = el.querySelector('section[aria-label="Project setup"]')!
    expect([...setup.querySelectorAll('dt')].map(e => e.textContent)).toEqual(['Repository', 'Guidance', 'Bindings'])
    for (const fact of ['main', 'https://example.com/app.git', '1 blocks', 'compiled-hash', 'model → helper v3', 'config-hash']) expect(setup.textContent).toContain(fact)
    expect(setup.querySelector('a[href="/integrations"]')).not.toBeNull()
    const section = el.querySelector('section[aria-label="Packs"]')!
    expect([...section.querySelectorAll('a')].map(e => [e.textContent, e.getAttribute('href')])).toEqual(packs.map(p => [p, `/packs/${p}`]))
    expect(section.textContent).toContain('platform')
    expect(el.textContent).not.toContain('old-pack')
    const inventory = el.querySelector('section[aria-label="What that gives it"]')!
    expect(inventory.textContent).toContain('3 workflows')
    for (const [name, facts] of [['implement', ['Build the requested change', 'v2', 'stock']], ['maintenance', ['Maintain the project', 'v4', 'custom', 'system']], ['legacy-build', ['Build with the retained workflow', 'v7', 'pack:frontend-only', 'withdrawn']]] as const) {
      const links = inventory.querySelectorAll(`a[href="/workflows?open=${name}"]`)
      expect(links).toHaveLength(1)
      for (const fact of facts) expect(links[0].closest('li')?.textContent).toContain(fact)
      expect(inventory.textContent?.split(name)).toHaveLength(2)
    }
    for (const [name, description, origin] of [['Review required', 'A person accepts', 'stock'], ['Limit size', 'changes < 100', 'custom']]) {
      const items = [...inventory.querySelectorAll('li')].filter(e => e.textContent?.includes(name))
      expect(items).toHaveLength(1)
      expect(items[0].textContent).toContain(description)
      expect(items[0].textContent).toContain(origin)
    }
  } finally { unmount(); client.clear(); vi.unstubAllGlobals() }
})

// A successful absence of adoption is a known choice; a failed read is unknown.
it.each([false, true])('distinguishes a bare project from an adoption read failure (failed: %s)', async failed => {
  const client = newQueryClient()
  client.setDefaultOptions({ queries: { retry: false } })
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.endsWith('/adoption')) return failed
      ? Response.json({ error: 'Adoption unavailable' }, { status: 500 })
      : Response.json({ current: null, packs: [], additions: [], exclusions: [], conditions: [] })
    if (url.endsWith(path)) return Response.json(guidance())
    return otherReads(url)
  }))
  const { el, unmount } = mount(client)
  try {
    const section = el.querySelector('section[aria-label="Packs"]')!
    if (failed) {
      await vi.waitFor(() => expect(section.textContent).toContain('unknown'))
      expect(section.querySelector('[aria-label*="Adoption unavailable"]')).not.toBeNull()
      expect(section.textContent).not.toContain('No packs')
    } else {
      await vi.waitFor(() => expect(section.textContent).toContain('No packs'))
      expect(el.textContent?.match(/No packs/g)).toHaveLength(1)
      expect(section.textContent).not.toContain('unknown')
    }
  } finally { unmount(); client.clear(); vi.unstubAllGlobals() }
})
