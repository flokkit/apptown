// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { settled } from '../settled'
import { ReleaseAvailabilityNote, UpdatesPage } from './Updates'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const status = {
  build: 'custom', platform: 'linux/amd64', channel: 'stable', state: 'available',
  lastAttempt: '2026-10-26T00:01:00Z', lastSuccess: '2026-10-26T00:01:00Z',
  automaticReplacement: false, replacementReason: 'Automatic replacement is not qualified.',
  candidate: { version: 'v1.2.0', commit: 'a'.repeat(40), notes: '# Signed notes\n<script>window.pwned=1</script>\n[unsafe](javascript:alert(1))', archive: { name: 'release.tar.gz', sha256: 'b'.repeat(64), size: 42 } },
}

describe('authenticated update notifications', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })
  async function mount(allowed: boolean, state = 'available', holdingsFailure = false) {
    const calls: { path: string; method: string }[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ path: String(url), method: init?.method ?? 'GET' })
      if (String(url).includes('/authz/me')) return new Response(JSON.stringify(holdingsFailure ? { error: 'unavailable' } : { instance: { 'instance:updates': allowed } }), { status: holdingsFailure ? 503 : 200 })
      return new Response(JSON.stringify({ ...status, state }))
    }))
    const client = newQueryClient()
    const el = document.createElement('div'); const root = createRoot(el)
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><ReleaseAvailabilityNote /><UpdatesPage /></MemoryRouter></QueryClientProvider>))
    await settled(client)
    return { el, client, calls, close: async () => { await act(async () => root.unmount()); client.clear() } }
  }
  it('does not read status or show notification without affirmative holdings', async () => {
    for (const failure of [false, true]) {
      const view = await mount(false, 'available', failure)
      try {
        expect(view.calls.every(c => c.path.includes('/authz/me'))).toBe(true)
        expect(view.el.textContent).not.toContain('v1.2.0')
        expect(view.el.querySelector('button')).toBeNull()
      } finally { await view.close() }
    }
  })
  it('shows verified availability, safe notes and separate manual check without replacement control', async () => {
    const view = await mount(true)
    try {
      expect(view.el.querySelector('a[href="/updates"]')).not.toBeNull()
      expect(view.el.textContent).toContain('Signed notes')
      expect(view.el.querySelector('script')).toBeNull()
      expect(view.el.querySelector('a[href^="javascript:"]')).toBeNull()
      expect(view.el.textContent).toContain('Automatic replacement is not qualified.')
      expect(view.calls.every(c => c.method === 'GET')).toBe(true)
      await act(async () => view.el.querySelector<HTMLButtonElement>('button')!.click())
      await settled(view.client)
      expect(view.calls.filter(c => c.method === 'POST').map(c => c.path)).toEqual(['/api/v1/instance-updates/check'])
      expect([...view.el.querySelectorAll('button')].map(b => b.textContent)).toEqual(['Check for updates'])
    } finally { await view.close() }
  })
  it('does not turn stale evidence into a current availability notification', async () => {
    const view = await mount(true, 'stale')
    try {
      expect(view.el.querySelector('a[href="/updates"]')).toBeNull()
      expect(view.el.textContent).toContain('Release information is stale')
      expect(view.el.textContent).toContain('v1.2.0')
      expect(view.calls.every(c => c.method === 'GET')).toBe(true)
    } finally { await view.close() }
  })
  it('observes check completion and suppresses retained availability after failed local refresh', async () => {
    vi.useFakeTimers()
    let phase = 'checking'
    const methods: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      methods.push(init?.method ?? 'GET')
      if (String(url).includes('/authz/me')) return new Response(JSON.stringify({ instance: { 'instance:updates': true } }))
      return new Response(JSON.stringify(phase === 'failed' ? { error: 'retained evidence unreadable' } : { ...status, state: phase }), { status: phase === 'failed' ? 503 : 200 })
    }))
    const client = newQueryClient()
    const el = document.createElement('div'); const root = createRoot(el)
    const advance = async (ms: number) => { await act(async () => { await vi.advanceTimersByTimeAsync(ms) }) }
    try {
      await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><ReleaseAvailabilityNote /><UpdatesPage /></MemoryRouter></QueryClientProvider>))
      await advance(1); await advance(1)
      expect(el.textContent).toContain('Checking authenticated release information')
      expect(el.querySelector<HTMLButtonElement>('button')!.disabled).toBe(true)
      phase = 'available'
      await advance(5_000); await advance(1)
      expect(el.querySelector<HTMLButtonElement>('button')!.disabled).toBe(false)
      expect(el.querySelector('a[href="/updates"]')).not.toBeNull()
      phase = 'failed'
      await advance(5_000); await advance(1)
      expect(el.querySelector('a[href="/updates"]')).toBeNull()
      expect(el.textContent).toContain('retained evidence unreadable')
      expect(methods.every(method => method === 'GET')).toBe(true)
    } finally { await act(async () => root.unmount()); client.clear() }
  })

})
