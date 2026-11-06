// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { settled } from '../settled'
import PackPage from './PackPage'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
afterEach(() => vi.unstubAllGlobals())
it.each([true, false])('owns visibility, edit, delete and recommendation (enabled=%s)', async enabled => {
  const sent: [string, unknown][] = []
  const declaration = { guidance: ['team/code-review'], workflows: [], policies: [] }
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') { sent.push([url, JSON.parse(String(init.body))]); return new Response('{}') }
    const data = url.endsWith('/authz/me') ? { instance: { 'standards:manage-packs': true } }
      : url.endsWith('/workflows/library') ? [{ name: 'review', description: 'Review the work' }]
      : url.endsWith('/guidance/blocks') ? []
      : url.endsWith('/policy-library') ? { library: [] }
      : url.endsWith('/packs') ? [{ name: 'team', enabled }]
      : url.includes('/conformance/what-if/') ? { id: 'assessment', pack: 'team', version: 2 }
      : { name: 'team', origin: 'custom', description: 'Team rules', recommended: 1, latest: 2, declaration, versions: [{ version: 2, at: '', spec: declaration, pins: { blocks: {}, workflows: {} } }] }
    return new Response(JSON.stringify(data))
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  const button = (text: string) => [...el.querySelectorAll('button')].find(b => b.textContent === text)!
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/packs/team?whatIf=assessment']}><Routes><Route path="/packs/:name" element={<PackPage />} /></Routes></MemoryRouter></QueryClientProvider>))
    await settled(client)
    expect([...el.querySelectorAll('h2')].map(h => h.textContent)).toContain('Guidance1')
    expect(button('Edit')).toBeDefined()
    expect(button('Delete…')).toBeDefined()
    await act(async () => button(enabled ? 'Hide' : 'Make available').click())
    expect(sent).toContainEqual(['/api/v1/packs/team/enabled', { enabled: !enabled }])
    await act(async () => button('Recommend v2').click())
    await act(async () => button('Recommend it').click())
    expect(sent).toContainEqual(['/api/v1/packs/team/recommended', { version: 2, whatIf: 'assessment' }])
    await act(async () => button('Delete…').click())
    expect(el.textContent).toContain('Projects keep the versions they applied.')
    expect(vi.mocked(fetch).mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
    await act(async () => button('Edit').click())
    await settled(client)
    expect(el.textContent).toContain('Edit pack: team')
    expect(el.textContent).toContain('review')
    expect(el.querySelector('input[aria-label="Name"]')).not.toBeNull()
  } finally { await act(async () => root.unmount()); client.clear() }
})
