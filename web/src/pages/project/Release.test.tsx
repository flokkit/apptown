// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { BundleFacts, OpenThisVersion, ReleasePermalinkPage } from './Release'
import { QueryClientProvider, focusManager } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { getKey, newQueryClient } from '../../data'

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
    const digest = render(<dl><BundleFacts bundle={{ id: B14, byDigest: true, registry: 'registry.example/team/p', platform: 'linux/amd64', parts: { web: ref }, init: { migrate: ref } }} /></dl>)
    expect(digest.el.textContent).toContain('Any linux/amd64 host')
    expect(digest.el.textContent).toContain('App Platform included')
    expect(digest.el.textContent).toContain('init migrate')
    expect(digest.el.textContent).toContain(ref)
    const arm = render(<dl><BundleFacts bundle={{ id: B15, byDigest: true, registry: 'r', platform: 'linux/arm64', parts: { web: 'r@sha256:b' }, init: {} }} /></dl>)
    expect(arm.el.textContent).toContain('so it cannot')
    const ids = render(<dl><BundleFacts bundle={{ id: B3, byDigest: false, parts: { web: 'sha256:local' }, init: {} }} /></dl>)
    expect(ids.el.textContent).toContain('Only the server where it was built')
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


it.each([
  ['ok', 'Backed up before this release →'],
  ['partial', 'Backup before this release was incomplete →'],
  ['failed', 'Backup before this release was incomplete →'],
  ['pruned', 'Backup before this release was removed →'],
])('describes the recorded %s backup and links its set', async (status, label) => {
  const client = newQueryClient()
  const body = { release: { id: B14, number: 14, state: 'released', deployment: 'main', createdAt: '2026-11-05T00:00:00Z' }, statusLabel: 'released', isLive: false, changes: [], events: [], backup: { id: B3, takenAt: '2026-11-05T00:00:00Z', bytes: 32, status } }
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(body))))
  const view = render(<QueryClientProvider client={client}><MemoryRouter><ReleasePermalinkPage slug="shop" number="14" /></MemoryRouter></QueryClientProvider>)
  try {
    await vi.waitFor(() => expect(view.el.textContent).toContain(label))
    const link = [...view.el.querySelectorAll('a')].find(a => a.textContent === label)
    expect(link?.getAttribute('href')).toBe(`/deployments/backups#backup-${B3}`)
    if (status !== 'ok') expect(view.el.textContent).not.toContain('Backed up before')
  } finally { act(() => view.root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
it('shows an unlinked reason for a copy that could not be assessed', async () => {
  const client = newQueryClient()
  const reason = 'Could not read the data; went ahead: accepted by Sam'
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ release: { id: B14, number: 14, state: 'released', createdAt: '2026-11-05T00:00:00Z' }, changes: [], events: [], backupReason: reason }))))
  const view = render(<QueryClientProvider client={client}><MemoryRouter><ReleasePermalinkPage slug="shop" number="14" /></MemoryRouter></QueryClientProvider>)
  try {
    await vi.waitFor(() => expect(view.el.textContent).toContain(reason))
    expect(view.el.textContent).not.toContain('Backed up before')
    expect(view.el.querySelector('a[href*="backup-"]')).toBeNull()
  } finally { act(() => view.root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

it('shows the follows-main setter separately from landing approvers', async () => {
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})))
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/shop/releases/14'), {
    release: { id: B14, number: 14, state: 'released', createdAt: '2026-11-06T00:00:00Z' },
    request: { feed: 'main', setBy: 'Sam', settingsRevision: 3, approvers: 'Priya' },
    events: [], changes: [],
  })
  const view = render(<QueryClientProvider client={client}><MemoryRouter><ReleasePermalinkPage slug="shop" number="14" /></MemoryRouter></QueryClientProvider>)
  try {
    expect(view.el.textContent).toContain('Follows main, set by Sam · settings revision 3')
    expect(view.el.textContent).toContain('Landing approved byPriya')
  } finally { act(() => view.root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

// The preview door never implies replacing a standing deployment's code.
it.each([true, false])('labels the exact-version preview as a preview (live=%s)', isLive => {
  const client = newQueryClient()
  const h = render(<QueryClientProvider client={client}><MemoryRouter><OpenThisVersion slug="shop" number={7} isLive={isLive} /></MemoryRouter></QueryClientProvider>)
  expect(h.el.querySelector('button')?.textContent).toBe('▶ Preview this version')
  act(() => h.root.unmount()); client.clear()
})

it('links the release record to each deployment where its bundle ran', async () => {
  const history = [
    { bundleId: B14, deployment: 'production', state: 'released', serving: true },
    { bundleId: B14, deployment: 'staging', state: 'released', serving: false },
    { bundleId: B15, deployment: 'other', state: 'released', serving: true },
  ]
  const detail = { release: { number: 14, bundleId: B14, state: 'released', createdAt: '2026-11-06T01:00:00Z' }, changes: [], events: [] }
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith('/releases') ? history : detail))))
  const client = newQueryClient()
  const h = render(<QueryClientProvider client={client}><MemoryRouter><ReleasePermalinkPage slug="shop" number="14" /></MemoryRouter></QueryClientProvider>)
  try {
    await vi.waitFor(() => expect(h.el.querySelector('a[href="/projects/shop/deployments/production"]')).not.toBeNull())
    expect(h.el.querySelector('a[href="/projects/shop/deployments/staging"]')).not.toBeNull()
    expect(h.el.querySelector('a[href="/projects/shop/deployments/other"]')).toBeNull()
    await vi.waitFor(() => expect(h.el.textContent).toContain('Running here'))
    expect(h.el.textContent).toContain('Has run here')
  } finally { act(() => h.root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
