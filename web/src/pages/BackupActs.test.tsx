// @vitest-environment jsdom
import { expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { DeploymentMenu } from './project/DeploymentMenu'
import type { DeploymentView } from '../wire.gen'

vi.mock('../instancePages', () => ({ useHolds: () => true }))
vi.mock('../features', () => ({ useNotOff: () => true }))
vi.mock('../MoreMenu', () => ({ MoreMenu: ({ items }: { items: { label: string; onClick: () => void }[] }) => <div>{items.map(i => <button key={i.label} onClick={i.onClick}>{i.label}</button>)}</div> }))
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function Location() { return <output>{useLocation().pathname}{useLocation().hash}</output> }

// Back up now stays on the deployment's ⋯ menu for every target with its
// own way to copy; restoring is Restore… on a copy on the deployment's
// backups page, not a menu dialog.
it('offers Back up now on the menu, and no restore dialog', async () => {
  for (const kind of ['docker-host', 'portainer', 'do-app']) {
    const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
    const env = { name: 'prod', displayLabel: 'Production', generation: 1, target: { kind } } as DeploymentView
    try {
      await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><DeploymentMenu slug="shop" env={env} current reload={async () => {}} onDeployRelease={() => {}} /></MemoryRouter></QueryClientProvider>))
      const labels = [...el.querySelectorAll('button')].map(b => b.textContent)
      expect(labels, kind).toContain('Back up now')
      expect(labels.some(l => l?.startsWith('Restore')), kind).toBe(false)
    } finally { act(() => root.unmount()); client.clear() }
  }
})

// After a Docker host's backup, and from the menu's Backups, the
// deployment's own backups page opens: at the new copy after a backup.
it('opens the deployment’s backups page, at the new copy after Back up now', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ ok: true, backup: { id: 'new-set' } }))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  const env = { name: 'prod', displayLabel: 'Production', generation: 1, target: { kind: 'docker-host' } } as DeploymentView
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><DeploymentMenu slug="shop" env={env} current reload={async () => {}} onDeployRelease={() => {}} /><Location /></MemoryRouter></QueryClientProvider>))
    await act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Backups')!.click())
    expect(el.querySelector('output')?.textContent).toBe('/projects/shop/deployments/prod/backups')
    await act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Back up now')!.click())
    await vi.waitFor(() => expect(el.querySelector('output')?.textContent).toBe('/projects/shop/deployments/prod/backups#backup-new-set'))
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
