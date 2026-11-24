// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { withFeatures } from '../features.fixture'
import { BackupsView, takenDownWords } from './Backups'
import type { ProjectBackup } from '../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const at = new Date(Date.now() - 6 * 3600e3).toISOString()
const reading = { observedAt: at, attemptedAt: at, waiting: false, stale: false, unknown: false }
const item = (more = {}) => ({ need: 'postgres', where: 'on docker-1', copy: 'fresh', copyLabel: 'fresh', lastCopy: { backup: 'b', takenAt: at, bytes: 18 * 2 ** 20, sha256: '' }, ...more })
const protection = (more = {}) => ({ reading, deployment: 'main', dataClass: 'non-production', offHost: true, keptOn: 'store', target: 'docker-1', items: [item()], recent: [], restoredCopies: [], coverage: { assessed: [], notAssessed: [] }, ...more })
const row = (app: string, more = {}, prot = {}) => ({ project: app.toLowerCase(), projectName: app, deployment: 'main', deploymentLabel: 'main', targetKind: 'docker-host', protection: protection(prot), newest: { backup: 'b', takenAt: at, bytes: 18 * 2 ** 20, sha256: '' }, ...more })
const op = (kind: string, state: string, more = {}) => ({ id: `op-${kind}-${state}`, number: 4, kind, statusLabel: state === 'succeeded' ? 'finished' : state === 'running' ? 'working' : state, state, askedBy: 'sam', input: {}, holds: [], steps: [], result: {}, cancellable: false, createdAt: at, touchedAt: at, startedAt: at, finishedAt: state === 'running' ? undefined : at, ...more })
const store = { bucket: 'acme-backups', endpoint: 'https://s.example', attachedBy: 'sam', attachedAt: at, accessKeyFrom: 'K', secretKeyFrom: 'S', reading }

function Where() { return <output>{useLocation().pathname}{useLocation().search}</output> }

let done = () => {}
afterEach(() => { done(); done = () => {}; vi.unstubAllGlobals() })

// mount draws the Backups page with AppTown answering each read from
// answers, and the viewer holding what holds says.
async function mount(answers: Record<string, unknown>, holds: Record<string, boolean> = { 'deployment:deploy': true, 'platform:manage-deploy-targets': true }, path = '/backups') {
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string) => {
    const u = String(url).replace(/^.*\/api\/v1/, '')
    if (u.startsWith('/authz/me')) return new Response(JSON.stringify({ user: 'sam', instance: holds, project: holds }))
    const key = Object.keys(answers).find(k => u === k)
    return new Response(JSON.stringify(key ? answers[key] : { error: 'AppTown could not read it' }), { status: key ? 200 : 422 })
  })))
  const client = newQueryClient()
  const el = document.createElement('div'), root = createRoot(el)
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><Routes><Route path="*" element={<><BackupsView /><Where /></>} /></Routes></MemoryRouter></QueryClientProvider>))
  done = () => { act(() => root.unmount()); client.clear() }
  return { el, client }
}
const rowsOf = (el: Element) => [...el.querySelectorAll('.deployment-status-rows > .list-row')]
const factsOf = (el: Element) => Object.fromEntries([...el.querySelectorAll('.deployment-status-facts .deploy-line')].map(l => [l.children[0].textContent, l.children[1].textContent]))
const fleet = (rows: unknown[], lastNight = { copied: rows.length, of: rows.length, at }) => ({ deployments: rows, lastNight })

// One row per deployment that holds data, trouble first, each with the
// owner's reason in plain words; Back up now only where it fixes something,
// and only for someone who may deploy there.
it('lists each deployment trouble first, with its reason and Back up now on trouble only', async () => {
  const answers = {
    '/backups/deployments': fleet([
      row('Van'),
      row('Trail', {}, { items: [item({ copy: 'stale', copyLabel: 'overdue', copyNote: 'the last backup did not copy it: docker-1 refused' })] }),
      row('Wish', { newest: null }, { items: [item({ copy: 'none', copyLabel: 'missing', lastCopy: null })] }),
    ]),
    '/backups': { backups: [], keep: 7, where: '/b', retention: 'Keeps the last 7' }, '/backup-store': { store, keptBytes: 4.2 * 2 ** 30 },
  }
  const { el } = await mount(answers)
  await vi.waitFor(() => expect(rowsOf(el)).toHaveLength(3))
  const rows = rowsOf(el)
  expect(rows.map(r => r.querySelector('strong')?.textContent)).toEqual(['Wish main', 'Trail main', 'Van main'])
  expect(rows[0].querySelector('.chip')?.textContent).toContain('no copy yet')
  expect(rows[1].querySelector('.chip')?.textContent).toContain('overdue')
  expect(rows[1].textContent).toContain('docker-1 refused')
  expect(rows[2].querySelector('.chip')?.textContent).toContain('backed up')
  expect(rows[2].textContent).toContain('18 MB')
  await vi.waitFor(() => expect(rows.map(r => !!r.querySelector('button'))).toEqual([true, true, false]))
  // A list, not a log: no limits prose, no store facts, no restore here.
  expect(el.textContent).not.toContain('Limits')
  expect(el.textContent).not.toContain('Endpoint')
  expect([...el.querySelectorAll('button')].some(b => b.textContent?.startsWith('Restore'))).toBe(false)
  const facts = factsOf(el)
  expect(facts.Store).toBe('acme-backups · 4.2 GB')
  expect(facts['Last night']).toMatch(/^3 of 3 copied · \d\d:\d\d$/)
  expect(facts['Last restore']).toBe('never')
})

