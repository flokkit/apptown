// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { enabledFeatures } from '../features.fixture'
import { settled } from '../settled'
import PackPage from './PackPage'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
afterEach(() => vi.unstubAllGlobals())
it.each([{enabled:true,beta:true}, {enabled:false,beta:true}, {enabled:true,beta:false}])('owns visibility, edit and delete; publications need no recommendation (%j)', async ({enabled, beta}) => {
  const sent: [string, unknown][] = []
  const declaration = { guidance: ['team/code-review'], workflows: [], policies: [] }
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (url.endsWith('/features')) return new Response(JSON.stringify(enabledFeatures.map(f => f.area === 'what-ifs' ? { ...f, enabled: beta } : f)))
    if (init?.method === 'POST') { sent.push([url, JSON.parse(String(init.body))]); return new Response('{}') }
    const data = url.endsWith('/authz/me') ? { instance: { 'standards:manage-packs': true } }
      : url.endsWith('/workflows/library') ? [{ name: 'review', description: 'Review the work' }]
      : url.endsWith('/guidance/blocks') ? []
      : url.endsWith('/policy-library') ? { library: [] }
      : url.endsWith('/packs') ? [{ name: 'team', enabled }]
      : url.endsWith('/wow') ? { packs: [] }
      : url.endsWith('/projects') ? []
      : url.includes('/conformance/what-if?') ? { whatIf: null, eligible: [] }
      : url.includes('/conformance/what-if/') ? { id: 'assessment', pack: 'team', version: 2 }
      : { name: 'team', origin: 'custom', description: 'Team rules', recommended: 1, latest: 2, declaration, versions: [{ version: 2, at: '', spec: declaration, pins: { blocks: {}, workflows: {} } }] }
    return new Response(JSON.stringify(data))
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  const button = (text: string) => [...el.querySelectorAll('button')].find(b => b.textContent === text)!
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[beta ? '/packs/team?whatIf=assessment' : '/packs/team']}><Routes><Route path="/packs/:name" element={<PackPage />} /></Routes></MemoryRouter></QueryClientProvider>))
    await settled(client)
    expect([...el.querySelectorAll('h2')].map(h => h.textContent)).toContain('Guidance1')
    expect(button('Edit')).toBeDefined()
    expect(button('Delete…')).toBeDefined()
    await act(async () => el.querySelector<HTMLButtonElement>('[role="switch"][aria-label="team"]')!.click())
    expect(sent).toContainEqual(['/api/v1/packs/team/enabled', { enabled: !enabled }])
    expect(button('Recommend v2')).toBeUndefined()
    expect(sent.some(([path]) => path.endsWith('/recommended'))).toBe(false)
    if (!beta) {
      expect(el.querySelector('a[href="/packs/team/versions/2"]')).not.toBeNull()
      expect(vi.mocked(fetch).mock.calls.some(([path]) => String(path).includes('/conformance/what-if'))).toBe(false)
    }
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

it.each([false, true])('version page starts what-ifs only when enabled (%s)', async beta => {
  const spec = { guidance: [], workflows: [], policies: [] }
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    url.endsWith('/features') ? enabledFeatures.map(f => f.area === 'what-ifs' ? { ...f, enabled: beta } : f)
      : url.endsWith('/authz/me') ? { instance: { 'standards:manage-packs': true } }
      : url.endsWith('/packs') ? []
      : { name: 'team', versions: [{ version: 2, spec, pins: { blocks: {}, workflows: {} } }] }
  ))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/packs/team/versions/2']}><Routes><Route path="/packs/:name/versions/:version" element={<PackPage />} /></Routes></MemoryRouter></QueryClientProvider>))
    await settled(client)
    expect(el.textContent).toContain('Version 2')
    expect(el.textContent?.includes('beta')).toBe(beta)
    expect(vi.mocked(fetch).mock.calls.some(([path]) => String(path).includes('/conformance/what-if?'))).toBe(beta)
  } finally { await act(async () => root.unmount()); client.clear() }
})

// A pack's workflows and policies say what each does, from their libraries.
it('describes each workflow and policy the pack holds', async () => {
  const declaration = { guidance: [], workflows: ['excavate'], policies: [{ name: 'scanner-bound' }] }
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    const data = url.endsWith('/features') ? enabledFeatures
      : url.endsWith('/authz/me') ? { instance: {} }
      : url.endsWith('/workflows/library') ? [{ name: 'excavate', description: 'Reads an existing app and writes down how it works.' }]
      : url.endsWith('/policy-library') ? { library: [{ name: 'scanner-bound', description: 'Scanners run before landing.' }] }
      : url.endsWith('/packs') ? [{ name: 'team', enabled: true }]
      : { name: 'team', origin: 'stock', description: '', recommended: 1, latest: 1, declaration, versions: [] }
    return new Response(JSON.stringify(data))
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/packs/team']}><Routes><Route path="/packs/:name" element={<PackPage />} /></Routes></MemoryRouter></QueryClientProvider>))
    await settled(client)
    const rows = [...el.querySelectorAll('.pack-content-row')].map(r => r.textContent)
    expect(rows).toContain('excavateReads an existing app and writes down how it works.')
    expect(rows).toContain('scanner-boundScanners run before landing.')
  } finally { await act(async () => root.unmount()); client.clear() }
})
