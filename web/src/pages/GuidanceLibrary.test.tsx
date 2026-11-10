// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import GuidanceLibrary, { GeneratedGuidance } from './GuidanceLibrary'
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
afterEach(() => vi.unstubAllGlobals())

// A library row shows the block's plain summary, as the server makes it,
// under its name.
it("shows each block's summary under its name", async () => {
  const summary = 'Every part AppTown serves from your build or image declares its readiness in .apptown/app.yaml (a static: part declares nothing):'
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.includes('/guidance/blocks')) return new Response(JSON.stringify([{ id: '00000000-0001-7000-8000-000000000000', name: 'deploy/health-declared', summary, tags: 'deploy', latestVersion: 2, origin: 'stock', createdAt: '', updatedAt: '', bytes: 4000, tier: 'platform' },
      { id: '00000000-0002-7000-8000-000000000000', name: 'build/container-image', summary: 'The final stage of a build: . part runs as a non-root USER. More follows.', tags: 'build', latestVersion: 1, origin: 'stock', createdAt: '', updatedAt: '', bytes: 3000, tier: 'platform' }]))
    return new Response(JSON.stringify({ generated: [] }))
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    act(() => { root.render(<QueryClientProvider client={client}><MemoryRouter><GuidanceLibrary /></MemoryRouter></QueryClientProvider>) })
    await vi.waitFor(() => expect(el.querySelector('[role=listitem]')).not.toBeNull())
    // A full stop that is code, not the end of a sentence, does not cut it.
    expect([...el.querySelectorAll('.pack-purpose')].map(p => p.textContent)).toContain('The final stage of a build: . part runs as a non-root USER.')
    const row = [...el.querySelectorAll('[role=listitem]')].find(r => r.textContent?.includes('health'))!
    expect(row.querySelector('.pack-name')?.textContent).toBe('health declared')
    expect(row.querySelector('.pack-purpose')?.textContent).toBe(summary)
  } finally { act(() => root.unmount()); client.clear() }
})

// One generated section's page: its title is the section's own heading (else
// its name in words), not the raw path, and its
// markdown is rendered as words that wrap, not an unwrapped source block.
it('titles a generated section by its own heading, never its raw name, and renders its markdown', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ generated: [{ name: 'platform/complaints-box', source: 'instance settings', bodyMd: '# The complaints box\n\nWhen something is **unreasonable**, file a complaint.', bytes: 60, always: true }], alwaysBytes: 60, conditionalBytes: 0 }))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    act(() => { root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/guidance/generated/platform%2Fcomplaints-box']}><Routes><Route path="/guidance/generated/:name" element={<GeneratedGuidance page />} /></Routes></MemoryRouter></QueryClientProvider>) })
    await vi.waitFor(() => expect(el.querySelector('.md strong')?.textContent).toBe('unreasonable'))
    // The section's own heading is the page's one title.
    expect([...el.querySelectorAll('h1')].map(h => h.textContent)).toEqual(['The complaints box'])
    expect(el.querySelector('pre.source')).toBeNull()
  } finally { act(() => root.unmount()); client.clear() }
})
