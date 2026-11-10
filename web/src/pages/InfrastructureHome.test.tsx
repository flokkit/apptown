// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { getKey, newQueryClient } from '../data'
import { InfrastructureHome } from './InfrastructureHome'

it.each(['low on disk', 'resources not known'])('shows an owner attention condition before the app count on a reachable host: %s', (condition) => {
  const client = newQueryClient()
  client.setQueryData(getKey('/auth/me'), { mode: 'internal', user: 'sam' })
  client.setQueryData(getKey('/infrastructure'), { targets: [{ target: 'kitchen', status: 'ready', statusLabel: 'working', resources: [], live: true }] })
  client.setQueryData(getKey('/targets'), { targets: [{ name: 'kitchen', displayLabel: 'Kitchen', deployments: [] }] })
  client.setQueryData(getKey('/infrastructure/disks'), { disks: [] })
  client.setQueryData(getKey('/attention'), { sources: [], items: [{ key: 'disk', title: `Kitchen: ${condition}`, detail: 'Free some disk space.', href: '/targets/kitchen', statusLabel: condition === 'resources not known' ? 'unknown' : 'failed', severity: 'warning', stale: false }] })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><InfrastructureHome /></QueryClientProvider></MemoryRouter>)
  expect(html).toContain(condition === 'resources not known' ? 'capacity not known' : condition)
  expect(html).not.toContain(`Kitchen: ${condition}`)
  expect(html).toContain(`${condition === 'resources not known' ? 'unknown' : 'failed'}</span>`)
  const el = document.createElement('div'); el.innerHTML = html
  expect(el.querySelector('.target-list .phone-state')?.textContent).toBe(`● ${condition === 'resources not known' ? 'unknown' : 'failed'}`)
  expect(html).not.toContain('0 apps')
  expect(html).not.toContain('working</span>')
})

it('uses one Targets heading and linked table names with the create action in the header', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/auth/me'), { mode: 'internal', user: 'sam' })
  client.setQueryData(getKey('/attention'), { sources: [], items: [] })
  client.setQueryData(getKey('/infrastructure'), { targets: [{ target: 'kitchen', statusLabel: 'working', live: true, resources: [] }] })
  client.setQueryData(getKey('/targets'), { targets: [{ name: 'kitchen', deployments: [{ project: 'shop', name: 'main' }] }] })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><InfrastructureHome /></QueryClientProvider></MemoryRouter>)
  const el = document.createElement('div'); el.innerHTML = html
  expect(el.querySelector('h1')?.textContent).toBe('Targets')
  expect(el.querySelector('h2')?.textContent).toBe('All targets1')
  expect(el.querySelector('h2 .section-count')?.textContent).toBe('1')
  expect(el.querySelector('.page-head-row a[href="/targets/new"]')).not.toBeNull()
  expect(el.querySelector('table.target-list[aria-label="Targets"] tbody a[href="/targets/kitchen"]')).not.toBeNull()
  expect(el.querySelectorAll('h2')).toHaveLength(3)
  expect(el.querySelector('[data-label="Apps"]')?.textContent).toBe('1 app')
  expect(html).not.toContain('View target')
  expect(html).not.toContain('Job queue')
})

// On a phone the Place line says where the target is; what was observed of it
// has its own Observation line.
it('keeps the place under Place and the observation under its own label', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/auth/me'), { mode: 'internal', user: 'sam' })
  client.setQueryData(getKey('/attention'), { sources: [], items: [] })
  client.setQueryData(getKey('/infrastructure'), { targets: [{ target: 'kitchen', place: 'the kitchen cupboard', statusLabel: 'failed', live: true, resources: [{ record: 'missing' }] }] })
  client.setQueryData(getKey('/targets'), { targets: [{ name: 'kitchen', deployments: [] }] })
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><InfrastructureHome /></QueryClientProvider></MemoryRouter>)
  expect(el.querySelector('td[data-label="Place"]')?.textContent).toBe('the kitchen cupboard')
  const observation = el.querySelector('td[data-label="Observation"]')!
  expect(observation.classList.contains('record-wide')).toBe(false)
  expect(observation.textContent).not.toBe('')
})
