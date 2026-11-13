// @vitest-environment jsdom
import { expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import Deployments from './Deployments'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const app = (deployment: string, label: string, observation: string, word: string) => ({ slug: 'ledger', name: 'Ledger', deploymentId: `internal-${deployment}`, deployment, deploymentLabel: label, targetName: 'lab', status: { word, reason: word === 'stopped' ? 'the application is stopped' : '', stale: false }, observation, observationStale: false, running: observation === 'running', backups: true })

it('lists every deployment separately by display name, including stopped copies, with its use and last backup', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    url.endsWith('/tiers') ? { tiers: [{ name: 'main', title: 'Main', apps: [app('east', 'East office', 'stopped', 'stopped'), { ...app('site', 'Website', 'running', 'working'), static: true }] }, { name: 'release', title: 'Released', apps: [app('west', 'West office', 'running', 'working')] }, { name: 'preview', title: 'Preview', apps: [app('preview-1', 'Preview', 'running', 'working')] }] }
      : url.endsWith('/targets/lab/capacity') ? { target: 'lab', kind: 'docker-host', tenants: [{ kind: 'deployment', name: 'ledger/west', project: 'ledger', deployment: 'west', containers: 2, used: { known: true, value: 96 * 2 ** 20 }, cpuUsed: { known: true, value: 0.25 }, promised: { known: true, value: 0 }, unlimited: 0, cpus: 0, cpuUnlimited: 0, cpuUnread: 0 }] }
      : url.endsWith('/backups') ? { backups: [{ project: 'ledger', deployment: 'west', takenAt: new Date(Date.now() - 3600_000).toISOString(), status: 'ok' }] }
      : {},
  ))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><Deployments /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelectorAll('tbody tr')).toHaveLength(3))
    expect(el.querySelector('h1')?.textContent).toBe('All deployments')
    expect([...el.querySelectorAll('thead th')].map(th => th.textContent)).toEqual(['Project', 'Deployment', 'Target', 'Status', 'Resources', 'Last backup', ''])
    expect(el.textContent).toContain('East office')
    expect(el.textContent).toContain('West office')
    expect(el.textContent).not.toContain('internal-east')
    expect(el.textContent).not.toContain('Preview')
    expect(el.querySelector('a[href="/projects/ledger/deployments/east"]')).not.toBeNull()
    expect(el.querySelector('a[href="/targets/lab"]')?.textContent).toBe('lab')
    expect(el.textContent).toContain('stopped')
    await vi.waitFor(() => expect(el.textContent).toContain('0.25 cores · 96 MB'))
    const east = [...el.querySelectorAll('tbody tr')].find(r => r.textContent?.includes('East office'))!
    // No reading for east: its use is unknown, never zero, and it has no backup.
    expect(east.querySelector('[data-label="Resources"]')?.textContent).toBe('—')
    expect(east.querySelector('[data-label="Last backup"]')?.textContent).toBe('none')
    // The status shows its word; why is behind a tap (or hover), not in the row.
    expect(east.querySelector('.phone-state')?.textContent).toContain('stopped')
    expect(east.textContent).not.toContain('the application is stopped')
    await act(async () => east.querySelector<HTMLButtonElement>('button.chip-button')!.click())
    expect(document.body.querySelector('.status-word-detail')?.textContent).toContain('the application is stopped')
    expect(location.pathname).not.toContain('/deployments/east')
    // A static site holds no data: nothing to back up, not a missing backup.
    expect([...el.querySelectorAll('tbody tr')].find(r => r.textContent?.includes('Website'))!.querySelector('[data-label="Last backup"]')?.textContent).toBe('no data')
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

it('lists releases waiting for approval with their own dates and deployment names', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ departures: [
    { releaseNumber: 7, at: '2026-11-04T07:41:00Z', slug: 'ledger', name: 'Ledger', tier: 'East office deployment', state: 'waiting for approval', changes: [{ id: 'fix', number: 3, title: 'Keep the date' }], commitSha: 'abc123def4567890' },
  ] }))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><Deployments page="releases" /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelector('tbody tr')).not.toBeNull())
    const cells = [...el.querySelectorAll('tbody tr td')].map(td => td.textContent)
    expect(el.querySelector('h1')?.textContent).toBe('All releases')
    expect(cells[0]).toBe('#3 Keep the dateabc123def456')
    expect(cells.indexOf('East office')).toBeLessThan(cells.indexOf('#7'))
    expect(cells).toContain('#7')
    expect(cells).toContain('East office')
    expect(el.textContent).toContain('waiting for approval')
    expect(el.textContent).toContain('4 Nov')
    expect(el.querySelector('a[href="/projects/ledger/releases/7"]')).not.toBeNull()
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

