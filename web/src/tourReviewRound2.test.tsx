// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from './data'
import { settled } from './settled'
import { BigChangeBox } from './pages/project/Overview'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const cleanups: (() => void)[] = []
afterEach(() => {
  for (const clean of cleanups.splice(0)) clean()
  vi.unstubAllGlobals()
})

it.each([false, true])('marks the held release configuration stale after a failed refresh (configured=%s)', async (configured) => {
  let failed = false
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.endsWith('/release/plan')) return failed
      ? new Response(JSON.stringify({ error: 'release plan unavailable' }), { status: 500 })
      : new Response(JSON.stringify({ configured }))
    if (url.endsWith('/projects/p')) return new Response(JSON.stringify({ slug: 'p', name: 'P', runtimeForm: 'runs from source', intranetReady: false }))
    return new Response('[]')
  }))
  const client = newQueryClient()
  client.setDefaultOptions({ queries: { retry: false } })
  const el = document.createElement('div')
  const root = createRoot(el)
  cleanups.push(() => { act(() => root.unmount()); client.clear() })
  await act(async () => {
    root.render(<QueryClientProvider client={client}><MemoryRouter><BigChangeBox slug="p" /></MemoryRouter></QueryClientProvider>)
  })
  await settled(client)
  expect(el.textContent?.includes('Release')).toBe(configured)
  expect(el.querySelector('.out-of-date')).toBeNull()
  failed = true
  await act(async () => { await client.refetchQueries({ queryKey: getKey('/projects/p/release/plan') }) })
  await settled(client)
  // Prove the failed refresh happened while the previous answer stayed cached.
  const state = client.getQueryState(getKey('/projects/p/release/plan'))!
  expect(state.status).toBe('error')
  expect(state.data).toEqual({ configured })
  const mark = el.querySelector('.out-of-date')
  expect(mark).not.toBeNull()
  expect(mark?.getAttribute('aria-label')).toContain('release plan unavailable')
})
