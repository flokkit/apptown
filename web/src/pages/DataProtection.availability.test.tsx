// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { ProtectionLines } from './DataProtection'

// Record ids are UUIDv7 strings.
const SET = '00000000-0002-7000-8000-000000000000'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const why = "Backups and restore tests need a copy runner: a Docker host that runs the copy commands. An administrator can choose one in the target's settings."
const yes = { available: true }
const no = { available: false, reason: 'no-copy-runner', why }
const outside = 'data the app keeps outside the needs it declares, such as a bucket or a database it reaches with its own settings'
const coverage = { assessed: ['postgres · database app on forge-ledger-prod'], notAssessed: [outside] }
const all = (a: unknown) => ({ copy: a, check: a, restore: a, rehearsal: a, startFromCopy: a, finalCopy: a })
const protection = (operations: unknown) => ({
  deployment: 'prod', dataClass: 'production', offHost: false, target: 'do-syd', recent: [], running: null, restoredCopies: [], operations, coverage,
  items: [{ need: 'postgres', where: 'database app on forge-ledger-prod', copy: 'none' }],
})

// mount shows a deployment's card for `deployment` with the forge answering
// `body` for its protection and `instance` for the viewer's holdings. Each
// test gets its own client; afterEach ends it and restores fetch.
let done = () => {}
afterEach(() => { done(); done = () => {}; vi.unstubAllGlobals() })
async function mount(body: unknown, instance: Record<string, boolean> = {}, deployment = { slug: 'ledger', name: 'prod' }) {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    String(url).includes('/authz/me') ? { user: 'bls', instance } : body), { status: 200 })))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><ProtectionLines slug={deployment.slug} name={deployment.name} /></MemoryRouter></QueryClientProvider>) })
  done = () => { act(() => root.unmount()); client.clear() }
  return { el, client }
}
// settled waits until every read the card started, its holdings included,
// has its answer, then for the zero-delay tick on which the query client
// hands answers to the page.
async function settled(client: ReturnType<typeof newQueryClient>) {
  await vi.waitFor(() => {
    expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).includes('/authz/me'))).toBe(true)
    expect(client.isFetching()).toBe(0)
  })
  await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
}
const button = (el: HTMLElement, label: string) => Array.from(el.querySelectorAll('button')).find((b) => b.textContent === label)
const doors = ['Copy now', 'Check it comes back', 'Test a restore here', 'Restore a copy to look at']

// The card's copy, check and restore buttons follow the forge's own answer
// of whether they can start: greyed with its sentence when they cannot (no
// copy runner on the target), with the way to the target's settings only
// for those who can edit targets; usable when they can.
describe('the data operations on a deployment card', () => {
  const card = async (body: unknown, editsTargets: boolean) => {
    const m = await mount(body, { 'platform:manage-deploy-targets': editsTargets })
    await vi.waitFor(() => expect(m.el.textContent).toContain('Copy now'))
    return m
  }

  it('greys them with the forge’s reason when the target has no copy runner', async () => {
    const { el } = await card(protection(all(no)), true)
    for (const label of doors) expect(button(el, label)?.disabled, label).toBe(true)
    expect(el.textContent).toContain(why)
    await vi.waitFor(() => expect(el.querySelector('a[href="/infrastructure/do-syd"]')).not.toBeNull())
  })
  it('offers the target settings only to those who can edit targets', async () => {
    const { el, client } = await card(protection(all(no)), false)
    expect(el.textContent).toContain(why)
    // Let the holdings answer arrive before saying the link is absent.
    await settled(client)
    expect(el.querySelector('a[href="/infrastructure/do-syd"]')).toBeNull()
  })
  it('leaves them usable, with no reason, when they can start', async () => {
    const { el } = await card(protection(all(yes)), true)
    for (const label of doors) expect(button(el, label)?.disabled, label).toBe(false)
    expect(el.textContent).not.toContain('copy runner')
  })
})

// Each button follows its own door's answer, not one shared flag: before
// the first copy a copy can start but a check cannot, and a rehearsal runs
// without the source's copy runner (review CR-02).
describe('each data operation follows its own answer', () => {
  it('greys only the operations that cannot start, with their own reason, before the first copy', async () => {
    const noCopy = { available: false, reason: 'no-copy', why: 'There is no good copy of its data in the backup store yet: copy it first.' }
    const { el } = await mount({ ...protection({ ...all(yes), check: noCopy, restore: noCopy }), rehearsalNote: 'never rehearsed' })
    await vi.waitFor(() => expect(el.textContent).toContain('Copy now'))
    expect(button(el, 'Copy now')?.disabled).toBe(false)
    expect(button(el, 'Check it comes back')?.disabled).toBe(true)
    expect(button(el, 'Test a restore here')?.disabled).toBe(true)
    expect(button(el, 'Rehearse a restore')?.disabled).toBe(false)
    expect(el.textContent).toContain(noCopy.why)
    expect(el.textContent).not.toContain(why)
  })
})

const docker = { slug: 'shelf', name: 'main' }