it('offers no Back up now to someone who may not deploy', async () => {
  const { el, client } = await mount({ '/backups/deployments': fleet([row('Trail', {}, { items: [item({ copy: 'none', copyLabel: 'missing', lastCopy: null })] })]), '/backups': { backups: [] }, '/backup-store': { store } }, {})
  await vi.waitFor(() => expect(rowsOf(el)).toHaveLength(1))
  await vi.waitFor(() => expect(client.isFetching()).toBe(0))
  expect(el.querySelector('button')).toBeNull()
})

// A failed refresh of the list, or an owner reading that is old, is
// unknown: never a green row under a stale note.
it('says unknown, not backed up, when the reading is old or could not be refreshed', async () => {
  const { el } = await mount({ '/backups/deployments': fleet([row('Old', {}, { reading: { ...reading, stale: true } })]), '/backups': { backups: [] }, '/backup-store': { store } })
  await vi.waitFor(() => expect(rowsOf(el)).toHaveLength(1))
  expect(rowsOf(el)[0].querySelector('.chip')?.textContent).toContain('unknown')
  expect(el.querySelector('.chip.pass')).toBeNull()
})

// The work under way, and a last operation that failed or ended unknown,
// is said beside the backups' own state, never hidden by it.
it('keeps operations beside the backup state', async () => {
  const none = { items: [item({ copy: 'none', copyLabel: 'missing', lastCopy: null })] }
  const overdue = { items: [item({ copy: 'stale', copyLabel: 'overdue', copyNote: 'too old' })] }
  const { el } = await mount({ '/backups/deployments': fleet([
    row('Copying', {}, { ...none, running: op('deployment.copy', 'running', { steps: [{ name: 'copying postgres', started: at }] }), recent: [op('deployment.copy', 'running')] }),
    row('Restoring', {}, { ...overdue, running: op('deployment.restore', 'running'), recent: [op('deployment.restore', 'running')] }),
    row('Failed', {}, { recent: [op('deployment.restore', 'failed')] }),
    row('Unknown', {}, { recent: [op('deployment.restore', 'unknown')] }),
  ]), '/backups': { backups: [] }, '/backup-store': { store } })
  await vi.waitFor(() => expect(rowsOf(el)).toHaveLength(4))
  const by = (name: string) => rowsOf(el).find(r => r.querySelector('strong')?.textContent?.startsWith(name))!
  expect(by('Copying').querySelector('.chip')?.textContent).toContain('no copy yet')
  expect(by('Copying').querySelector('a[href="/operations/op-deployment.copy-running"]')?.textContent).toContain('Copy running')
  expect(by('Restoring').querySelector('.chip')?.textContent).toContain('overdue')
  expect(by('Restoring').querySelector('a[href="/operations/op-deployment.restore-running"]')).not.toBeNull()
  expect(by('Failed').querySelector('.chip')?.textContent).toContain('restore failed')
  expect(by('Failed').querySelector('a[href="/operations/op-deployment.restore-failed"]')).not.toBeNull()
  expect(by('Unknown').querySelector('.chip')?.textContent).toContain('restore unknown')
  expect(by('Unknown').querySelector('.chip.pass')).toBeNull()
})

