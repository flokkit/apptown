// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { getKey, newQueryClient } from '../../data'
import { fmtStamp } from '../../format'
import { settled } from '../../settled'
import { DeploymentDeploys } from './DeploymentDeploys'
import type { DeploymentView } from '../../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const cleanups: (() => void)[] = []
afterEach(() => { for (const clean of cleanups.splice(0)) clean(); vi.unstubAllGlobals() })
const stamp = '2026-11-06T01:00:00Z'
const releases = [
  { status: { word: 'working', reason: '', stale: false }, id: 'r3', number: 3, deployment: 'production', state: 'released', bundleId: 'b3', backupId: 'copy', createdAt: stamp, changes: [], serving: true },
  { id: 'r2', number: 2, deployment: 'staging', state: 'released', bundleId: 'b1', createdAt: stamp, changes: [], serving: true },
  { id: 'r1', number: 1, deployment: 'production', state: 'released', bundleId: 'b1', createdAt: stamp, changes: [{ id: 'c1', number: 1, title: 'Show delivery times' }], serving: false },
]
async function fixture(recorded = true, options: { backup?: boolean; reason?: string; approvalRole?: string; data?: boolean; feed?: string; unknown?: boolean; failed?: boolean; kind?: string } = {}) {
  const sent: { path: string; body: unknown }[] = []
  const fetcher = vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') { sent.push({ path: url, body: JSON.parse(String(init.body)) }); return new Response('{"number":4,"state":"waiting for approval"}', { status: 202 }) }
    if (url.endsWith('/deployments')) return new Response(JSON.stringify([{ name: 'production', displayLabel: 'Customer office', approvalRole: options.approvalRole || '', feed: options.feed || 'manual', target: { kind: options.kind || 'docker-host' } }, { name: 'staging', displayLabel: 'Team trial' }]))
    if (url.endsWith('/protection')) return new Response(JSON.stringify({ items: options.data ? [{ need: 'postgres' }] : [], operations: { copy: { available: true } } }))
    if (url.includes('/authz/me')) return new Response(JSON.stringify({ instance: {}, project: { 'deployment:deploy': true, 'deployment:operate': true } }))
    if (url.endsWith('/release-bundles')) return new Response(JSON.stringify([
      { id: 'b4', contractRevision: 'newest', createdAt: '2026-10-28T01:00:00Z', builtFrom: { changeId: 'c1' } },
      { id: 'b3', contractRevision: 'current', createdAt: stamp },
      { id: 'b1', contractRevision: 'earlier', createdAt: stamp },
    ]))
    if (url.endsWith('/changes/c1')) return new Response(JSON.stringify({ change: { id: 'c1', title: 'Show delivery times' } }))
    if (url.endsWith('/backups')) return new Response(JSON.stringify({ backups: [{ id: 'copy', status: 'ok', runningReleaseId: recorded ? 'r1' : undefined }] }))
    if (url.endsWith('/releases/3')) return new Response(JSON.stringify({ release: releases[0], backup: options.backup === false ? undefined : { id: 'copy', status: 'pruned' }, backupReason: options.reason }))
    if (url.endsWith('/releases')) return new Response(JSON.stringify(options.failed ? [{ id: 'r8', number: 8, deployment: 'production', state: 'failed', bundleId: 'b4', createdAt: stamp, changes: [], failure: 'Provider refused the update' }, ...releases] : options.unknown ? releases.map(r => ({ ...r, serving: false, observed: 'unknown' })) : releases))
    return new Response('{}')
  })
  vi.stubGlobal('fetch', fetcher)
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  document.body.appendChild(el)
  cleanups.push(() => { act(() => root.unmount()); client.clear(); el.remove() })
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><DeploymentDeploys slug="shop" name="production" displayLabel="Customer office" env={{ id: 'production', name: 'production', status: { word: 'working', reason: '', stale: false }, runtimeState: 'running', runtimeActor: 'Priya', runtimeControls: { deploy: '', start: '', stop: '', restart: '' }, runtimeActions: ['start', 'stop', 'restart'], observation: 'running', generation: 0, kind: 'standing', displayLabel: 'Customer office', servingRelease: null, promotionBundleId: null, promotionRelease: 0, promotionSource: '', approvalRole: '', feedRevision: 1, feedConfirmed: true, readinessLabel: 'deployed', restoreLabel: 'unknown', targetId: null, url: '', running: true, releaseRun: null, updatedAt: '', targetName: '', observationStale: false, protected: false } as DeploymentView} current reload={async () => {}} tab="history" /></MemoryRouter></QueryClientProvider>))
  await settled(client)
  // A deployment's less common acts sit behind its ⋯ menu, which opens in a
  // popover: a label not on the page is looked for there.
  const find = (text: string) => [...document.body.querySelectorAll('button')].find(b => b.textContent === text || (!!b.closest('.more-menu-list') && b.firstChild?.textContent === text))
  const click = async (text: string) => {
    if (!find(text)) await act(async () => { el.querySelector<HTMLButtonElement>('button[aria-label^="More actions"]')!.click() })
    await act(async () => { find(text)!.click() }); await settled(client)
  }
  return { el, sent, click, fetcher, client }
}