// A Docker host's deployment: the card says calmly where its backups are,
// shows only the doors that apply on its platform (a rehearsal), and no
// check line, since a check runs only on App Platform.
describe('a deployment on a Docker host', () => {
  it('shows only what applies there, and its note as a fact, not an alarm', async () => {
    const elsewhere = (why: string) => ({ available: false, reason: 'not-on-this-platform', why })
    const onDisk = { available: false, reason: 'on-apptown-disk', why: 'Rehearsals read copies from a backup store, and none is attached.' }
    const body = {
      deployment: 'main', dataClass: 'production', offHost: false, target: 'home', recent: [], running: null, restoredCopies: [], coverage,
      keptOn: 'host', note: "Backups are kept on the forge's own disk.", rehearsalNote: "not available while its backups are only on the forge's own disk",
      operations: { copy: { available: false, reason: 'backups-page', why: 'backed up daily' }, check: elsewhere('check: App Platform only'), restore: elsewhere('restore: App Platform only'), rehearsal: onDisk, startFromCopy: elsewhere('x'), finalCopy: yes },
      items: [{ need: 'postgres', where: 'on home', copy: 'fresh', copyNote: "kept on the forge's own disk", lastCopy: { backup: SET, takenAt: '2026-10-08T01:00:00Z', bytes: 40, sha256: 'a' } }],
    }
    const { el } = await mount(body, {}, docker)
    await vi.waitFor(() => expect(el.textContent).toContain('Rehearse a restore'))
    for (const label of doors) expect(button(el, label), label).toBeUndefined()
    expect(el.textContent).not.toContain('never checked to come back')
    expect(el.textContent).not.toContain('App Platform only')
    expect(el.textContent).toContain(onDisk.why)
    const kept = Array.from(el.querySelectorAll('div')).filter((d) => d.textContent?.startsWith('Backups kept on this host ⚠')).pop()
    expect(kept?.className).toBe('muted')
    expect(el.textContent).not.toContain(body.note)
  })
})

// Where a Docker deployment's backups are is what is recorded, and a
// contract that cannot be read is unknown, not nothing (OHB2-01, OHB2-02).
describe('what a Docker deployment card says it holds', () => {
  const read = async (body: unknown) => {
    const { el, client } = await mount(body, {}, docker)
    await settled(client)
    const text = el.textContent ?? ''
    done(); done = () => {}
    return text
  }
  const base = { deployment: 'main', dataClass: 'production', offHost: false, target: 'home', recent: [], running: null, restoredCopies: [], coverage,
    operations: { copy: { available: false, reason: 'backups-page', why: 'x' }, check: { available: false, reason: 'not-on-this-platform', why: 'x' }, restore: { available: false, reason: 'not-on-this-platform', why: 'x' }, rehearsal: { available: false, reason: 'no-copy', why: 'none yet' }, startFromCopy: { available: false, reason: 'x', why: 'x' }, finalCopy: { available: true } },
    items: [{ need: 'postgres', where: 'on home', copy: 'none' }] }
  it('says no backup yet when none is recorded, never that they are on this host', async () => {
    const text = await read(base)
    expect(text).toContain('No backup yet')
    expect(text).not.toContain('this host')
  })
  it('says where the newest backup is', async () => {
    expect(await read({ ...base, keptOn: 'host' })).toContain('Backups kept on this host')
    expect(await read({ ...base, keptOn: 'store', offHost: true })).toContain('Backups kept in the backup store')
  })
  it('says it could not read what the deployment holds, rather than nothing', async () => {
    const text = await read({ ...base, items: [], operations: undefined, unknown: 'no contract: bad yaml',
      coverage: { assessed: [], notAssessed: ["the deployment's own data: what it holds is unknown", outside] } })
    expect(text).toContain('unknown')
    expect(text).toContain("couldn't read what this deployment holds")
    expect(text).toContain(`not assessed the deployment's own data: what it holds is unknown`)
    expect(text).toContain(`not assessed ${outside}`)
  })
})

// A card's evidence speaks for the items assessed only. A fresh copy and a
// passed rehearsal still show data outside the deployment as not assessed,
// right beside them, and a deployment holding no data the forge knows of
// still says so rather than showing nothing.
describe('what a deployment card covers', () => {
  const read = async (body: unknown) => {
    const { el, client } = await mount(body, {}, docker)
    await settled(client)
    return el
  }
  const rehearsal = { at: '2026-10-08T02:00:00Z', outcome: 'passed', passed: true, backup: SET, release: 7, host: 'rehearse-1', operation: SET, facts: { egress: 'outbound network not blocked', checks: [] } }
  it('shows outside data not assessed beside a fresh copy and a passed rehearsal', async () => {
    const el = await read({ deployment: 'main', dataClass: 'production', offHost: true, keptOn: 'store', target: 'home', recent: [], running: null, restoredCopies: [], rehearsal,
      coverage: { assessed: ['postgres · volume forge-need-env-shelf-main-postgres'], notAssessed: [outside] },
      operations: { copy: { available: false, reason: 'backups-page', why: 'x' }, check: { available: false, reason: 'not-on-this-platform', why: 'x' }, restore: { available: false, reason: 'not-on-this-platform', why: 'x' }, rehearsal: yes, startFromCopy: { available: false, reason: 'x', why: 'x' }, finalCopy: yes },
      items: [{ need: 'postgres', volume: 'forge-need-env-shelf-main-postgres', where: 'on home', copy: 'fresh', lastCopy: { backup: SET, takenAt: '2026-10-08T01:00:00Z', bytes: 40, sha256: 'a' } }] })
    const text = el.textContent ?? ''
    expect(text).toContain('covers: postgres · volume forge-need-env-shelf-main-postgres')
    expect(text).toContain('last restore rehearsal passed')
    const limit = Array.from(el.querySelectorAll('div')).find((d) => d.textContent === `not assessed ${outside}`)
    expect(limit?.querySelector('.chip')?.textContent).toBe('not assessed')
    expect(text).not.toMatch(/fully protected|complete/i)
  })
  it('says what is not assessed when it holds no data the forge knows of', async () => {
    const el = await read({ deployment: 'main', dataClass: 'production', offHost: false, target: 'home', recent: [], running: null, restoredCopies: [], items: [],
      coverage: { assessed: [], notAssessed: [outside] } })
    expect(el.textContent).toContain('covers: no data AppTown knows of')
    expect(el.textContent).toContain(`not assessed ${outside}`)
  })
})
