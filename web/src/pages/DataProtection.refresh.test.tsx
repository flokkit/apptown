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
describe('a protection reading whose refresh failed', () => {
  afterEach(() => vi.unstubAllGlobals())
  const mount = async (path: string, ui: React.ReactNode, body: unknown) => {
    const client = newQueryClient()
    vi.stubGlobal('fetch', answer(body))
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/operations/${OPERATION}`]}>{ui}</MemoryRouter></QueryClientProvider>) })
    await vi.waitFor(() => expect(el.textContent).not.toContain('Reading'))
    vi.stubGlobal('fetch', down)
    await act(async () => { await client.refetchQueries({ queryKey: getKey(path) }).catch(() => {}) })
    return { el, done: () => { act(() => root.unmount()); client.clear() } }
  }
  it('says the copies were fresh when last read, not that they are', async () => {
    const { el, done } = await mount('/projects/ledger/deployments/prod/protection', <ProtectionLines slug="ledger" name="prod" />, protection)
    await vi.waitFor(() => expect(el.textContent).toContain('unknown'))
    expect(el.textContent).toContain('as read')
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
