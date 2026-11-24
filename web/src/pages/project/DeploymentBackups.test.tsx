// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../../data'
import { withFeatures } from '../../features.fixture'
import { DeploymentBackupsPage } from './DeploymentBackups'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const hoursAgo = (h: number) => new Date(Date.now() - h * 3600e3).toISOString()
const at = hoursAgo(6)
const reading = { observedAt: at, attemptedAt: at, waiting: false, stale: false, unknown: false }
const env = (more = {}) => ({ name: 'main', displayLabel: 'main', kind: 'standing', generation: 3, backups: true, target: { kind: 'docker-host', name: 'docker-1' }, ...more })
const dockerOps = { copy: { available: false, reason: 'backups-page', why: 'backed up nightly' }, check: { available: false, reason: 'not-on-this-platform', why: 'Checks run on App Platform only.' }, restore: { available: false, reason: 'not-on-this-platform', why: 'App Platform only.' }, rehearsal: { available: true }, startFromCopy: { available: true }, finalCopy: { available: true } }
const item = (more = {}) => ({ need: 'postgres', where: 'on docker-1', copy: 'fresh', copyLabel: 'fresh', lastCopy: { backup: 's1', takenAt: at, bytes: 2048, sha256: '' }, ...more })
const protection = (more = {}) => ({ reading, deployment: 'main', dataClass: 'non-production', offHost: true, keptOn: 'store', target: 'docker-1', items: [item()], recent: [], restoredCopies: [], operations: dockerOps, rehearsalNote: 'never rehearsed', coverage: { assessed: ['postgres'], notAssessed: [] }, ...more })
const need = (more = {}) => ({ kind: 'need', name: 'postgres', file: 'p.dump', bytes: 2048, sha256: 'a', how: 'pg_dump', copy: 'declared', format: 'pg-custom', ...more })
const set = (id: string, h: number, more = {}) => ({ id, projectId: 'p', project: 'shop', deployment: 'main', deploymentLabel: 'main', takenAt: hoursAgo(h), trigger: 'scheduled', actor: 'apptown-system', path: `shop/main/${id}`, bytes: 2048, items: [need()], status: 'ok', statusLabel: 'finished', store: 'store', ...more })
const op = (kind: string, state: string) => ({ id: `op-${state}`, number: 9, kind, statusLabel: state === 'succeeded' ? 'finished' : state === 'running' ? 'working' : state, state, askedBy: 'sam', input: {}, holds: [], steps: [], result: {}, cancellable: false, createdAt: at, touchedAt: at, startedAt: at, finishedAt: state === 'running' ? undefined : at })

function Where() { return <output>{useLocation().pathname}</output> }

let done = () => {}
afterEach(() => { done(); done = () => {}; vi.unstubAllGlobals() })

