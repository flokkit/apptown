// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { newQueryClient } from '../../data'
import { settled } from '../../settled'
import { RemediationPage } from './RemediationPage'
import { RemediationsList } from './Remediations'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
vi.mock('../../instancePages', () => ({ useHolds: () => false }))
afterEach(() => vi.unstubAllGlobals())

const remediation = { id: 'job', number: 2, status: 'running', statusLabel: 'working', sentence: 'fix everything on notes · 2 queued · 2 waiting on project work', counts: { total: 2, queued: 2 }, actor: 'sam', mode: 'attended', createdAt: '2026-11-02T00:00:00Z' }
const occupancy = { kind: 'remediation', workers: 2, busy: 0, queued: 2, holding: [] }
async function render(body: unknown, list = false) {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(body))))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter>{list ? <RemediationsList /> : <RemediationPage id="job" />}</MemoryRouter></QueryClientProvider>) })
  await settled(client)
  return { el, done: () => { act(() => root.unmount()); client.clear() } }
}

it('shows queued reasons and links the changes and remediation that hold them', async () => {
  const reasons = [
    { kind: 'project', reason: 'Waiting: project work is running.', changes: [{ id: 'four', number: 4, kind: 'step' }, { id: 'five', number: 5, kind: 'landing' }] },
    { kind: 'remediation', reason: 'Waiting: another remediation item is in progress.', remediationId: 'other-job', remediationNumber: 3 },
    { kind: 'cooldown', reason: 'Waiting: the worker pool is cooling down. provider rate limit' },
    { kind: 'worker', reason: 'Waiting: starts on the next pass when a worker is free.' },
  ]
  const { el, done } = await render({ remediation, occupancy, attempts: {}, items: reasons.map((waiting, i) => ({ id: `item-${i}`, projectSlug: 'notes', state: 'queued', statusLabel: 'waiting', check: 'dockerfile-lint', attempt: 0, reason: 'old retry feedback', waiting })) })
  try {
    for (const { reason } of reasons) expect(el.textContent).toContain(reason)
    expect(el.querySelector('a[href="/changes/four"]')?.textContent).toBe('change 4')
    expect(el.querySelector('a[href="/changes/five"]')?.textContent).toBe('change 5')
    expect(el.querySelector('a[href="/conformance/remediations/other-job"]')?.textContent).toBe('remediation 3')
    expect(el.textContent).not.toContain('old retry feedback')
  } finally { done() }
})

it('keeps the recorded reason for a stuck item', async () => {
  const { el, done } = await render({ remediation, occupancy, attempts: {}, items: [{ id: 'stuck', projectSlug: 'notes', state: 'stuck', statusLabel: 'failed', check: 'dockerfile-lint', attempt: 1, reason: 'The build failed.' }] })
  try { expect(el.textContent).toContain('The build failed.') } finally { done() }
})

it('shows the owner’s waiting counts on the remediation list', async () => {
  const { el, done } = await render({ remediations: [remediation], occupancy }, true)
  try { expect(el.textContent).toContain('2 waiting on project work') } finally { done() }
})
