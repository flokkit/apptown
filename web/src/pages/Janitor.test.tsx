// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { settled } from '../settled'
import Janitor from './Janitor'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const project = '00000000-0001-7000-8000-000000000000'
const deployment = '00000000-0002-7000-8000-000000000000'
const target = '00000000-0003-7000-8000-000000000000'
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

it('offers no consent form while automatic updates are unavailable', async () => {
  const requests: { path: string; body: Record<string, unknown> }[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const path = String(input).replace('/api/v1', '')
    if (init?.method === 'POST') {
      const body = JSON.parse(String(init.body))
      requests.push({ path, body })
      if (path.endsWith('/preview')) return new Response(JSON.stringify({ definition: body, projects: [], shown: 'Exact bounded full-delivery consent', digest: 'shown-digest' }))
      return new Response(JSON.stringify({ ruleId: project, revision: 1, enabled: true, question: { definition: body.definition }, issuer: { name: 'Alice' } }))
    }
    const data: Record<string, unknown> = {
      '/authz/me': { actorId: project }, '/projects/shop': { id: project, name: 'Shop', slug: 'shop' },
      [`/standing-approvals?projectId=${project}`]: [],
      '/projects/shop/lanes': { lanes: [{ name: 'quick-change', version: 1, selected: true }], system: [] },
      '/projects/shop/deployments': [{ id: deployment, targetId: target, name: 'customer', displayLabel: 'Customer office', kind: 'standing' }],
    }
    if (!(path in data)) throw new Error(`unexpected read ${path}`)
    return new Response(JSON.stringify(data[path]))
  }))
  const host = document.createElement('div'); document.body.append(host)
  const root = createRoot(host); const client = newQueryClient()
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/projects/shop/janitor']}><Routes><Route path="/projects/:slug/janitor" element={<Janitor />} /></Routes></MemoryRouter></QueryClientProvider>))
    await settled(client)
    expect(requests).toEqual([])
    expect(host.textContent).toContain('Automatic updates are not available yet')
    expect(host.textContent).toContain('Coming soon')
    expect(host.querySelector('select')).toBeNull()
    expect(host.querySelector('input')).toBeNull()
    expect(requests).toEqual([])
  } finally { await act(async () => root.unmount()); host.remove(); client.clear() }
})

// Saved consent remains manageable even while new automatic updates are unavailable.
it('disables saved consent, refreshes the list, and keeps history and revision available', async () => {
  const definition = { administrative: false, projects: [{ projectId: project, workflow: 'quick-change', environments: [] }], categories: { dependencies: 'patch', baseImages: false, securityFixes: true, waysOfWorking: false }, delivery: 'land-only' }
  let rule = { ruleId: project, revision: 1, enabled: true, issuer: { name: 'Alice' }, changedBy: { name: 'Alice' }, question: { definition, shown: 'Small dependency fixes', digest: 'original' } }
  const original = rule
  let reads = 0
  const requests: { path: string; body: unknown }[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const path = String(input).replace('/api/v1', '')
    if (init?.method === 'POST') {
      const body = JSON.parse(String(init.body)); requests.push({ path, body })
      if (path.endsWith('/disable')) { rule = { ...rule, revision: 2, enabled: false, changedBy: { name: 'Bob' } }; return new Response(JSON.stringify(rule)) }
      if (path.endsWith('/preview')) return new Response(JSON.stringify({ definition: body, shown: 'Revised small fixes', digest: 'revised' }))
      rule = { ...rule, revision: 3, enabled: true }; return new Response(JSON.stringify(rule))
    }
    if (path === `/standing-approvals?projectId=${project}`) { reads++; return new Response(JSON.stringify([{ rule, reason: 'Automatic updates are not available yet.' }])) }
    if (path.endsWith('/history')) return new Response(JSON.stringify([rule, original]))
    if (path === '/projects') return new Response(JSON.stringify([{ id: project, slug: 'shop', name: 'Shop' }]))
    if (path.endsWith('/lanes')) return new Response(JSON.stringify({ lanes: [{ name: 'quick-change', version: 1, selected: true }], system: [] }))
    if (path.endsWith('/deployments')) return new Response('[]')
    if (path === '/projects/shop') return new Response(JSON.stringify({ id: project, slug: 'shop', name: 'Shop' }))
    throw new Error(`unexpected read ${path}`)
  }))
  const host = document.createElement('div'); document.body.append(host)
  const root = createRoot(host); const client = newQueryClient()
  const press = async (label: string) => act(async () => { [...host.querySelectorAll('button')].find(b => b.textContent === label)!.click() })
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/projects/shop/janitor']}><Routes><Route path="/projects/:slug/janitor" element={<Janitor />} /></Routes></MemoryRouter></QueryClientProvider>))
    await settled(client)
    await press('Disable'); await settled(client)
    expect(requests).toEqual([{ path: `/standing-approvals/${project}/disable`, body: { expectedRevision: 1 } }])
    expect(reads).toBeGreaterThan(1)
    expect(host.textContent).toContain('Disabled by Bob')
    expect([...host.querySelectorAll('button')].some(b => b.textContent === 'Disable')).toBe(false)
    await press('History'); await settled(client)
    expect(host.textContent).toContain('Revision 2: disabled')
    expect(host.textContent).toContain('Revision 1: enabled')
    await press('Revise consent'); await settled(client)
    await press('Preview'); await settled(client)
    expect(host.textContent).toContain('Revised small fixes')
    await press('I agree to this standing consent'); await settled(client)
    expect(requests.at(-1)).toEqual({ path: '/standing-approvals', body: { ruleId: project, expectedRevision: 2, definition, shownDigest: 'revised' } })
    expect(host.textContent).toContain('Enabled by Alice')
  } finally { await act(async () => root.unmount()); host.remove(); client.clear() }
})
