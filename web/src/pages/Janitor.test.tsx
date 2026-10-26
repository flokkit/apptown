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

it('requires exact environment selection and a separate displayed consent before saving', async () => {
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
    expect(host.textContent).toContain('Automatic processing is not available yet')
    const selects = Array.from(host.querySelectorAll('select'))
    const delivery = selects.find(s => Array.from(s.options).some(o => o.value === 'full'))!
    expect(delivery.value).toBe('full')
    const environment = Array.from(host.querySelectorAll('input')).find(i => i.parentElement?.textContent?.includes('Customer office'))!
    expect(environment.checked).toBe(false)
    const lane = selects.find(s => Array.from(s.options).some(o => o.value === 'quick-change'))!
    await act(async () => { lane.value = 'quick-change'; lane.dispatchEvent(new Event('change', { bubbles: true })) })
    await act(async () => { environment.click() })
    const button = (text: string) => Array.from(host.querySelectorAll('button')).find(b => b.textContent === text)!
    await act(async () => { button('Read the exact consent').click(); await settled(client) })
    expect(requests).toHaveLength(1)
    expect(requests[0].body).toMatchObject({ delivery: 'full', projects: [{ projectId: project, workflow: 'quick-change', environments: [{ deploymentId: deployment, targetId: target, promotion: false }] }] })
    expect(host.textContent).toContain('Exact bounded full-delivery consent')
    await act(async () => { button('I agree to this standing consent').click(); await settled(client) })
    expect(requests).toHaveLength(2)
    expect(requests[1]).toMatchObject({ path: '/standing-approvals', body: { shownDigest: 'shown-digest', expectedRevision: 0 } })
    expect(host.textContent).toContain('No update or release has started')
    await act(async () => { delivery.value = 'land-only'; delivery.dispatchEvent(new Event('change', { bubbles: true })) })
    await act(async () => { button('Read the exact consent').click(); await settled(client) })
    expect(requests[2].body).toMatchObject({ delivery: 'land-only', projects: [{ projectId: project, environments: [] }] })
  } finally { await act(async () => root.unmount()); host.remove(); client.clear() }
})