// Without a store, the page says where copies are and offers Attach… to
// an administrator only; the store's facts and the keep rule are on Settings.
it('warns of no store, with Attach… for administrators, and keeps the store on Settings', async () => {
  const answers = { '/backups/deployments': fleet([row('Van', {}, { keptOn: 'host', offHost: false })]), '/backups': { backups: [], retention: 'Keeps the last 7 copies' }, '/backup-store': { store: null } }
  const { el } = await mount(answers)
  await vi.waitFor(() => expect(rowsOf(el)[0]?.textContent).toContain('No backup store'))
  expect(rowsOf(el)[0].textContent).toContain("Copies stay on AppTown's disk and go if it does.")
  await vi.waitFor(() => expect([...el.querySelectorAll('button')].map(b => b.textContent)).toContain('Attach…'))
  done()
  const viewer = await mount(answers, {})
  await vi.waitFor(() => expect(rowsOf(viewer.el)[0]?.textContent).toContain('No backup store'))
  await vi.waitFor(() => expect(viewer.client.isFetching()).toBe(0))
  expect([...viewer.el.querySelectorAll('button')].map(b => b.textContent)).not.toContain('Attach…')
  done()
  const settings = await mount({ ...answers, '/backup-store': { store, keptBytes: 10 } }, undefined, '/backups?tab=settings')
  await vi.waitFor(() => expect(settings.el.textContent).toContain('acme-backups'))
  expect(settings.el.textContent).toContain('https://s.example')
  expect(settings.el.textContent).toContain('K and S')
  await vi.waitFor(() => expect(settings.el.textContent).toContain('Keeps the last 7 copies. A final copy stays with its project.'))
  expect(settings.el.querySelector('a[href="/docs/guides/deployment-backups"]')).not.toBeNull()
})

// The store's kept total is said when known and left out when not, never 0.
it('leaves the kept total out when it is not known', async () => {
  const { el } = await mount({ '/backups/deployments': fleet([row('Van')]), '/backups': { backups: [] }, '/backup-store': { store } })
  await vi.waitFor(() => expect(factsOf(el).Store).toBe('acme-backups'))
})

// A deployment taken down keeps a row that opens its history, and says what
// its newest retained set is, by its trigger and outcome.
it('lists a taken-down deployment that opens its own backups page', async () => {
  const set = { id: 'f', projectId: 'p', project: 'trail', deployment: 'test', deploymentLabel: 'test', deploymentRemoved: true, takenAt: at, trigger: 'take-down', actor: 'sam', path: '', bytes: 10, items: [], status: 'ok', statusLabel: 'finished', store: 'store' }
  const { el } = await mount({ '/backups/deployments': fleet([]), '/backups': { backups: [set] }, '/backup-store': { store } })
  await vi.waitFor(() => expect(rowsOf(el)).toHaveLength(1))
  expect(rowsOf(el)[0].textContent).toContain('final copy')
  expect(rowsOf(el)[0].textContent).toContain('kept with the project')
  await act(async () => (rowsOf(el)[0].querySelector('.list-state') as HTMLElement).click())
  expect(el.querySelector('output')?.textContent).toBe('/projects/trail/deployments/test/backups')
})

it('calls only a take-down set that holds data a final copy', () => {
  const set = (trigger: string, status: string) => ({ trigger, status, takenAt: at, error: 'disk full' }) as ProjectBackup
  expect(takenDownWords(set('take-down', 'ok'))).toMatch(/^final copy .* · kept with the project$/)
  expect(takenDownWords(set('take-down', 'partial'))).toMatch(/^final copy .*, partial · kept with the project$/)
  expect(takenDownWords(set('manual', 'ok'))).toMatch(/^newest copy /)
  expect(takenDownWords(set('manual', 'ok'))).not.toContain('final')
  expect(takenDownWords(set('take-down', 'failed'))).toMatch(/^last copy failed .*: disk full$/)
})

it('says nothing to back up when nothing holds data, and unknown when the list cannot be read', async () => {
  const { el } = await mount({ '/backups/deployments': fleet([]), '/backups': { backups: [] }, '/backup-store': { store } })
  await vi.waitFor(() => expect(el.textContent).toContain('Nothing to back up yet'))
  done()
  const failed = await mount({ '/backups': { backups: [] }, '/backup-store': { store } })
  await vi.waitFor(() => expect(failed.el.textContent).toContain('unknown'))
  expect(failed.el.textContent).not.toContain('Nothing to back up yet')
})

// An unread history of taken-down deployments is not an empty list.
it('says unknown, not nothing to back up, when the taken-down history cannot be read', async () => {
  const { el, client } = await mount({ '/backups/deployments': fleet([]), '/backup-store': { store } })
  await vi.waitFor(() => expect(factsOf(el)['Last restore']).toBe('never'))
  await vi.waitFor(() => expect(client.isFetching()).toBe(0))
  expect(el.textContent).not.toContain('Nothing to back up yet')
  expect(el.textContent).toContain('unknown')
})
