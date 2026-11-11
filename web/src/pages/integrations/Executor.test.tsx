// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient, getKey } from '../../data'
import { ExecutorObservation } from './Executor'

it.each([false, true])('shows observed worker facts or the AppTown server (worker: %s)', configured => {
  const client = newQueryClient()
  client.setQueryData(getKey('/executor'), { configured, address: 'https://worker.example:7443', state: 'reachable', checkedAt: '2026-11-07T01:00:00Z', version: 'v1', appTownVersion: 'v2', versionMismatch: true, previews: [{ project: 'shop', release: 4 }], previewsUnknown: false })
  client.setQueryData(getKey('/infrastructure/disks'), { disks: [{ what: 'worker Docker', unknown: true, totalBytes: 100, freeBytes: 50 }] })
  client.setQueryData(getKey('/runs/executing'), { executing: [{ project: 'shop', runId: 'run-id', changeId: 'change-id', stepId: 'step-id', title: 'Fix dates', step: 'test' }], operations: [] })
  client.setQueryData(getKey('/tiers'), { tiers: [{ name: 'preview', apps: [{ slug: 'shop', name: 'Shop', changeId: 'change-id', changeNumber: 2 }] }] })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><ExecutorObservation /></QueryClientProvider></MemoryRouter>)
  expect(html).toContain('/changes/change-id?tab=details&amp;run=run-id')
  expect(html).toContain('/projects/shop/releases/4')
  expect(html).not.toContain('<button')
  expect(html).toContain('unknown')
  if (configured) {
    expect(html).toContain('https://worker.example:7443')
    expect(html).toContain('reachable')
    expect(html).toContain('differs from AppTown (v2)')
  } else expect(html).toContain('Work runs on AppTown’s own server.')
})

it('keeps each observed disk source identifiable', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/executor'), { configured: false, previews: [] })
  client.setQueryData(getKey('/infrastructure/disks'), { disks: [
    { what: "this AppTown's data folder", totalBytes: 100 * 2 ** 30, freeBytes: 90 * 2 ** 30 },
    { what: "this AppTown's Docker data", totalBytes: 200 * 2 ** 30, freeBytes: 180 * 2 ** 30 },
  ] })
  client.setQueryData(getKey('/runs/executing'), { executing: [] })
  client.setQueryData(getKey('/tiers'), { tiers: [] })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><ExecutorObservation /></QueryClientProvider></MemoryRouter>)
  const el = document.createElement('div'); el.innerHTML = html
  const labels = [...el.querySelectorAll('.capacity-strip h3')].map(e => e.textContent)
  expect(labels).toContain("DISK · this AppTown's data folder")
  expect(labels).toContain("DISK · this AppTown's Docker data")
  expect(html).toContain('10 GB / 100 GB')
  expect(html).toContain('20 GB / 200 GB')
})

// CPU and memory are the reading of the host whose Docker runs the work,
// never a pair of bars that nothing fills; a worker's machine is not read,
// and the page says so instead of drawing empty bars.
it.each([false, true])('shows the work host CPU and memory, or says a worker is not read (worker: %s)', configured => {
  const client = newQueryClient()
  client.setQueryData(getKey('/executor'), { configured, address: 'https://worker.example:7443', state: 'reachable', previews: [], ...(configured ? {} : { hostTarget: 'built-in-docker' }) })
  client.setQueryData(getKey('/targets/built-in-docker/capacity'), { target: 'built-in-docker', kind: 'docker-host', elastic: false, fixedHost: true, stale: false, words: '',
    cpu: { total: { known: true, value: 8 }, hostUsed: { known: true, value: 2 }, promised: {} },
    memory: { total: { known: true, value: 8 * 2 ** 30 }, hostUsed: { known: true, value: 3 * 2 ** 30 }, promised: {} } })
  client.setQueryData(getKey('/infrastructure/disks'), { disks: [] })
  client.setQueryData(getKey('/runs/executing'), { executing: [] })
  client.setQueryData(getKey('/tiers'), { tiers: [] })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><ExecutorObservation /></QueryClientProvider></MemoryRouter>)
  const el = document.createElement('div'); el.innerHTML = html
  const strips = Object.fromEntries([...el.querySelectorAll('.capacity-strip')].map(s => [s.querySelector('h3')!.textContent, s.querySelector('span')!.textContent]))
  if (configured) {
    expect(strips.CPU).toBeUndefined()
    expect(strips.MEMORY).toBeUndefined()
    expect(el.textContent).toContain("The worker's CPU and memory are not read.")
  } else {
    expect(strips.CPU).toBe('2 / 8')
    expect(strips.MEMORY).toBe('3 GB / 8 GB')
    expect(html).toContain('href="/targets/built-in-docker"')
  }
})
