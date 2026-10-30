// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { BundleFacts, NewReleasePage, ReleasePermalinkPage } from './Release'
import { QueryClientProvider, focusManager } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { newQueryClient } from '../../data'

// Record ids are UUIDv7 strings.
const B14 = '00000000-0014-7000-8000-000000000000'
const B15 = '00000000-0015-7000-8000-000000000000'
const B3 = '00000000-0003-7000-8000-000000000000'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function render(node: React.ReactNode) {
  const el = document.createElement('div')
  const root = createRoot(el)
  act(() => { root.render(node) })
  return { el, root }
}

describe('a release page', () => {
  // A bundle by digest names its registry, platform and every image's
  // digest, and says App Platform can take it; one by image ID says it
  // cannot go where only digests go.
  it('says whether its bundle is by digest, with each image', () => {
    const ref = 'registry.example/team/p@sha256:' + 'a'.repeat(64)
    const digest = render(<BundleFacts bundle={{ id: B14, byDigest: true, registry: 'registry.example/team/p', platform: 'linux/amd64', parts: { web: ref }, init: { migrate: ref } }} />)
    expect(digest.el.textContent).toContain('by digest')
    expect(digest.el.textContent).toContain('App Platform included')
    expect(digest.el.textContent).toContain('init migrate')
    expect(digest.el.textContent).toContain(ref)
    const arm = render(<BundleFacts bundle={{ id: B15, byDigest: true, registry: 'r', platform: 'linux/arm64', parts: { web: 'r@sha256:b' }, init: {} }} />)
    expect(arm.el.textContent).toContain('so it cannot')
    const ids = render(<BundleFacts bundle={{ id: B3, byDigest: false, parts: { web: 'sha256:local' }, init: {} }} />)
    expect(ids.el.textContent).toContain('by image ID')
    expect(ids.el.textContent).toContain('sha256:local')
    for (const r of [digest, arm, ids]) act(() => r.root.unmount())
  })
})

// A released version can fail later. Its log must refresh after settlement,
// and a failed refresh retains the witnessed note under a stale marker.
it('reads a crash-loop note after successful release and marks a failed refresh', async () => {
  vi.useFakeTimers()
  focusManager.setFocused(true)
  const note = 'After release: the app crash-looped (5 restarts in 10m0s); AppTown stopped its container.'
  let calls = 0
  vi.stubGlobal('fetch', vi.fn(async () => {
    calls++
    if (calls >= 3) return new Response(JSON.stringify({ error: 'the host is unavailable' }), { status: 404 })
    return new Response(JSON.stringify({
      release: { id: B14, number: 14, state: 'released', deployment: 'main', createdAt: '2026-10-13T04:34:46Z', releasedAt: '2026-10-13T04:34:46Z' },
      isLive: true, changes: [], events: calls === 1 ? [] : [{ at: '2026-10-13T04:38:45Z', text: note }],
    }))
  }))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><ReleasePermalinkPage slug="shop" number="14" /></MemoryRouter></QueryClientProvider>) })
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(el.textContent).toContain('Released')
    expect(el.textContent).not.toContain(note)
    await act(async () => { await vi.advanceTimersByTimeAsync(8500) })
    expect(el.textContent).toContain(note)
    expect(el.textContent).toContain('Released')
    await act(async () => { await vi.advanceTimersByTimeAsync(8500) })
    expect(el.textContent).toContain(note)
    expect(el.textContent).toContain('unknown')
  } finally {
    act(() => root.unmount())
    client.clear()
    focusManager.setFocused(undefined)
    vi.useRealTimers()
    vi.unstubAllGlobals()
  }
})

it('starts reading the new permalink after a historical release stopped polling', async () => {
  vi.useFakeTimers()
  focusManager.setFocused(true)
  let reads = 0
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    const current = url.includes('/releases/15')
    if (current) reads++
    return new Response(JSON.stringify({
      release: { id: current ? B15 : B14, number: current ? 15 : 14, state: 'released', deployment: 'main', createdAt: '2026-10-13T04:34:46Z' },
      isLive: current, changes: [], events: [{ at: '2026-10-13T04:38:45Z', text: current ? reads > 1 ? 'New version crash-loop note' : 'New version released' : 'Old version history' }],
    }))
  }))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  const page = (number: string) => <QueryClientProvider client={client}><MemoryRouter><ReleasePermalinkPage slug="shop" number={number} /></MemoryRouter></QueryClientProvider>
  try {
    await act(async () => { root.render(page('14')) })
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(el.textContent).toContain('Old version history')
    await act(async () => { root.render(page('15')) })
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(el.textContent).toContain('New version released')
    expect(el.textContent).not.toContain('Old version history')
    await act(async () => { await vi.advanceTimersByTimeAsync(8500) })
    expect(el.textContent).toContain('New version crash-loop note')
  } finally {
    act(() => root.unmount())
    client.clear()
    focusManager.setFocused(undefined)
    vi.useRealTimers()
    vi.unstubAllGlobals()
  }
})


