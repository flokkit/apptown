// @vitest-environment jsdom
import { expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient, getKey } from '../data'
import { BackupsView } from './Backups'

// Record ids are UUIDv7 strings.
const SET = '00000000-0012-7000-8000-000000000000'
const PROJECT = '00000000-0001-7000-8000-000000000000'
const DOCKER_SET = '00000000-0020-7000-8000-000000000000'
const CLOUD_SET = '00000000-0021-7000-8000-000000000000'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const set = {
  id: SET, projectId: PROJECT, project: 'shop', deployment: 'main', takenAt: '2026-10-08T01:00:00Z', trigger: 'manual', actor: 'bls', path: '/p', bytes: 10, status: 'ok',
  items: [{ kind: 'need', name: 'redis', file: 'redis.rdb', bytes: 5, sha256: 'a', how: 'save', copy: 'declared' }, { kind: 'need', name: 's3', file: 's3.tar.gz', bytes: 5, sha256: 'b', how: 'tar of /data', copy: 'live-files', caveat: 'object metadata is not in the copy' }],
}

// The Backups page lists copies; acts belong to the deployment (its ⋯
// menu: Back up now, Restore from a backup). A row opens the deployment,
// or the project's deployments when that deployment was taken down.
it('lists copies without acts, and a row opens its deployment', async () => {
  const client = newQueryClient()
  const store = { ...set, id: DOCKER_SET, deploymentLabel: 'Production', store: 'store', path: 'shop/main/x' }
  const final = { ...set, id: CLOUD_SET, deployment: 'old', trigger: 'take-down' }
  const body = { keep: 7, where: '/b', backups: [store, final] }
  client.setQueryData(getKey('/backups'), body)
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(body))))
  const el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><BackupsView /></MemoryRouter></QueryClientProvider>) })
    for (const label of ['Back up now', 'Restore main from this copy', 'Start a new deployment from this'])
      expect([...el.querySelectorAll('button, a')].some(b => b.textContent === label)).toBe(false)
    expect(el.querySelector('input[placeholder="a deployment"]')).toBeNull()
    expect([...el.querySelectorAll('th')].map(th => th.textContent)).toContain('Details')
    // Where each set is kept, by its recorded place and path: a folder a
    // stopped move left behind is matched against it (OHB-02).
    const all = el.querySelectorAll('table')[1].querySelectorAll('tbody tr')
    expect(all[0].textContent).toContain('backup store')
    // The cleanup guide requires the exact folder identity to be readable.
    // A display label does not supply the path's deployment segment, and
    // a native title tooltip is unavailable to a person using a phone.
    expect(all[0].textContent).toContain('shop/main/x')
    expect(el.textContent).toContain('object metadata is not in the copy')
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

it('labels only an explicitly associated set before its numbered release', async () => {
  const client = newQueryClient()
  const body = { keep: 7, where: '/b', backups: [{ ...set, trigger: 'pre-release', beforeRelease: 3 }] }
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith('/backups') ? body : {}))))
  const el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/backups#backup-${SET}`]}><BackupsView /></MemoryRouter></QueryClientProvider>) })
    await vi.waitFor(() => expect(el.textContent).toContain('before release 3'))
    expect(el.querySelector(`a[href="/projects/shop/releases/3"]`)).not.toBeNull()
    expect(el.querySelector(`[id="backup-${SET}"]`)).not.toBeNull()
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

// On a phone the details cell already names the deployment and shares its
// place, so the Deployment column is a desktop column only; on a phone the
// two would paint over each other.
it('shows the Deployment column on wide screens only', async () => {
  const client = newQueryClient()
  const body = { keep: 7, where: '/b', backups: [set] }
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith('/backups') ? body : {}))))
  const el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><BackupsView /></MemoryRouter></QueryClientProvider>) })
    await vi.waitFor(() => expect(el.querySelectorAll('table').length).toBe(2))
    for (const table of el.querySelectorAll('table')) {
      const heads = [...table.querySelectorAll('th')]
      const deployment = heads.findIndex(th => th.textContent === 'Deployment')
      expect(heads[deployment].classList.contains('record-wide')).toBe(true)
      for (const row of table.querySelectorAll('tbody tr')) expect(row.children[deployment].classList.contains('record-wide')).toBe(true)
    }
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

// The heading counts copies kept; sets the keep policy removed are listed for
// their record and counted apart, so the count matches the policy.
it('counts kept copies in the heading and removed sets apart', async () => {
  const client = newQueryClient()
  const kept = { ...set, statusLabel: 'finished' }
  const removed = { ...set, id: DOCKER_SET, status: 'pruned', statusLabel: 'removed' }
  const body = { keep: 7, where: '/b', backups: [kept, removed, { ...removed, id: CLOUD_SET }] }
  client.setQueryData(getKey('/backups'), body)
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(body))))
  const el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><BackupsView /></MemoryRouter></QueryClientProvider>) })
    const all = [...el.querySelectorAll('section')].find(s => s.querySelector('h2')?.textContent?.startsWith('All backups'))!
    expect(all.querySelector('.section-count')?.textContent).toBe('1')
    expect(all.textContent).toContain('2 removed by the keep policy')
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

// A copy of a running app's files may hold a file caught half-written. The
// set still finished, so its status stays; the item says why in words.
it('says why a file copy of a running app may be inconsistent', async () => {
  const client = newQueryClient()
  const volume = { ...set, items: [{ kind: 'volume', name: 'shop_data', file: 'shop_data.tar.gz', bytes: 5, sha256: 'c', how: 'tar of the volume shop_data', copy: 'live-files' }] }
  const body = { keep: 7, where: '/b', backups: [volume] }
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith('/backups') ? body : {}))))
  const el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><BackupsView /></MemoryRouter></QueryClientProvider>) })
    await vi.waitFor(() => expect(el.querySelectorAll('table').length).toBe(2))
    const latest = el.querySelector('table')!.textContent ?? ''
    expect(latest).toContain('file copy, app running')
    expect(latest).toContain('Copied while the app was running: a file it was writing at that moment may be incomplete in this copy.')
    expect(latest).not.toContain('may be inconsistent')
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
