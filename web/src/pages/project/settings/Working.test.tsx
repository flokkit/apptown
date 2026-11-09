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
      expect(el.textContent).toContain('2 instructions its coding agents follow')
      // Every workflow the project can run, not only the ones once ticked.
      expect(el.textContent).toContain('quick-change, research')
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
