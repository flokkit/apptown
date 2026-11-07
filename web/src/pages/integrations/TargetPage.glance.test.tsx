// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { getKey, newQueryClient } from '../../data'
import { TargetPage } from './TargetPage'

it.each([false, true])('leads with the host name, state and app address, and links to its settings (unknown capacity: %s)', (unknown) => {
  const client = newQueryClient()
  client.setQueryData(getKey('/auth/me'), { mode: 'internal', user: 'sam' })
  client.setQueryData(getKey('/attention'), { sources: [], items: unknown ? [{ key: 'inventory', title: 'Kitchen: resources not known', detail: 'Inventory could not be checked.', href: '/targets/kitchen', statusLabel: 'unknown', stale: true }] : [] })
  client.setQueryData(getKey('/targets/kitchen'), {
    target: { id: 'host-id', name: 'kitchen', displayLabel: 'Kitchen', status: 'ready', statusLabel: 'working',
      check: { detail: 'Docker daemon answers', observedAt: '2026-10-29T00:00:00Z', stale: true, failure: 'SSH connection refused', unknown: true, waiting: false },
      kind: 'docker-host', kindInfo: { title: 'Docker host' }, ingress: { domain: 'apps.example.test' },
      address: { host: 'ssh://root@10.0.0.4', hostname: '10.0.0.4', user: 'root', port: 22, keyFrom: 'HOST_KEY' },
      capabilities: { needs: ['postgres'], rollout: ['replace'], persistentVolumes: true },
      needsMode: 'emulators', deployments: [], offers: [], retained: 0 },
    history: [], builtIn: true,
  })
  client.setQueryData(getKey('/infrastructure'), { targets: [{ target: 'kitchen', kind: 'docker-host', live: true, resources: [{ kind: 'volume', name: 'orphan-data', record: 'unrecorded' }] }] })
  const html = renderToStaticMarkup(<MemoryRouter initialEntries={['/targets/kitchen']}><QueryClientProvider client={client}><Routes><Route path="/targets/:name" element={<TargetPage />} /></Routes></QueryClientProvider></MemoryRouter>)
  const el = document.createElement('div')
  el.innerHTML = html
  expect([...el.querySelectorAll('button')].filter(b => b.textContent === 'View resource')).toHaveLength(0)
  expect(el.querySelector('a[href="/targets/kitchen/resources/volume%3Aorphan-data"]')).not.toBeNull()
  expect(el.querySelector('a[href="/targets/kitchen/capacity"]')).not.toBeNull()
  expect(el.querySelector('.target-settings')?.textContent).toContain('SSH keysecret HOST_KEY')
  expect(el.querySelector('.target-settings')?.textContent?.match(/10\.0\.0\.4/g)).toHaveLength(1)
  expect(el.querySelector('.target-settings')?.textContent).toContain('ssh://root@10.0.0.4')
  expect(el.querySelector('.target-settings a[href="/integrations/secrets"]')?.textContent).toBe('Edit secret')
  // The lead is everything before the first section: name, state, address.
  el.querySelectorAll('section').forEach(d => d.remove())
  el.querySelectorAll('.status-help > div').forEach(d => d.remove())
  const first = el.textContent
  expect(first).toContain('Target check: Docker daemon answers')
  expect(first).toContain('SSH connection refused')
  expect(first).toContain('(old)')
  expect(first).toContain('Kitchen')
  expect(first).toContain('*.apps.example.test')
  expect(html).toContain(`${unknown ? 'unknown' : 'working'}</span>`)
  if (unknown) {
    expect(first).toContain('capacity not known')
    expect(first).not.toContain('Kitchen:')
    expect(html).not.toContain('working</span>')
  }
  expect(first).not.toMatch(/HOST_KEY|10\.0\.0\.4|replace|has disks|emulators|Edit display name/)
})

