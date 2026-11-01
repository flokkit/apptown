import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { getKey, newQueryClient } from '../data'
import type { DiskReadings } from '../wire.gen'
import { InfrastructureHome, OwnDisks } from './InfrastructureHome'

const render = (d: DiskReadings) => {
  const client = newQueryClient()
  client.setQueryData(getKey('/infrastructure/disks'), d)
  return renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><OwnDisks /></QueryClientProvider></MemoryRouter>)
}

// AppTown's own disks are each their own row, with where they are, when
// they were read and what they mean for new work: refused below the floor,
// refused when the worker could not read its own, not read from here (a
// worker that did not answer), or room. Unknown is never shown as free.
describe("this AppTown's disks", () => {
  it('shows each reading apart, with its time and what it means for new work', () => {
    const at = '2026-10-10T01:00:00Z'
    const html = render({ disks: [
      { what: "this AppTown's data folder", path: '/var/lib/apptown', totalBytes: 100 * 2 ** 30, freeBytes: 60 * 2 ** 30, refuses: false, low: false, readAt: at },
      { what: "the worker's state folder", path: '/srv/worker', totalBytes: 100 * 2 ** 30, freeBytes: 2 ** 30, floorBytes: 5 * 2 ** 30, refuses: true, low: true, readAt: at },
      { what: "the worker's Docker data (tasks, images, private Docker)", path: '', refuses: true, low: false, unknown: 'where Docker keeps its data: exit status 1', readAt: at },
    ] })
    expect(html).toContain('/var/lib/apptown')
    expect(html).toContain('60.00 GB free of 100.00 GB')
    expect(html).toContain('working</span>')
    expect(html).toContain('New work stops below 5.00 GB free.')
    expect(html).toContain('where Docker keeps its data: exit status 1')
    expect(html).toContain('New work is blocked. Check the disk and free space.')
    expect(html).toContain('ago')
    const summaries = [...html.matchAll(/<summary class="host-row">([\s\S]*?)<\/summary>/g)].map(m => m[1]).join(' ')
    expect(summaries).toContain('60% disk free')
    expect(summaries).not.toContain('/var/lib')
    expect(summaries).not.toContain('where Docker')
  })
  it('shows the owner warning threshold without claiming controller refusal', () => {
    const html = render({ disks: [{ what: "this AppTown's data folder", totalBytes: 4096 * 2 ** 30, freeBytes: 30 * 2 ** 30, warningBytes: 40 * 2 ** 30, refuses: false, low: true, readAt: '2026-10-10T01:00:00Z' }] })
    expect(html).toContain('Free some space when less than 40.00 GB is free.')
    expect(html).not.toContain('refused')
    expect(html).not.toContain('tenth')
  })
  it("says a worker that did not answer was not read, not that it refuses", () => {
    const html = render({ disks: [{ what: "the worker's disks", refuses: false, low: false, unknown: 'the worker could not be reached', readAt: '2026-10-10T01:00:00Z' }] })
    expect(html).toContain('unknown</span>')
    expect(html).not.toContain('refused')
  })
})

it.each(['low on disk', 'resources not known'])('shows an owner attention condition before the app count on a reachable host: %s', (condition) => {
  const client = newQueryClient()
  client.setQueryData(getKey('/auth/me'), { mode: 'internal', user: 'sam' })
  client.setQueryData(getKey('/infrastructure'), { targets: [{ target: 'kitchen', status: 'ready', statusLabel: 'working', resources: [], live: true }] })
  client.setQueryData(getKey('/targets'), { targets: [{ name: 'kitchen', displayLabel: 'Kitchen', deployments: [] }] })
  client.setQueryData(getKey('/infrastructure/disks'), { disks: [] })
  client.setQueryData(getKey('/attention'), { sources: [], items: [{ key: 'disk', title: `Kitchen: ${condition}`, detail: 'Free some disk space.', href: '/infrastructure/kitchen', statusLabel: condition === 'resources not known' ? 'unknown' : 'failed', severity: 'warning', stale: false }] })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><InfrastructureHome /></QueryClientProvider></MemoryRouter>)
  expect(html).toContain(condition === 'resources not known' ? 'capacity not known' : condition)
  expect(html).not.toContain(`Kitchen: ${condition}`)
  expect(html).toContain(`${condition === 'resources not known' ? 'unknown' : 'failed'}</span>`)
  expect(html).not.toContain('0 apps')
  expect(html).not.toContain('working</span>')
})

it('shows a three percent disk warning using the owner reading', () => {
  const html = render({ disks: [{ what: "this AppTown's data folder", totalBytes: 4096 * 2 ** 30, freeBytes: 123 * 2 ** 30, warningBytes: 409.6 * 2 ** 30, refuses: false, low: true, readAt: '2026-10-10T01:00:00Z' }] })
  const front = html.split('</summary>')[0]
  expect(front).toContain('low</span>')
  expect(front).toContain('Low on disk')
  expect(front).not.toContain('working')
})

it('shows pending until the first disk check answers', () => {
  expect(render({ disks: [] })).toContain('Waiting for the first disk check.')
})
