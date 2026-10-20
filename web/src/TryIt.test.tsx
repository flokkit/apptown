// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from './data'
import { TryIt } from './TryIt'

// Record ids are UUIDv7 strings.
const CHANGE = '00000000-0007-7000-8000-000000000000'

async function render(state: object) {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(state))))
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => {
    root.render(<QueryClientProvider client={newQueryClient()}><TryIt changeId={CHANGE} /></QueryClientProvider>)
  })
  await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
  const text = el.textContent ?? ''
  act(() => root.unmount())
  vi.unstubAllGlobals()
  return text
}

describe('the try-it button', () => {
  // Previews on a worker AppTown could not ask: not known, never "cold"
  // or "didn't start", with the reason, and the person may try again.
  it('says when the preview state is not known, and why', async () => {
    const text = await render({ state: 'unavailable', reason: 'the worker could not be asked: connection refused' })
    expect(text).toContain('preview state not known')
    expect(text).toContain('connection refused')
    expect(text).toContain('Try again')
    expect(text).not.toContain("didn't start")
  })
  // A live preview AppTown says answers on the AppTown host only is not a
  // link that fails elsewhere: the page says where it works.
  it('says where a host-only preview answers instead of opening it', async () => {
    const text = await render({ state: 'live', url: 'http://forge.example:40001', hostOnly: true })
    expect(text).toContain("on AppTown's host only, at localhost:40001")
    expect(text).not.toContain('Open')
  })
  it('opens a preview that answers at its address', async () => {
    const text = await render({ state: 'live', url: 'https://change-7.preview.example' })
    expect(text).toContain('Open')
    expect(text).not.toContain("AppTown's host only")
  })
  it('offers the preview when it is cold', async () => {
    const text = await render({ state: 'cold' })
    expect(text).toContain('Try it')
    expect(text).not.toContain('not known')
  })
  // A preview built from the change's working files never claims a commit;
  // one booted from a commit names it (review F1).
  it('says what the running preview was built from', async () => {
    const folder = await render({ state: 'live', url: 'https://change-1.preview.example.com', built: '2026-10-21T09:00:00Z' })
    expect(folder).toContain("built from the change's working files at")
    expect(folder).not.toContain('commit')
    const commit = await render({ state: 'live', url: 'https://change-1.preview.example.com', built: '2026-10-21T09:00:00Z', commit: 'abcdef1234567' })
    expect(commit).toContain('built from commit abcdef1 at')
  })
  // Open asks AppTown for the preview first, so what opens is the files as
  // they are now, never an older build at the same address.
  it('asks AppTown before opening a running preview', async () => {
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      calls.push(`${init?.method ?? 'GET'} ${url}`)
      return new Response(JSON.stringify({ state: 'live', url: 'https://change-1.preview.example.com', built: '2026-10-21T09:00:00Z' }))
    }))
    vi.stubGlobal('open', vi.fn(() => ({ document: { write() {} }, location: { href: '' }, close() {} })))
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={newQueryClient()}><TryIt changeId={CHANGE} /></QueryClientProvider>)
    })
    await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
    const open = [...el.querySelectorAll('button')].find((b) => b.textContent?.includes('Open'))
    await act(async () => { open?.click(); await new Promise((r) => setTimeout(r, 0)) })
    act(() => root.unmount())
    vi.unstubAllGlobals()
    expect(calls.some((c) => c.startsWith('POST') && c.includes(`/changes/${CHANGE}/preview`))).toBe(true)
  })
})
