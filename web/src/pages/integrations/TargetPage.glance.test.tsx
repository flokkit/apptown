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
      kind: 'docker-host', kindInfo: { title: 'Docker host' }, ingress: { domain: 'apps.example.test' },
      address: { hostname: '10.0.0.4', user: 'root', keyFrom: 'HOST_KEY' },
      capabilities: { needs: ['postgres'], rollout: ['replace'], persistentVolumes: true },
      needsMode: 'emulators', deployments: [], offers: [], retained: 0 },
    history: [], builtIn: true,
  })
  client.setQueryData(getKey('/infrastructure'), { targets: [{ target: 'kitchen', kind: 'docker-host', live: true, resources: [{ kind: 'volume', name: 'orphan-data', record: 'unrecorded' }] }] })
  const html = renderToStaticMarkup(<MemoryRouter initialEntries={['/targets/kitchen']}><QueryClientProvider client={client}><Routes><Route path="/targets/:name" element={<TargetPage />} /></Routes></QueryClientProvider></MemoryRouter>)
  const el = document.createElement('div')
  el.innerHTML = html
  expect([...el.querySelectorAll('button')].filter(b => b.textContent === 'View resource')).toHaveLength(1)
  expect(el.querySelector('.target-settings')?.textContent).toContain('SSH keysecret HOST_KEY')
  expect(el.querySelector('.target-settings a[href="/integrations/secrets"]')?.textContent).toBe('Edit secret')
  expect(el.querySelector('a[href="/targets/kitchen"]')).toBeNull()
  // The lead is everything before the first section: name, state, address.
  el.querySelectorAll('section').forEach(d => d.remove())
  el.querySelectorAll('.status-help > div').forEach(d => d.remove())
  const first = el.textContent
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
