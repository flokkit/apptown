// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, expect, it, vi } from 'vitest'
import App from './App'
import { newQueryClient } from './data'
import { settled } from './settled'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
afterEach(() => { vi.unstubAllGlobals(); window.history.replaceState(null, '', '/') })

const page = '/projects/shop/deployments/main'
const deployment = { id: 'd', name: 'main', kind: 'standing', generation: 0, status: { word: 'stopped', reason: '', stale: false }, runtimeState: 'stopped', runtimeControls: {}, routineUpdates: { mode: 'automatic' } }

// A fresh load starts the page's reads before "who am I" answers. Each
// request is recorded with its signal, so a read the app abandoned shows as
// aborted; `who` is what /auth/me answers. `held` keeps the first answers of
// /auth/me and the deployment back until the test answers them, so the
// deployment read is still on its way when the app learns who this is.
function server() {
  let who = 'alice'
  let release = () => {}
  const held = new Promise<void>((resolve) => { release = resolve })
  const asked: { path: string; signal?: AbortSignal | null }[] = []
  const fetcher = vi.fn(async (url: string, init?: RequestInit) => {
    const path = String(url).replace('/api/v1', '')
    asked.push({ path, signal: init?.signal })
    if (path === '/auth/me') { await held; return new Response(JSON.stringify({ mode: 'internal', user: who, role: 'admin' })) }
    if (path === '/projects/shop/deployments') { await held; return new Response(JSON.stringify([deployment])) }
    if (path === '/projects/shop/releases' || path === '/features') return new Response('[]')
    return new Response('{}')
  })
  vi.stubGlobal('fetch', fetcher)
  return { asked, answer: () => release(), become: (name: string) => { who = name } }
}

async function load() {
  const s = server()
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[page]}><App /></MemoryRouter></QueryClientProvider>))
  return { ...s, client, el, done: () => { act(() => root.unmount()); client.clear() } }
}

const reads = (asked: { path: string; signal?: AbortSignal | null }[], path: string) => asked.filter(a => a.path === path)

it('keeps the page and its reads when "who am I" first answers', async () => {
  const v = await load()
  try {
    expect(reads(v.asked, '/projects/shop/deployments')).toHaveLength(1)
    v.answer()
    await settled(v.client)
    const deployments = reads(v.asked, '/projects/shop/deployments')
    expect(deployments).toHaveLength(1)
    expect(deployments[0].signal?.aborted).toBe(false)
    expect(v.el.querySelector('h1')?.textContent).toContain('main')
  } finally { v.done() }
})

it('starts the page again and reads afresh when another person signs in', async () => {
  const v = await load()
  try {
    v.answer()
    await settled(v.client)
    const before = reads(v.asked, '/projects/shop/deployments').length
    v.become('bob')
    await act(async () => { await v.client.invalidateQueries({ queryKey: ['get', 'auth', 'me'] }) })
    await settled(v.client)
    expect(reads(v.asked, '/projects/shop/deployments').length).toBeGreaterThan(before)
    expect(v.el.querySelector('h1')?.textContent).toContain('main')
  } finally { v.done() }
})

// The deploy history needs only the project, so the page asks for it beside
// the deployment instead of after the deployment answers.
it('asks for the releases beside the deployment', async () => {
  const v = await load()
  try {
    expect(reads(v.asked, '/projects/shop/releases')).toHaveLength(1)
    v.answer()
    await settled(v.client)
    expect(reads(v.asked, '/projects/shop/releases')).toHaveLength(1)
  } finally { v.done() }
})