type Answers = { setsError?: boolean; list?: unknown; protection?: unknown; sets?: unknown[]; all?: unknown[]; finals?: unknown[]; store?: unknown }
// mount draws one deployment's backups page with AppTown answering its
// reads, the viewer holding holds, and each POST recorded and admitted.
async function mount(a: Answers, holds: Record<string, boolean> = { 'deployment:deploy': true, 'platform:manage-deploy-targets': true }) {
  const posts: { url: string; body: unknown }[] = []
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string, init?: RequestInit) => {
    const u = String(url).replace(/^.*\/api\/v1/, '')
    if (init?.method === 'POST') { posts.push({ url: u, body: JSON.parse(String(init.body ?? '{}')) }); return new Response(JSON.stringify({ operationId: 'op-new', state: 'queued', url: '' }), { status: 202 }) }
    const json = (b: unknown) => new Response(JSON.stringify(b))
    if (u.startsWith('/authz/me')) return json({ user: 'sam', instance: holds, project: holds })
    if (u === '/projects/shop/deployments') return json(a.list ?? [env()])
    if (u === '/projects/shop/deployments/main/protection' || u === '/projects/shop/deployments/test/protection') return json(a.protection ?? protection())
    if (a.setsError && u.endsWith('/backups') && u.startsWith('/projects/shop/deployments/')) return new Response(JSON.stringify({ error: 'history unavailable' }), { status: 503 })
    if (u.endsWith('/backups') && u.startsWith('/projects/shop/deployments/')) return json({ backups: a.sets ?? [], plan: [], note: '', keep: 7 })
    if (u === '/backups') return json({ backups: a.all ?? [], keep: 7, where: '/data/backups', retention: '' })
    if (u === '/projects/shop/final-copies') return json({ copies: a.finals ?? [] })
    if (u === '/backup-store') return json(a.store ?? { store: { bucket: 'acme-backups' } })
    return new Response(JSON.stringify({ error: 'not answered' }), { status: 422 })
  })))
  const client = newQueryClient()
  const el = document.createElement('div'), root = createRoot(el)
  const name = (a.list as { name: string }[] | undefined)?.length === 0 ? 'test' : 'main'
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/projects/shop/deployments/${name}/backups`]}>
    <Routes><Route path="/projects/:slug/deployments/:name/backups" element={<DeploymentBackupsPage />} /><Route path="*" element={<Where />} /></Routes></MemoryRouter></QueryClientProvider>))
  done = () => { act(() => root.unmount()); client.clear() }
  return { el, client, posts }
}
const rows = (el: Element) => [...el.querySelectorAll('.deployment-status-rows > .list-row')]
const button = (el: Element, label: string) => [...el.querySelectorAll('button')].find(b => b.textContent === label)
const facts = (el: Element) => Object.fromEntries([...el.querySelectorAll('.deployment-status-facts .deploy-line')].map(l => [l.children[0].textContent, l.children[1].textContent]))
const settle = async (client: ReturnType<typeof newQueryClient>) => vi.waitFor(() => expect(client.isFetching()).toBe(0))

// Restore… is on every copy that holds data: it asks in place, says what
// the copy does not hold, and on Yes asks AppTown to restore that copy, then
// opens the operation.
it('restores a copy from its own row, asking in place first', async () => {
  const bucket = need({ name: 'photos', copy: 'declared', format: undefined, caveat: 'access rules are not copied' })
  const { el, posts } = await mount({ sets: [set('s1', 6, { items: [need(), bucket] }), set('s2', 30), set('s3', 54, { status: 'failed', statusLabel: 'failed', error: 'disk full', items: [] })] })
  await vi.waitFor(() => expect(el.querySelectorAll('button')).not.toHaveLength(0))
  await vi.waitFor(() => expect(rows(el).filter(r => button(r, 'Restore…'))).toHaveLength(2))
  await act(async () => button(rows(el).find(r => button(r, 'Restore…'))!, 'Restore…')!.click())
  expect(el.textContent).toContain('Restore main to ')
  expect(el.textContent).toContain('What is there now is copied first; the app stops while its data is put back.')
  expect(el.textContent).toContain('photos: access rules are not copied')
  await act(async () => button(el, 'Restore')!.click())
  expect(posts).toEqual([{ url: '/projects/shop/deployments/main/backups/s1/restore', body: { confirm: 'main' } }])
  await vi.waitFor(() => expect(el.querySelector('output')?.textContent).toBe('/operations/op-new'))
})

it('offers no restore without deploy rights, on a managed copy, or while an operation runs', async () => {
  const cases: [Answers, Record<string, boolean>][] = [
    [{ sets: [set('s1', 6)] }, {}],
    [{ sets: [set('s1', 6, { items: [need({ instance: 'db-1' })] })] }, { 'deployment:deploy': true }],
    [{ sets: [set('s1', 6)], protection: protection({ running: op('deployment.copy', 'running'), recent: [op('deployment.copy', 'running')] }) }, { 'deployment:deploy': true }],
    [{ sets: [set('s1', 6)], list: [env({ target: { kind: 'do-app', name: 'do' } })] }, { 'deployment:deploy': true }],
  ]
  for (const [answers, holds] of cases) {
    const { el, client } = await mount(answers, holds)
    await vi.waitFor(() => expect(rows(el).some(r => r.textContent?.includes('nightly'))).toBe(true))
    await settle(client)
    expect(button(el, 'Restore…')).toBeUndefined()
    done(); done = () => {}
  }
})

// A copy that holds its data carries no status word; only unusual copies
// do. Every copy is listed: the keep rule caps how many there are.
it('lists every copy, with a status only on unusual ones', async () => {
  const sets = Array.from({ length: 20 }, (_, i) => set(`k${i}`, 6 + 24 * i))
  sets.push(set('p1', 500, { status: 'partial', statusLabel: 'failed', error: 'one item' }))
  const { el } = await mount({ sets })
  await vi.waitFor(() => expect(rows(el).filter(r => r.textContent?.includes('nightly'))).toHaveLength(21))
  expect(rows(el).filter(r => r.querySelector('.chip')).map(r => r.querySelector('.chip')?.textContent)).toEqual(['● partial'])
  expect([...el.querySelectorAll('button')].some(b => b.textContent?.startsWith('Show'))).toBe(false)
})

// Fourteen nights at a glance, and the copies under them: a run of
// failures with one error, or of copies the keep rule removed, is one row.
it('marks each night and folds runs of failures and removals into one row', async () => {
  const fail = { status: 'failed', statusLabel: 'failed', error: 'docker-1 refused', items: [] }
  const gone = { status: 'pruned', statusLabel: 'removed' }
  const { el } = await mount({ sets: [set('f1', 0.5, fail), set('f2', 24.5, fail), set('f3', 48.5, fail), set('k1', 72.5), set('r1', 96.5, gone), set('r2', 120.5, gone)] })
  await vi.waitFor(() => expect(rows(el).length).toBeGreaterThan(2))
  const texts = rows(el).map(r => r.textContent ?? '')
  expect(texts.some(t => t.includes('3 failed') && t.includes('docker-1 refused'))).toBe(true)
  expect(texts.some(t => t.includes('2 removed') && t.includes('by the keep rule'))).toBe(true)
  const marks = [...el.querySelectorAll('.backup-nights td a')].map(a => a.textContent)
  expect(marks.slice(0, 6)).toEqual(['✗', '✗', '✗', '✓', '·', '·'])
  expect(el.querySelector('.backup-nights a[href="#backup-k1"]')).not.toBeNull()
})

// Each item keeps its own account (its state, why, newest copy and size),
// and its copy's limits are said once, on its line.
it('says each item’s own newest copy and its limits once', async () => {
  const live = { kind: 'volume', name: 'shop_data', volume: 'shop_data', file: 'd.tar', bytes: 10, sha256: 'b', how: 'tar', copy: 'live-files' }
  const p = protection({ items: [item(), item({ need: 'data folder', volume: 'shop_data', copy: 'stale', copyLabel: 'overdue', copyNote: 'the last backup did not copy it: busy', lastCopy: { backup: 's2', takenAt: hoursAgo(30), bytes: 5 * 2 ** 20, sha256: '' } })] })
  const { el } = await mount({ protection: p, sets: [set('s1', 6, { items: [need(), { ...live, error: 'busy' }] }), set('s2', 30, { items: [need(), live] })] })
  await vi.waitFor(() => expect(facts(el)['data folder']).toBeDefined())
  expect(facts(el).postgres).toContain('2 KB')
  expect(facts(el).postgres).toContain('database copy')
  expect(facts(el)['data folder']).toContain('5 MB')
  expect(facts(el)['data folder']).toContain('overdue')
  expect(el.textContent?.match(/may be incomplete in this copy/g)).toBeNull()
  expect(el.textContent).toContain('busy')
})

it('says "never" for an untested restore only of production data', async () => {
  const one = await mount({ sets: [set('s1', 6)], protection: protection({ dataClass: 'production' }) })
  await vi.waitFor(() => expect(facts(one.el)['Restore test']).toBe('never'))
  done(); done = () => {}
  const other = await mount({ sets: [set('s1', 6)] })
  await vi.waitFor(() => expect(facts(other.el).Covers).toBeDefined())
  expect(other.el.textContent).not.toContain('never')
})

// A good backup state never hides an operation that went wrong, or one
// under way; a restore that worked says so.
describe('operations beside the backups', () => {
  for (const [state, word] of [['failed', 'failed'], ['unknown', 'unknown'], ['succeeded', 'restored']] as const) {
    it(`shows the last restore that ${state}`, async () => {
      const { el } = await mount({ sets: [set('s1', 6)], protection: protection({ recent: [op('deployment.restore', state)] }) })
      await vi.waitFor(() => expect(el.querySelector('a[href="/operations/op-' + state + '"]')).not.toBeNull())
      const row = rows(el).find(r => r.querySelector('a[href="/operations/op-' + state + '"]'))!
      expect(row.querySelector('.chip')?.textContent).toContain(word)
    })
  }
  it('shows a copy under way beside a missing copy', async () => {
    const running = op('deployment.copy', 'running')
    const { el } = await mount({ sets: [], protection: protection({ items: [item({ copy: 'none', copyLabel: 'missing', lastCopy: null })], running, recent: [running] }) })
    await vi.waitFor(() => expect(rows(el).length).toBeGreaterThan(1))
    expect(rows(el)[0].querySelector('.chip')?.textContent).toContain('no copy yet')
    expect(rows(el).some(r => r.querySelector('a[href="/operations/op-running"]'))).toBe(true)
  })
})

// A deployment taken down keeps its history, read-only: its newest set is
// described as recorded, and starting from it is offered only when the
// owner lists it as a final copy to start from.
describe('a taken-down deployment', () => {
  const tdSet = (id: string, h: number, more = {}) => set(id, h, { deployment: 'test', deploymentLabel: 'test', deploymentRemoved: true, ...more })
  it('lists its sets read-only, with the start-from-copy link for its final copy', async () => {
    const { el, client } = await mount({ list: [], sets: [tdSet('fin', 6, { trigger: 'take-down' }), tdSet('o1', 30)], finals: [{ backup: 'fin', deployment: 'test', takenAt: at, bytes: 1, dataClass: 'x', verified: false }] }, {})
    await vi.waitFor(() => expect(rows(el)[0]?.textContent).toContain('final copy'))
    await settle(client)
    expect(rows(el).some(r => r.textContent?.includes('final copy, taken down'))).toBe(true)
    expect(rows(el).some(r => r.textContent?.includes('nightly'))).toBe(true)
    expect(button(el, 'Restore…')).toBeUndefined()
    expect(button(el, 'Back up now')).toBeUndefined()
    await vi.waitFor(() => expect(el.querySelector('a[href="/projects/shop/deployments"]')?.textContent).toBe('Start a new deployment from it'))
  })
  it('never calls an ordinary set final, nor offers to start from it', async () => {
    const { el, client } = await mount({ list: [], sets: [tdSet('o1', 6, { trigger: 'manual' })] })
    await vi.waitFor(() => expect(rows(el)[0]?.textContent).toContain('newest copy'))
    await settle(client)
    expect(rows(el)[0].textContent).not.toContain('final')
    expect(el.querySelector('a[href="/projects/shop/deployments"]')).toBeNull()
  })
  it('shows a failed last set as the failure it was', async () => {
    const { el, client } = await mount({ list: [], sets: [tdSet('x1', 6, { status: 'failed', statusLabel: 'failed', error: 'disk full', items: [] })] })
    await vi.waitFor(() => expect(rows(el)[0]?.textContent).toContain('last copy failed'))
    await settle(client)
    expect(rows(el)[0].textContent).toContain('disk full')
    expect(el.textContent).not.toContain('kept with the project')
    expect(el.querySelector('a[href="/projects/shop/deployments"]')).toBeNull()
  })
})

// The doors to check a copy or test a restore follow AppTown's answer for
// each: greyed with its reason when it cannot start; the target's settings
// only for those who can edit targets. A Docker host shows only what applies
// there.
describe('the doors on the facts card', () => {
  const why = 'Backups and restore tests need a copy runner.'
  const no = { available: false, reason: 'no-copy-runner', why }
  const cloud = (ops: unknown) => protection({ dataClass: 'production', target: 'do-syd', keptOn: undefined, operations: ops })
  const all = (a: unknown) => ({ copy: a, check: a, restore: a, rehearsal: { available: false, reason: 'not-on-this-platform' }, startFromCopy: a, finalCopy: a })
  const doors = ['Back up now', 'Check it comes back', 'Test a restore here']
  it('greys them with AppTown’s reason, and links the target settings for those who edit targets', async () => {
    const { el } = await mount({ sets: [set('s1', 6, { items: [need({ instance: 'db' })] })], list: [env({ target: { kind: 'do-app', name: 'do-syd' } })], protection: cloud(all(no)) })
    await vi.waitFor(() => expect(button(el, 'Check it comes back')).toBeDefined())
    await vi.waitFor(() => expect(button(el, 'Back up now')).toBeDefined())
    for (const label of doors) expect(button(el, label)?.disabled, label).toBe(true)
    expect(el.textContent).toContain(why)
    await vi.waitFor(() => expect(el.querySelector('a[href="/targets/do-syd"]')).not.toBeNull())
    done(); done = () => {}
    const viewer = await mount({ sets: [set('s1', 6)], list: [env({ target: { kind: 'do-app', name: 'do-syd' } })], protection: cloud(all(no)) }, { 'deployment:deploy': true })
    await vi.waitFor(() => expect(viewer.el.textContent).toContain(why))
    await settle(viewer.client)
    expect(viewer.el.querySelector('a[href="/targets/do-syd"]')).toBeNull()
  })
  it('leaves them usable when they can start', async () => {
    const { el } = await mount({ sets: [set('s1', 6)], list: [env({ target: { kind: 'do-app', name: 'do-syd' } })], protection: cloud(all({ available: true })) })
    await vi.waitFor(() => expect(button(el, 'Back up now')).toBeDefined())
    for (const label of doors) expect(button(el, label)?.disabled, label).toBe(false)
  })
  it('shows only the rehearsal on a Docker host, and says where copies are kept', async () => {
    const { el } = await mount({ sets: [set('s1', 6)], protection: protection({ keptOn: 'host', offHost: false, note: "Backups are kept on AppTown's disk only: they do not outlive it.", operations: { ...dockerOps, rehearsal: { available: false, reason: 'no-copy', why: 'There is no good backup of its data yet.' } } }) })
    await vi.waitFor(() => expect(button(el, 'Test a restore')).toBeDefined())
    expect(button(el, 'Check it comes back')).toBeUndefined()
    expect(button(el, 'Test a restore here')).toBeUndefined()
    expect(el.textContent).not.toContain('App Platform only')
    expect(el.textContent).not.toContain('never checked to come back')
    expect(facts(el).Kept).toBe("AppTown's disk")
    expect(el.textContent).not.toContain('this host')
  })
  it('says the missing copy once, with the restore test greyed and pointing at it', async () => {
    const why = 'There is no good backup of its data yet.'
    const { el } = await mount({ sets: [], protection: protection({ items: [item({ copy: 'none', copyLabel: 'missing', lastCopy: null })], operations: { ...dockerOps, rehearsal: { available: false, reason: 'no-copy', why } } }) })
    const test = await vi.waitFor(() => { const b = button(el, 'Test a restore'); expect(b).toBeDefined(); return b! })
    expect(test.disabled).toBe(true)
    expect(test.title).toBe(why)
    expect(el.querySelector(`[id="${test.getAttribute('aria-describedby')}"]`)?.textContent).toContain('No backup yet')
    expect(el.textContent?.match(/There is no good backup/g)).toBeNull()
  })
  it('says unknown when what the deployment holds could not be read', async () => {
    const { el } = await mount({ sets: [], protection: protection({ items: [], operations: undefined, unknown: 'no contract: bad yaml', reading: { ...reading, unknown: true } }) })
    await vi.waitFor(() => expect(facts(el).Holds).toBeDefined())
    expect(facts(el).Holds).toContain('unknown')
    expect(facts(el).Holds).toContain('no contract: bad yaml')
    expect(facts(el).Covers).toContain('unknown')
  })
})

// The copy history and each item's failure are evidence in their own right.
it('says unknown when the deployment’s copy history cannot be read', async () => {
  const { el, client } = await mount({ setsError: true })
  await vi.waitFor(() => expect(facts(el).Covers).toBeDefined())
  await settle(client)
  expect(el.textContent).toContain('unknown')
})

it('keeps each failed item’s own reason on its copy', async () => {
  const { el, client } = await mount({ sets: [set('good', 6), set('partial', 30, {
    status: 'partial', statusLabel: 'failed', error: '',
    items: [need(), need({ name: 'photos', error: 'bucket access was denied' })],
  })] })
  await vi.waitFor(() => expect(el.textContent).toContain('photos not copied'))
  await settle(client)
  expect(el.textContent).toContain('bucket access was denied')
})

it('opens each night’s copy row when failures are grouped', async () => {
  const failure = { status: 'failed', statusLabel: 'failed', error: 'host refused', items: [] }
  const { el } = await mount({ sets: [set('f1', 0.5, failure), set('f2', 24.5, failure)] })
  await vi.waitFor(() => expect(el.querySelector('a[href="#backup-f2"]')).not.toBeNull())
  expect(el.querySelector('[id="backup-f2"]')).not.toBeNull()
})
