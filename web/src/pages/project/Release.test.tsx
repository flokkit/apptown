import { enabledFeatures } from '../../features.fixture'
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

// The deployment attempt id in a log line is for administrators: the words
// stay, and the id shows on its own line only with the technical switch.
it.each([false, true])('shows a log line\'s deployment attempt id only with the technical switch (%s)', async technical => {
  const user = `release-log-viewer-${technical}`
  if (technical) localStorage.setItem(`apptown.technical-detail.${user}`, '1')
  const client = newQueryClient()
  client.setQueryData(getKey('/auth/me'), { mode: 'internal', user })
  client.setQueryData(getKey('/projects/shop/releases/14'), {
    release: { id: B14, number: 14, state: 'released', deployment: 'main', createdAt: '2026-10-13T04:34:46Z', releasedAt: '2026-10-13T04:34:46Z' },
    isLive: false, changes: [], events: [{ at: '2026-10-13T04:38:45Z', text: `deployment attempt ${B15}, released to main` }],
  })
  const el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><ReleasePermalinkPage slug="shop" number="14" log /></MemoryRouter></QueryClientProvider>) })
    expect(el.textContent).toContain('released to main')
    expect(el.textContent?.includes(`deployment attempt ${B15}`)).toBe(technical)
    expect(el.textContent?.includes(B15)).toBe(technical)
  } finally { act(() => root.unmount()); client.clear(); localStorage.clear() }
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
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><ReleasePermalinkPage slug="shop" number="14" log /></MemoryRouter></QueryClientProvider>) })
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(el.querySelector('h1')?.textContent).toBe('Release log')
    expect(el.textContent).not.toContain(note)
    await act(async () => { await vi.advanceTimersByTimeAsync(8500) })
    expect(el.textContent).toContain(note)
    expect(el.querySelector('h1')?.textContent).toBe('Release log')
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
  const page = (number: string) => <QueryClientProvider client={client}><MemoryRouter><ReleasePermalinkPage slug="shop" number={number} log /></MemoryRouter></QueryClientProvider>
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
    expect(link?.getAttribute('href')).toBe(`/backups#backup-${B3}`)
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
  client.setQueryData(getKey('/features'), enabledFeatures)
  const h = render(<QueryClientProvider client={client}><MemoryRouter><OpenThisVersion slug="shop" number={7} isLive={isLive} /></MemoryRouter></QueryClientProvider>)
  expect(h.el.querySelector('button')?.textContent).toBe('▶ Preview this version')
  act(() => h.root.unmount()); client.clear()
})

it('links the release record to each deployment where its bundle ran', async () => {
  const history = [
    { number: 14, bundleId: B14, deployment: 'production', state: 'released', serving: true },
    { number: 14, bundleId: B14, deployment: 'staging', state: 'released', serving: false },
    { number: 15, bundleId: B15, deployment: 'other', state: 'released', serving: true },
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

// Release 23 and release 16 can ship one build; a deployment running 16 does
// not run release 23, so its row names the release it runs.
it('names the release a deployment runs when it serves the same build under another number', async () => {
  const history = [
    { number: 23, bundleId: B14, deployment: 'staging', state: 'released', serving: true },
    { number: 16, bundleId: B14, deployment: 'production', deploymentLabel: 'Production', state: 'released', serving: true },
  ]
  const detail = { release: { number: 23, bundleId: B14, state: 'released', createdAt: '2026-11-06T01:00:00Z' }, changes: [], events: [] }
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith('/releases') ? history : detail))))
  const client = newQueryClient()
  const h = render(<QueryClientProvider client={client}><MemoryRouter><ReleasePermalinkPage slug="shop" number="23" /></MemoryRouter></QueryClientProvider>)
  try {
    await vi.waitFor(() => expect(h.el.querySelector('a[href="/projects/shop/releases/16"]')).not.toBeNull())
    const rows = [...h.el.querySelectorAll('.release-record p')].map(p => p.textContent)
    expect(rows).toContain('staging · Running here')
    expect(rows).toContain('Production · Runs the same build as release 16')
  } finally { act(() => h.root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

// A rollback deploys an earlier release's build again: the page names the
// release it brings back, never as if that release were the one undone.
it('says a rollback deploys the earlier release again', async () => {
  const client = newQueryClient()
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ release: { id: B14, number: 4, state: 'released', createdAt: '2026-11-09T00:00:00Z', rollbackOf: B3 }, rollbackNumber: 2, changes: [], events: [] }))))
  const view = render(<QueryClientProvider client={client}><MemoryRouter><ReleasePermalinkPage slug="shop" number="4" /></MemoryRouter></QueryClientProvider>)
  try {
    await vi.waitFor(() => expect(view.el.textContent).toContain('Deploys release 2 again.'))
    expect(view.el.textContent).not.toContain('Rollback of')
    expect(view.el.querySelector('a[href="/projects/shop/releases/2"]')).not.toBeNull()
  } finally { act(() => view.root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

// An old release a later one replaced is not shown with the same green
// "deployed" as the release running now.
it('marks a release a later one replaced, naming it', async () => {
  const client = newQueryClient()
  const body = { release: { id: B14, number: 1, state: 'released', deployment: 'main', createdAt: '2026-11-05T00:00:00Z' }, statusLabel: 'deployed', isLive: false, replacedBy: 16, changes: [], events: [] }
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(body))))
  const view = render(<QueryClientProvider client={client}><MemoryRouter><ReleasePermalinkPage slug="shop" number="1" /></MemoryRouter></QueryClientProvider>)
  try {
    await vi.waitFor(() => expect(view.el.querySelector('.release-summary .chip')).not.toBeNull())
    const chip = view.el.querySelector('.release-summary .chip')!
    expect(chip.textContent).toContain('replaced')
    expect(chip.classList.contains('pass')).toBe(false)
    expect(chip.getAttribute('data-help')).toBe('Release 16 replaced it on this deployment.')
  } finally { act(() => view.root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

// The page's own heading names the release (Project draws it); the record
// below does not repeat it, and keeps its deployment, state and badges.
it('does not repeat the release number under the page heading', async () => {
  const client = newQueryClient()
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ release: { id: B14, number: 14, state: 'released', deployment: 'main', createdAt: '2026-11-05T00:00:00Z' }, deploymentLabel: 'Production', statusLabel: 'released', isLive: true, changes: [], events: [] }))))
  const view = render(<QueryClientProvider client={client}><MemoryRouter><ReleasePermalinkPage slug="shop" number="14" /></MemoryRouter></QueryClientProvider>)
  try {
    await vi.waitFor(() => expect(view.el.querySelector('.release-summary')).not.toBeNull())
    expect(view.el.textContent).not.toContain('Release #14')
    const badges = [...view.el.querySelectorAll('.release-summary .badge')].map(b => b.textContent)
    expect(badges).toEqual(['Production', 'current release'])
  } finally { act(() => view.root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
