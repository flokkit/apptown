// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { BundleFacts, ReleasePermalinkPage } from './Release'
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
  const note = 'After release: the app crash-looped (5 restarts in 10m0s); Forge stopped its container.'
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
    expect(el.textContent).toContain('out of date')
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
