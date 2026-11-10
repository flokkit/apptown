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

it.each(['Retry', 'Skip'])('records %s in place, with a required reason only for Skip', async label => {
  const { el, done } = await render({ remediation, occupancy, attempts: {}, items: [{ id: 'stuck', projectSlug: 'notes', state: 'stuck', statusLabel: 'failed', check: 'dockerfile-lint', attempt: 1, reason: 'The build failed.' }] })
  const calls: { url: string; body: unknown }[] = []
  const fetch = globalThis.fetch
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') { calls.push({ url, body: JSON.parse(String(init.body)) }); return new Response('{}') }
    return fetch(url, init)
  }))
  const button = (text: string) => [...el.querySelectorAll('button')].reverse().find(b => b.textContent === text)!
  try {
    await act(async () => button(label).click())
    expect(el.querySelector('input')).not.toBeNull()
    expect(button(label).disabled).toBe(label === 'Skip')
    expect(calls).toEqual([])
    await act(async () => button('Cancel').click())
    expect(calls).toEqual([])
    await act(async () => button(label).click())
    const input = el.querySelector('input')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'The project has moved')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => button(label).click())
    expect(calls).toEqual([{ url: `/api/v1/conformance/remediations/job/items/stuck/${label.toLowerCase()}`, body: { [label === 'Retry' ? 'note' : 'reason']: 'The project has moved' } }])
  } finally { done() }
})

// A running remediation's chip says "in progress" and opens nothing; one that
// finished with stuck items opens what its word does not say.
it('opens remediation status details only when they add to the word', async () => {
  const running = await render({ remediation, occupancy, attempts: {}, items: [] })
  try { expect(running.el.querySelector('h1 .chip-button')).toBeNull(); expect(running.el.querySelector('h1 .chip')?.textContent).toContain('in progress') } finally { running.done() }
  const stuck = await render({ remediation: { ...remediation, status: 'completed_with_stuck', statusLabel: 'failed' }, occupancy, attempts: {}, items: [] })
  try { expect(stuck.el.querySelector('h1 .chip-button')?.getAttribute('data-help')).toBe('finished; some items could not be fixed') } finally { stuck.done() }
})
