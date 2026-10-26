// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from './data'
import { settled } from './settled'
import App from './App'
import { BigChangeBox } from './pages/project/Overview'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const cleanups: (() => void)[] = []
afterEach(() => {
  for (const clean of cleanups.splice(0)) clean()
  vi.unstubAllGlobals()
})
async function render(child: ReactNode) {
  const client = newQueryClient()
  client.setDefaultOptions({ queries: { retry: false } })
  const el = document.createElement('div')
  document.body.append(el)
  const root = createRoot(el)
  cleanups.push(() => { act(() => root.unmount()); client.clear(); el.remove() })
  await act(async () => {
    root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/projects']}>{child}</MemoryRouter></QueryClientProvider>)
  })
  await settled(client)
  return el
}

it('R1: distinguishes an unreadable release plan from no configured intranet', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.endsWith('/release/plan')) return new Response(JSON.stringify({ error: 'release plan unavailable' }), { status: 500 })
    if (url.endsWith('/projects/p')) return new Response(JSON.stringify({ slug: 'p', name: 'P', runtimeForm: 'runs from source', intranetReady: false }))
    return new Response('[]')
  }))
  const el = await render(<BigChangeBox slug="p" />)
  expect(el.textContent).toContain('release plan unavailable')
})

it('R2: retains global finding on the narrow projects page', async () => {
  vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: true, addEventListener() {}, removeEventListener() {} })))
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.endsWith('/auth/me')) return new Response(JSON.stringify({ mode: 'open' }))
    if (url.includes('/authz/me')) return new Response(JSON.stringify({ instance: {} }))
    if (url.endsWith('/attention')) return new Response(JSON.stringify({ items: [], sources: [] }))
    if (url.includes('/project-cleanups')) return new Response(JSON.stringify({ cleanups: [] }))
    return new Response(JSON.stringify({ projects: [], total: 0 }))
  }))
  const el = await render(<App />)
  const menu = el.querySelector<HTMLButtonElement>('[aria-label="open the menu"]')!
  await act(async () => menu.click())
  expect(el.querySelector('#find-projects')).not.toBeNull()
})
