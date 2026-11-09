// @vitest-environment jsdom
import { expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { BackupsView } from './Backups'
import { DeploymentMenu } from './project/DeploymentMenu'
import type { DeploymentView } from '../wire.gen'

vi.mock('../instancePages', () => ({ useHolds: () => true }))
vi.mock('../features', () => ({ useNotOff: () => true }))
vi.mock('../MoreMenu', () => ({ MoreMenu: ({ items }: { items: { label: string }[] }) => <div>{items.map(i => <button key={i.label}>{i.label}</button>)}</div> }))
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function Location() { return <output>{useLocation().pathname}</output> }

// Backing up and restoring are the deployment's acts, on its ⋯ menu: a
// Docker host's and a Portainer environment's data is restored in place;
// a cloud deployment's managed database is backed up but only started from.
it('offers Back up now and Restore on the deployment for every target that restores in place', async () => {
  for (const [kind, restores] of [['docker-host', true], ['portainer', true], ['do-app', false]] as const) {
    const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
    const env = { name: 'prod', displayLabel: 'Production', generation: 1, target: { kind } } as DeploymentView
    try {
      await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><DeploymentMenu slug="shop" env={env} current reload={async () => {}} onDeployRelease={() => {}} /></MemoryRouter></QueryClientProvider>))
      const labels = [...el.querySelectorAll('button')].map(b => b.textContent)
      expect(labels, kind).toContain('Back up now')
      expect(labels.includes('Restore from a backup…'), kind).toBe(restores)
    } finally { act(() => root.unmount()); client.clear() }
  }
})

// A set whose deployment was taken down, of whatever kind (its final
// set, an older manual one, a pruned one), opens the project's
// deployments, never the deployment that is gone; a live deployment's set
// opens that deployment.
it('opens project deployments for every set of a taken-down deployment', async () => {
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  const base = { project: 'shop', deployment: 'old', deploymentLabel: 'old', deploymentRemoved: true, takenAt: '2026-10-08T01:00:00Z', bytes: 10, items: [], store: 'store', path: '/p', statusLabel: 'finished' }
  const body = { where: '/b', keep: 7, backups: [
    { ...base, id: 'live', deployment: 'main', deploymentLabel: 'main', deploymentRemoved: undefined, trigger: 'manual', status: 'ok' },
    { ...base, id: 'final', trigger: 'take-down', status: 'ok' },
    { ...base, id: 'ordinary', trigger: 'manual', status: 'ok' },
    { ...base, id: 'pruned', trigger: 'manual', status: 'pruned' },
  ] }
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith('/backups') ? body : {}))))
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/backups']}><BackupsView /><Location /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelectorAll('table')).toHaveLength(2))
    const rows = [...el.querySelectorAll('table')[1].querySelectorAll('tbody tr')]
    const opened: string[] = []
    for (const row of rows) {
      await act(async () => (row.children[2] as HTMLElement).click())
      opened.push(el.querySelector('output')?.textContent ?? '')
    }
    expect(opened).toEqual(['/projects/shop/deployments/main', '/projects/shop/deployments', '/projects/shop/deployments', '/projects/shop/deployments'])
    expect(el.querySelector('a[href="/projects/shop/deployments/old"]')).toBeNull()
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
