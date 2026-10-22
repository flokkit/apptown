import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { getKey, newQueryClient } from '../data'
import type { DiskReadings } from '../wire.gen'
import { OwnDisks } from './InfrastructureHome'

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
    expect(html).toContain('60.00 GB of 100.00 GB')
    expect(html).toContain('>room<')
    expect(html).toContain('refused: under 5.00 GB free')
    expect(html).toContain('unknown <span class="muted">— where Docker keeps its data: exit status 1</span>')
    expect(html).toContain('refused: not read')
    expect(html).toMatch(/ago<\/td>/)
  })
  it("says a worker that did not answer was not read, not that it refuses", () => {
    const html = render({ disks: [{ what: "the worker's disks", refuses: false, low: false, unknown: 'the worker could not be reached', readAt: '2026-10-10T01:00:00Z' }] })
    expect(html).toContain('>not read<')
    expect(html).not.toContain('refused')
  })
})