it('warns before deploying history code and shows the current backup availability', async () => {
  const h = await fixture()
  expect([...h.el.querySelectorAll('button')].filter(b => b.textContent === 'Deploy this again')).toHaveLength(1)
  await h.click('Deploy this again')
  expect(h.el.querySelector('[role=dialog]')?.textContent).toContain("This deploys release 1's code to Customer office. Your data stays as it is now; AppTown does not undo data changes or migrations.")
  expect(h.el.textContent).toContain("AppTown can't tell whether the older code works with today's data. It may run its own start-up steps.")
  expect(h.el.textContent).toContain('Backup taken before release 3: copy · pruned')
  expect(h.el.querySelector('[role=dialog] a')?.getAttribute('href')).toBe('/backups#backup-copy')
  expect(h.el.querySelector('[role=dialog]')?.textContent).not.toMatch(/Needs approval|A backup is taken first|next change that lands|production/)
  expect(h.sent).toEqual([])
  await h.click('Deploy release 1 to Customer office')
  expect(h.sent).toEqual([{ path: '/api/v1/projects/shop/deployments/production/deploy', body: { releaseNumber: 1 } }])
  expect(h.el.textContent).toContain('Release 4 requested')
})

it('lists bundles newest first with deployments and admits a never-deployed bundle to this deployment only', async () => {
  const h = await fixture()
  await h.click('Deploy a release…')
  const dialog = h.el.querySelector('[role=dialog]')!
  expect([...dialog.querySelectorAll('code')].map(c => c.textContent)).toEqual(['newest', 'current', 'earlier'])
  expect(dialog.textContent).toContain('Release 1')
  expect(dialog.textContent).toContain('Show delivery times')
  expect(dialog.textContent).toContain(`built ${fmtStamp('2026-10-28T01:00:00Z')}`)
  expect(dialog.textContent).toContain('Runs on Team trial.')
  expect(dialog.textContent).toContain('Previously ran on Customer office.')
  expect(dialog.textContent).not.toMatch(/Select|production|staging/)
  expect(dialog.textContent).toContain('Has not run on a deployment')
  await act(async () => { dialog.querySelector('button')!.click() }) // close button
  await h.click('Deploy a release…')
  await act(async () => { [...h.el.querySelectorAll('button')].find(b => b.textContent === 'Deploy')!.click() })
  await settled(h.client)
  await h.click('Deploy this bundle to Customer office')
  expect(h.sent[0]).toEqual({ path: '/api/v1/projects/shop/deployments/production/deploy', body: { bundleId: 'b4' } })
})