it.each([true, false])('uses the explicit customer environment and its displayed revision (configured=%s)', async (configured) => {
  vi.useFakeTimers()
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  let submitted: unknown
  vi.stubGlobal('fetch', vi.fn(async (_url, init?: RequestInit) => {
    if (init?.method === 'POST') {
      submitted = JSON.parse(String(init.body))
      return new Response(JSON.stringify({ number: 22 }))
    }
    return new Response(JSON.stringify({ configured, deployment: configured ? 'customer' : '', deploymentLabel: 'Office tools', defaultRevision: 7, published: false, head: { sha: 'abcdef', subject: 'Current software' }, changesSinceLast: [], configError: configured ? '' : 'Choose a release environment in project settings.' }))
  }))
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><NewReleasePage slug="shop" /></MemoryRouter></QueryClientProvider>) })
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    const button = el.querySelector('button.primary') as HTMLButtonElement | null
    if (configured) {
      expect(el.textContent).toContain('Release to Office tools')
      expect(button).not.toBeNull()
      await act(async () => { button!.click() })
      expect(submitted).toEqual({ defaultRevision: 7 })
    } else {
      expect(button).toBeNull()
      expect(el.textContent).toContain('Choose a release environment')
      expect(submitted).toBeUndefined()
    }
  } finally {
    act(() => root.unmount())
    client.clear()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  }
})

it('keeps choosing a preference separate from a refused release', async () => {
  const client = newQueryClient(); const el = document.createElement('div'); document.body.appendChild(el); const root = createRoot(el)
  let saved = false
  const writes: { method: string; path: string; body: unknown }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const path = String(url).replace('/api/v1', '')
    if (init?.method === 'PUT' || init?.method === 'POST') {
      writes.push({ method: init.method, path, body: JSON.parse(String(init.body)) })
      if (path.endsWith('/release/default')) { saved = true; return new Response('{"deploymentId":"chosen","revision":10}') }
      return new Response('{"error":"deployment permission withdrawn"}', { status: 403 })
    }
    if (path.endsWith('/release/plan')) return new Response(JSON.stringify({ configured: saved, defaultRevision: saved ? 10 : 9, deployment: saved ? 'customer-test' : '', deploymentLabel: saved ? 'Customer office' : '', head: { sha: 'abc', subject: 'Software' }, changesSinceLast: [] }))
    if (path.endsWith('/release/default')) return new Response(JSON.stringify(saved ? { deploymentId: 'chosen', revision: 10 } : { revision: 9 }))
    if (path.endsWith('/deployments')) return new Response('[{"id":"chosen","name":"customer-test","displayLabel":"Customer office","kind":"standing","feed":"promotion"}]')
    throw new Error(`unexpected read ${path}`)
  }))
  const settle = async () => { await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) }) }
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><NewReleasePage slug="shop" /></MemoryRouter></QueryClientProvider>) })
    await vi.waitFor(async () => { await settle(); expect(el.textContent).toContain('Choose release preference') })
    expect(writes).toEqual([])
    await act(async () => { [...el.querySelectorAll('button')].find(b => b.textContent === 'Choose release preference')!.click() })
    await vi.waitFor(async () => { await settle(); expect(el.textContent).toContain('Release preference cleared') })
    expect(writes).toEqual([])
    await act(async () => {
      const select = el.querySelector('select')!
      select.value = 'chosen'
      select.dispatchEvent(new Event('change', { bubbles: true }))
    })
    await act(async () => { el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })) })
    await vi.waitFor(async () => { await settle(); expect(el.textContent).toContain('Release to Customer office') })
    expect(writes).toEqual([{ method: 'PUT', path: '/projects/shop/release/default', body: { deploymentId: 'chosen', expectedRevision: 9 } }])
    expect(el.textContent).toContain('No release has started')
    await act(async () => { (el.querySelector('button.primary') as HTMLButtonElement).click() })
    await vi.waitFor(async () => { await settle(); expect(el.textContent).toContain('deployment permission withdrawn') })
    expect(writes[1]).toEqual({ method: 'POST', path: '/projects/shop/release', body: { defaultRevision: 10 } })
    expect(el.textContent).toContain('Release preference saved')
  } finally { act(() => root.unmount()); client.clear(); el.remove(); vi.unstubAllGlobals() }
})
