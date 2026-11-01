// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { fmtStamp } from '../format'
import { getKey, newQueryClient } from '../data'
import { PoliciesCard } from './project/settings/Working'
import { DeletedProjects } from './Infrastructure'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// An empty answer whose refresh failed may be old just like a full one:
// someone may have adopted a rule or deleted a project since. The page says
// so instead of presenting the old empty answer as current (review
// web-readgate-2026-10-08, W-01).
afterEach(() => vi.unstubAllGlobals())

async function emptyThenDown(ui: React.ReactNode, path: string, empty: unknown) {
  let down = false
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (String(url).includes('/authz/me')) return new Response(JSON.stringify({ user: 'bls', instance: { 'identity:manage-auth': true } }))
    if (down) return new Response(JSON.stringify({ error: 'database unavailable' }), { status: 400 })
    return new Response(JSON.stringify(empty))
  }))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter>{ui}</MemoryRouter></QueryClientProvider>) })
  await vi.waitFor(() => expect(client.getQueryState(getKey(path))?.status).toBe('success'))
  down = true
  await act(async () => { await client.refetchQueries({ queryKey: getKey(path) }).catch(() => {}) })
  return { el, done: () => { act(() => root.unmount()); client.clear() } }
}

it('says an empty rule list may be old when its refresh failed', async () => {
  const { el, done } = await emptyThenDown(<PoliciesCard slug="shop" />, '/projects/shop/policies', { policies: [], violations: [] })
  await vi.waitFor(() => expect(el.textContent).toContain('unknown'))
  expect(el.querySelector('.chip.unknown')?.getAttribute('aria-label')).toContain('(database unavailable)')
  expect(el.textContent).toContain('No rules adopted')
  done()
})

it('says "no cleanups under way" may be old when its refresh failed', async () => {
  const { el, done } = await emptyThenDown(<DeletedProjects />, '/project-cleanups', { cleanups: [] })
  await vi.waitFor(() => expect(el.textContent).toContain('unknown'))
  expect(el.querySelector('.chip.unknown')?.getAttribute('aria-label')).toContain('(database unavailable)')
  done()
})


it('dates a deletion as an action with its actor', async () => {
  const requestedAt = '2026-11-01T00:00:00Z'
  const { el, done } = await emptyThenDown(<DeletedProjects />, '/project-cleanups', { cleanups: [{ projectId: 'gone', slug: 'gone', state: 'pending', attempts: 0, requestedAt, requestedBy: 'admin' }] })
  try {
    expect(el.textContent).toContain(`deleted ${fmtStamp(requestedAt)} by admin`)
    expect(el.textContent).not.toContain('deleted last seen')
  } finally { done() }
})