it.each([true, false])('offers the paired rollback only with recorded backup-time evidence: %s', async recorded => {
  const h = await fixture(recorded)
  expect(h.el.textContent?.includes('Roll back to before release 3')).toBe(recorded)
  if (!recorded) return
  await h.click('Roll back to before release 3')
  expect(h.el.querySelector('[role=dialog]')?.textContent).toContain('the restored data stays and the app stays stopped')
  expect(h.sent).toEqual([])
  await h.click('Roll back to before release 3 on Customer office')
  expect(h.sent[0].body).toEqual({ beforeRelease: 3 })
})

it('keeps the confirmation disabled when current backup evidence cannot be read', async () => {
  const h = await fixture()
  h.client.removeQueries({ queryKey: getKey('/projects/shop/releases/3') })
  h.fetcher.mockImplementation(async () => new Response('{"error":"backup evidence unavailable"}', { status: 403 }))
  await h.click('Deploy this again')
  const button = [...h.el.querySelectorAll('button')].find(b => b.textContent === 'Deploy release 1 to Customer office')!
  expect(button.disabled).toBe(true)
  expect(h.el.textContent).toContain("Not available to you: the current release's backup")
  expect(h.sent).toEqual([])
})

it.each(['no copy was taken before this release: it holds no data', undefined])('explains a missing backup without duplicating the backup sentence: %s', async reason => {
  const h = await fixture(true, { backup: false, reason })
  await h.click('Deploy this again')
  expect(h.el.querySelector('[role=dialog]')?.textContent).toContain(`No backup was taken before release 3: ${reason ? 'it holds no data' : 'the reason was not recorded'}.`)
})

it('shows only the destination approval, backup and follows-main rules that apply', async () => {
  const h = await fixture(true, { approvalRole: 'release-manager', data: true, feed: 'main' })
  await h.click('Deploy this again')
  expect(h.el.textContent).toContain('Needs approval from release-manager.')
  expect(h.el.textContent).toContain('A backup is taken first.')
  expect(h.el.textContent).toContain('The next change that lands deploys over this release.')
})

it('keeps an unobserved current deployment distinct from a past deployment', async () => {
  const h = await fixture(true, { unknown: true })
  await h.click('Deploy a release…')
  const dialog = h.el.querySelector('[role=dialog]')!
  expect(dialog.textContent).toContain('Last deployed on Customer office; current state unknown.')
  const currentBundle = [...dialog.querySelectorAll('.card')].find(card => card.textContent?.includes('Release 3'))!
  expect(currentBundle.textContent).not.toContain('Previously ran on Customer office')
})


it('retries failed bundle code through the normal confirmation and shows its failure in the bundle picker', async () => {
  const h = await fixture(true, { failed: true, approvalRole: 'release-manager', feed: 'main' })
  await h.click('Try again')
  expect(h.el.querySelector('[role=dialog]')?.textContent).toContain('Needs approval from release-manager.')
  await h.click('Deploy release 8 to Customer office')
  expect(h.sent[0]).toEqual({ path: '/api/v1/projects/shop/deployments/production/deploy', body: { releaseNumber: 8 } })
  expect(h.el.textContent).toContain('Release 4 requested')
  await act(async () => { h.el.querySelector<HTMLButtonElement>('[role=dialog] button')!.click() })
  await h.click('Deploy a release…')
  expect(h.el.querySelector('[role=dialog]')?.textContent).toContain('Release 8 failed: Provider refused the update')
  expect(h.el.querySelector('[role=dialog]')?.textContent).toContain('Release 8')
})

it('reads protection for a Fly release and says when its backup goes first', async () => {
  const h = await fixture(true, { kind: 'fly-machines', data: true })
  await h.click('Deploy this again')
  expect(h.fetcher.mock.calls.some(([url]) => url.endsWith('/protection'))).toBe(true)
  expect(h.el.textContent).toContain('A backup is taken first.')
})