it('keeps a deployment resource count unknown until its inventory can be read', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/auth/me'), { mode: 'internal', user: 'sam' })
  client.setQueryData(getKey('/attention'), { sources: [], items: [] })
  client.setQueryData(getKey('/targets/kitchen'), {
    target: { id: 'host', name: 'kitchen', status: 'ready', statusLabel: 'working', kind: 'docker-host', kindInfo: { title: 'Docker host' }, check: {}, ingress: {}, address: {}, capabilities: { persistentVolumes: true }, needsMode: 'emulators', offers: [], retained: 0,
      deployments: [{ project: 'shop', name: 'main', status: { word: 'working', reason: '', stale: false }, statusLabel: 'working' }] }, history: [], builtIn: true,
  })
  const html = renderToStaticMarkup(<MemoryRouter initialEntries={['/targets/kitchen']}><QueryClientProvider client={client}><Routes><Route path="/targets/:name" element={<TargetPage />} /></Routes></QueryClientProvider></MemoryRouter>)
  expect(html).toContain('<td class="number phone-extra">unknown</td>')
  expect(html).not.toContain('0 resources')
})

it('keeps unknown inventory muted with its reason', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/auth/me'), { mode: 'internal', user: 'sam' })
  client.setQueryData(getKey('/attention'), { sources: [], items: [] })
  client.setQueryData(getKey('/targets/kitchen'), { target: { id: 'host', name: 'kitchen', status: 'ready', statusLabel: 'unknown', kind: 'docker-host', kindInfo: {}, check: {}, ingress: {}, address: {}, capabilities: {}, deployments: [], offers: [], retained: 0 }, history: [], builtIn: true })
  client.setQueryData(getKey('/infrastructure'), { targets: [{ target: 'kitchen', unknown: true, live: false, error: 'SSH did not answer', resources: [] }] })
  const html = renderToStaticMarkup(<MemoryRouter initialEntries={['/targets/kitchen']}><QueryClientProvider client={client}><Routes><Route path="/targets/:name" element={<TargetPage />} /></Routes></QueryClientProvider></MemoryRouter>)
  const el = document.createElement('div'); el.innerHTML = html
  const section = el.querySelector('section[aria-label="Resources not accounted for"]')!
  expect(section.textContent).toContain('Inventory is unknown: SSH did not answer')
  expect(section.querySelector('.error')).toBeNull()
})

it('shows recorded deployment resources, generation and address without a current host inventory', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/auth/me'), { mode: 'internal', user: 'sam' })
  client.setQueryData(getKey('/attention'), { sources: [], items: [] })
  client.setQueryData(getKey('/targets/kitchen'), {
    target: { id: 'host', name: 'kitchen', status: 'ready', statusLabel: 'working', kind: 'docker-host', kindInfo: {}, check: {}, ingress: {}, address: {}, capabilities: {}, offers: [], retained: 0,
      deployments: [{ project: 'shop', name: 'main', displayLabel: 'Production', status: { word: 'working', reason: '', stale: false }, statusLabel: 'working', url: 'https://shop.example.test', generation: 16 }] }, history: [], builtIn: true,
  })
  client.setQueryData(getKey('/infrastructure'), { targets: [{ target: 'kitchen', live: false, unknown: true, invalidated: true, error: 'SSH did not answer', resources: [
    { kind: 'container', name: 'shop-main', record: 'recorded', hosts: [{ project: 'shop', deployment: 'main', generation: 16 }] },
    { kind: 'volume', name: 'shop-data', record: 'recorded', hosts: [{ project: 'shop', deployment: 'main', generation: 16 }] },
    { kind: 'container', name: 'other-main', record: 'recorded', hosts: [{ project: 'other', deployment: 'main' }] },
  ] }] })
  const html = renderToStaticMarkup(<MemoryRouter initialEntries={['/targets/kitchen']}><QueryClientProvider client={client}><Routes><Route path="/targets/:name" element={<TargetPage />} /></Routes></QueryClientProvider></MemoryRouter>)
  const el = document.createElement('div'); el.innerHTML = html
  const row = el.querySelector('table[aria-label="What runs here"] tbody tr')!
  expect(row.querySelector('.phone-name a')?.textContent).toBe('shop/Production')
  expect(row.querySelector('.phone-detail')?.textContent).toBe('shop.example.test')
  expect(row.querySelector('.phone-extra a')?.textContent).toBe('2 resources')
  expect(row.querySelector('.phone-extra a')?.getAttribute('href')).toBe('/targets/kitchen/resources?deployment=shop%2Fmain')
  expect(row.querySelector('.phone-time')?.textContent).toBe('16')
  expect(row.textContent).not.toContain('unknown')
})