// A failed release's reason can be long; the list shows the word and keeps
// the reason behind it (hover or tap), so the row's own text keeps its width.
it('keeps a release failure reason in the status details, not the row', async () => {
  const reason = 'Stopped before deploying: release refused: the copy taken before this release failed. Deploy again once this is fixed.'
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ departures: [
    { releaseNumber: 2, at: '2026-10-30T00:55:00Z', slug: 'lunch', name: 'Lunch Orders', tier: 'main deployment', state: 'failed', reason },
  ] }))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><Deployments page="releases" /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelector('a[href="/projects/lunch/releases/2"]')?.textContent).toBe('#2'))
    expect(el.textContent).toContain('failed')
    expect(el.textContent).not.toContain(reason)
    expect(el.querySelector('button.chip-button')?.getAttribute('data-help')).toBe(reason)
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

// A backup that was taken stays shown after a refresh fails, but marked as an
// old reading: whether it is still the newest is no longer known.
it('marks the last backup as an old reading when its refresh fails', async () => {
  let backupsFail = false
  vi.stubGlobal('fetch', vi.fn(async (url: string) =>
    url.endsWith('/backups') && backupsFail ? new Response('{"error":"gone"}', { status: 404 })
      : new Response(JSON.stringify(
        url.endsWith('/tiers') ? { tiers: [{ name: 'release', title: 'Released', apps: [app('west', 'West office', 'running', 'working')] }] }
          : url.endsWith('/backups') ? { backups: [{ project: 'ledger', deployment: 'west', takenAt: new Date(Date.now() - 3600_000).toISOString(), status: 'ok' }] }
          : {}))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><Deployments /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelector('[data-label="Last backup"]')?.textContent).toMatch(/\d{2}:\d{2}/))
    expect(el.querySelector('[data-label="Last backup"]')?.textContent).not.toContain('old reading')
    backupsFail = true
    await act(async () => { await client.refetchQueries() })
    await vi.waitFor(() => expect(el.querySelector('[data-label="Last backup"]')?.textContent).toContain('old reading'))
    expect(el.querySelector('[data-label="Last backup"]')?.textContent).toMatch(/\d{2}:\d{2}/)
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

// While deployment backups are switched off the table neither reports nor
// offers them; while the switches cannot be read it still shows them.
it.each([[false, false], [true, true], [undefined, true]] as const)('shows the last backup column only unless backups are known to be off (enabled=%s)', async (enabled, shown) => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    url.endsWith('/features') ? (enabled === undefined ? { error: 'unreadable' } : [{ area: 'backups', enabled }])
      : url.endsWith('/tiers') ? { tiers: [{ name: 'release', title: 'Released', apps: [app('west', 'West office', 'running', 'working')] }] }
      : url.endsWith('/backups') ? { backups: [] } : {}), { status: url.endsWith('/features') && enabled === undefined ? 500 : 200 })))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><Deployments /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelectorAll('tbody tr')).toHaveLength(1))
    await act(async () => { await new Promise(r => setTimeout(r, 50)) })
    expect([...el.querySelectorAll('thead th')].map(th => th.textContent).includes('Last backup')).toBe(shown)
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

// A row offers only acts the person may do (the server refuses the rest):
// someone who may not operate or deploy a project gets no row menu.
it.each([[{}, false], [{ 'deployment:operate': true }, true]] as const)('offers the row menu only to people who may act on the deployment (%o)', async (project, shown) => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    url.includes('/authz/me') ? { instance: {}, project }
      : url.endsWith('/tiers') ? { tiers: [{ name: 'release', title: 'Released', apps: [app('west', 'West office', 'running', 'working')] }] }
      : url.endsWith('/targets/lab/capacity') ? { target: 'lab', kind: 'docker-host', tenants: [] }
      : url.endsWith('/backups') ? { backups: [] } : {}))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><Deployments /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelectorAll('tbody tr')).toHaveLength(1))
    await act(async () => { await new Promise(r => setTimeout(r, 50)) })
    expect(!!el.querySelector('button[aria-label^="More actions"]')).toBe(shown)
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
