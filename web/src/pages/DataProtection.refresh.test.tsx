import { settled } from '../settled'
import { withFeatures } from '../features.fixture'
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../data'
import { OperationPage, ProtectionLines } from './DataProtection'

// Record ids are UUIDv7 strings.
const SET = '00000000-0003-7000-8000-000000000000'
const OPERATION = '00000000-0007-7000-8000-000000000000'
const PROJECT = '00000000-0001-7000-8000-000000000000'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const now = new Date().toISOString()
const protection = {
  deployment: 'prod', dataClass: 'production', offHost: true, recent: [], running: null, restoredCopies: [], coverage: { assessed: ['postgres'], notAssessed: [] },
  items: [{ need: 'postgres', where: 'database app on forge-ledger-prod', copy: 'fresh', copyLabel: 'fresh', lastCopy: { backup: SET, takenAt: now, bytes: 2048, sha256: 'ab' } }],
}
const takeDown = { id: OPERATION, projectId: PROJECT, subject: 'project:1/deployment:2', holds: ['project:1/deployment:2'], input: { deployment: 'prod' }, kind: 'deployment.take-down',
  askedBy: 'bls', state: 'running', result: { writesStopped: true }, cancellable: true, createdAt: now, touchedAt: now, startedAt: now,
  steps: [{ name: 'deleting the app, so nothing writes', started: now, ended: now }, { name: 'copying postgres', started: now }] }

const answer = (body: unknown) => vi.fn(async () => new Response(JSON.stringify(body), { status: 200 }))
const down = vi.fn(async () => new Response(JSON.stringify({ error: 'AppTown is restarting' }), { status: 502 }))

// DPB-05: an answer that could not be refreshed is shown as what was last
// read, with its age; it never goes on claiming fresh copies or stopped
// writes as they are now.
// A status's details ride in its title, not in the page text.
const details = (root: Element) => [...root.querySelectorAll('[data-help], [title]')].map(e => e.getAttribute('data-help') ?? e.getAttribute('title')).join(' | ')

describe('a protection reading whose refresh failed', () => {
  afterEach(() => vi.unstubAllGlobals())
  const mount = async (path: string, ui: React.ReactNode, body: unknown) => {
    const client = newQueryClient()
    vi.stubGlobal('fetch', withFeatures(answer(body)))
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/operations/${OPERATION}`]}>{ui}</MemoryRouter></QueryClientProvider>) })
    await settled(client)
    vi.stubGlobal('fetch', withFeatures(down))
    await act(async () => { await client.refetchQueries({ queryKey: getKey(path) }).catch(() => {}) })
    return { el, done: () => { act(() => root.unmount()); client.clear() } }
  }
  it('says the copies were fresh when last read, not that they are', async () => {
    const { el, done } = await mount('/projects/ledger/deployments/prod/protection', <ProtectionLines slug="ledger" name="prod" />, protection)
    await vi.waitFor(() => expect(el.textContent).toContain('unknown'))
    // How old the answer is rides in the unknown status's details.
    expect(details(el)).toContain('as read')
    expect(el.querySelector('.chip.pass')).toBeNull()
    done()
  })
  it('says writes were stopped when last read, not that they are', async () => {
    const { el, done } = await mount(`/operations/${OPERATION}`, <Routes><Route path="/operations/:id" element={<OperationPage />} /></Routes>, takeDown)
    await vi.waitFor(() => expect(el.textContent).toContain('unknown'))
    expect(el.textContent).toContain('writes stopped as read')
    done()
  })
})

// f502, f503: the operation page names its project and links its
// deployment; the ledger's subject key (ids) is for diagnosis, shown only
// with the technical switch on.
describe('an operation page', () => {
  afterEach(() => { vi.unstubAllGlobals(); localStorage.clear() })
  // The switch is remembered per person for the session, so each test
  // reads as its own person.
  const show = async (body: unknown, technical: boolean) => {
    const user = `kim-${Math.random().toString(36).slice(2)}`
    if (technical) localStorage.setItem(`apptown.technical-detail.${user}`, '1')
    const client = newQueryClient()
    vi.stubGlobal('fetch', withFeatures(async (url: string) => new Response(JSON.stringify(url.endsWith('/auth/me') ? { mode: 'internal', user } : body), { status: 200 })))
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/operations/${OPERATION}`]}><Routes><Route path="/operations/:id" element={<OperationPage />} /></Routes></MemoryRouter></QueryClientProvider>) })
    await settled(client)
    await vi.waitFor(() => expect(el.querySelector('h1')).not.toBeNull())
    return { el, done: () => { act(() => root.unmount()); client.clear() } }
  }
  const withProject = { ...takeDown, project: { slug: 'ledger', name: 'Ledger' } }
  it('links the deployment and the project, without the subject key', async () => {
    const { el, done } = await show(withProject, false)
    const title = el.querySelector('h1')!
    expect(title.textContent).toBe('Take-down of prod')
    expect(title.querySelector('a')?.getAttribute('href')).toBe('/projects/ledger/deployments/prod')
    const project = [...el.querySelectorAll('a')].find(a => a.textContent === 'Ledger')
    expect(project?.getAttribute('href')).toBe('/projects/ledger')
    expect(el.textContent).not.toContain('project:')
    done()
  })
  it('shows the subject key with the technical switch on', async () => {
    const { el, done } = await show(withProject, true)
    await vi.waitFor(() => expect(el.textContent).toContain('project:1/deployment:2'))
    done()
  })
  it('names the deployment without links when the project is not known', async () => {
    const { el, done } = await show(takeDown, false)
    expect(el.querySelector('h1')!.textContent).toBe('Take-down of prod')
    expect(el.querySelector('h1 a')).toBeNull()
    expect(el.textContent).not.toContain('project:')
    done()
  })
  // f507: a restore's summary carries no ids; the restored set's id is
  // for diagnosis, behind the technical switch, and a step that took under
  // half a second does not read "0s".
  const restore = { ...withProject, kind: 'deployment.restore', state: 'failed', cancellable: false, finishedAt: now, result: { restore: { backup: SET, from: 'prod' } },
    detail: 'Set from prod restored, not verified: data copied.', steps: [{ name: 'restoring data', started: '2026-11-11T09:06:00.000Z', ended: '2026-11-11T09:06:00.200Z' }] }
  it('keeps the restored set behind the technical switch and says under a second', async () => {
    const plain = await show(restore, false)
    expect(plain.el.textContent).not.toContain(SET)
    expect(plain.el.textContent).toContain('under 1s')
    expect(plain.el.textContent).not.toContain('0s')
    plain.done()
    const technical = await show(restore, true)
    await vi.waitFor(() => expect(technical.el.textContent).toContain(`set ${SET}`))
    technical.done()
  })
})
