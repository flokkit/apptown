import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { getKey, newQueryClient } from '../data'
import type { DiskReadings } from '../wire.gen'
import UsagePage from './Usage'
import { UsageDisk } from './UsageDisk'

function render(data: DiskReadings) {
  const client = newQueryClient()
  client.setQueryData(getKey('/infrastructure/disks'), data)
  return renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><UsageDisk /></QueryClientProvider></MemoryRouter>)
}
const at = new Date().toISOString()
describe('Usage disk measurements', () => {
  it('shows measured categories and a capacity strip, warning strictly above 80%', () => {
    const html = render({ disks: [{ what: 'Worker apps', totalBytes: 100 * 2 ** 30, freeBytes: 19 * 2 ** 30, readAt: at, low: false, refuses: false, categories: [{ name: 'Images', bytes: 3 * 2 ** 30, readAt: at }, { name: 'Build cache', bytes: 0, readAt: at }] }] })
    for (const text of ['Disk', '81 GB / 100 GB', 'Images', '3 GB', 'Build cache', '0 B', 'About Disk', 'capacity-strip-high']) expect(html).toContain(text)
    for (const text of ['% full', 'free of', 'checked now', 'Above 80%.', 'not available yet', 'Folder sizes count']) expect(html).not.toContain(text)
    expect(render({ disks: [{ what: 'Controller', totalBytes: 100, freeBytes: 20, readAt: at, low: false, refuses: false }] })).not.toContain('capacity-strip-high')
  })
  it('shows only reading times that add information', () => {
    const older = new Date(Date.now() - 3600000).toISOString()
    const html = render({ disks: [{ what: 'Controller', totalBytes: 100, freeBytes: 50, readAt: older, low: false, refuses: false, categories: [{ name: 'Database', bytes: 10, readAt: older }, { name: 'Backups', bytes: 20, readAt: at }] }] })
    expect(html.match(/checked 1h ago/g)).toHaveLength(1)
    expect(html.match(/checked now/g)).toHaveLength(1)
  })
  it('keeps missing, failed and stale readings distinct from measured zero', () => {
    expect(render({ disks: [] })).toContain('Waiting for the first disk check.')
    const html = render({ disks: [{ what: 'Worker', totalBytes: 100, freeBytes: 19, readAt: at, low: false, refuses: false, stale: true, unknown: 'unavailable', categories: [{ name: 'Images', bytes: 0, unknown: 'unavailable', readAt: at }] }] })
    expect(html).not.toContain('Unknown')
    expect(html).toContain('unknown')
    expect(html).not.toContain('aria-valuenow')
    expect(html).not.toContain('81% full')
    expect(html).not.toContain('0 B')
    const stale = render({ disks: [{ what: 'Controller', totalBytes: 100, freeBytes: 19, readAt: at, low: false, refuses: false, stale: true, error: 'The disk check failed.' }] })
    expect(stale).toContain('last measured values')
    expect(stale).toContain('last seen')
  })
})

it('puts disk measurements on Usage even while model usage is unavailable', () => {
 const client = newQueryClient()
 client.setQueryData(getKey('/infrastructure/disks'), { disks: [] })
 const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><UsagePage /></QueryClientProvider></MemoryRouter>)
 expect(html).toContain('aria-label="Disk"')
 expect(html).toContain('Waiting for the first disk check.')
})

it('shows whole sizes without decimals, preserves fractions and uses lowercase unknown', () => {
  const html = render({ disks: [{ what: 'Controller', totalBytes: 100 * 2 ** 30, freeBytes: 64 * 2 ** 30, readAt: at, low: false, refuses: false, categories: [{ name: 'Images', bytes: 36 * 2 ** 30, readAt: at }, { name: 'Volumes', bytes: 0, unknown: 'unavailable', readAt: at }, { name: 'Workspaces', bytes: 1.25 * 2 ** 30, readAt: at }, { name: 'Database', bytes: 244 * 2 ** 20, readAt: at }, { name: 'Local backups', bytes: 1.2 * 2 ** 30, readAt: at }] }] })
  for (const text of ['36 GB / 100 GB', '<dd>36 GB</dd>', '<dd>unknown</dd>', '1.25 GB', '244 MB', '1.2 GB']) expect(html).toContain(text)
  for (const text of ['36.00 GB', '244.0 MB', '1.20 GB', 'Unknown']) expect(html).not.toContain(text)
})
