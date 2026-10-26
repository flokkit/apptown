// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider, focusManager } from '@tanstack/react-query'
import { newQueryClient, getKey } from './data'
import { settled } from './settled'
import { AttentionBadge } from './attention'
import AttentionPage from './pages/Attention'
import Dashboard from './pages/Dashboard'
import TopBar from './TopBar'
import App from './App'
import type { AttentionView } from './wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const at = '2026-10-26T00:00:00Z'
const known: AttentionView = { items: [], sources: [{ key: 'workers', name: 'Worker availability', observedAt: at, unknown: false, stale: false }] }
const warning: AttentionView = { ...known, items: [
  { key: 'workers/a', title: 'Worker unavailable', detail: 'Work is waiting.', href: '/infrastructure', severity: 'warning', observedAt: at, stale: false },
  { key: 'protection/a', title: 'Recovery is untested', detail: 'No recovery evidence yet.', href: '/projects/game/settings/data', severity: 'advisory', observedAt: at, stale: false },
] }
const emptyDashboard = { pendingApprovals: [], activeRuns: [], recentAdrs: [], endedOperations: [] }
const emptyActivity = { agents: [], active: [], history: [], deployments: [] }
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const pathOf = (url: unknown) => String(url).replace(/^.*\/api\/v1/, '')
const mounted: { close: () => void }[] = []
afterEach(() => { for (const m of mounted.splice(0)) m.close(); vi.useRealTimers(); vi.unstubAllGlobals(); focusManager.setFocused(undefined) })
function mount(ui: ReactNode, path = '/attention') {
  const client = newQueryClient(); const el = document.createElement('div'); document.body.append(el); const root = createRoot(el)
  act(() => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}>{ui}</MemoryRouter></QueryClientProvider>))
  mounted.push({ close: () => { act(() => root.unmount()); client.clear(); el.remove() } })
  return { client, el }
}
function responses(read: (init?: RequestInit) => Response | Promise<Response>, extra: Record<string, unknown> = {}) {
  const calls: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: unknown, init?: RequestInit) => {
    const path = pathOf(url); calls.push(path)
    if (path === '/attention') return read(init)
    if (path === '/auth/me') return json({ mode: 'internal', user: 'alice' })
    if (path === '/authz/me') return json({ instance: { 'instance:updates': false } })
    if (path in extra) return json(extra[path])
    if (path === '/setup') return json({ modelConfigured: true })
    return json({})
  }))
  return calls
}
const badge = (el: HTMLElement) => el.querySelector('a[aria-label^="Attention:"]')!

describe('Attention surfaces share authorized current evidence', () => {
  it('shares one answer and request between narrow topbar and actual page, with actionable severity and links', async () => {
    const calls = responses(() => json(warning))
    const v = mount(<><TopBar me={{ mode: 'internal', user: 'alice' }} narrow /><AttentionPage /></>)
    await settled(v.client)
    expect(calls.filter(p => p === '/attention')).toHaveLength(1)
    expect(v.el.querySelector('.topbar')?.contains(badge(v.el))).toBe(true)
    expect(badge(v.el).getAttribute('aria-label')).toBe('Attention: 2 conditions')
    expect(v.el.querySelectorAll('article')).toHaveLength(2)
    expect(v.el.querySelector('a[href="/infrastructure"]')?.textContent).toBe('Worker unavailable')
    expect(v.el.querySelector('a[href="/projects/game/settings/data"]')?.textContent).toBe('Recovery is untested')
    expect(v.el.querySelectorAll('article .chip.warn')).toHaveLength(1)
    expect(v.el.querySelectorAll('article')[1].textContent).toContain('advisory')
  })
  it.each(['unknown', 'stale', 'failed'] as const)('does not report all-clear for %s empty observations', async mode => {
    responses(() => mode === 'failed' ? json({ error: 'observation read failed' }, 503) : json({ ...known, sources: [{ ...known.sources[0], unknown: mode === 'unknown', stale: mode === 'stale' }] }))
    const v = mount(<><AttentionBadge /><AttentionPage /></>); await settled(v.client)
    expect(badge(v.el).getAttribute('aria-label')).toContain('coverage incomplete')
    expect(v.el.textContent).not.toContain('No current conditions need attention')
    expect(v.el.textContent).toContain(mode === 'failed' ? 'could not be read' : 'not an all-clear')
  })
  it('retains conditions across a failed refresh and clears them only after successful recovery', async () => {
    let answer = () => json(warning)
    responses(() => answer())
    const v = mount(<><AttentionBadge /><AttentionPage /></>); await settled(v.client)
    answer = () => json({ error: 'offline' }, 503)
    await act(async () => { await v.client.refetchQueries({ queryKey: getKey('/attention') }) }); await settled(v.client)
    expect(v.el.querySelectorAll('article')).toHaveLength(2)
    expect(badge(v.el).getAttribute('aria-label')).toContain('2 conditions, coverage incomplete')
    expect(v.el.textContent).toContain('out of date')
    answer = () => json(known)
    await act(async () => { await v.client.refetchQueries({ queryKey: getKey('/attention') }) }); await settled(v.client)
    expect(v.el.querySelectorAll('article')).toHaveLength(0)
    expect(badge(v.el).getAttribute('aria-label')).toBe('Attention: 0 conditions')
    expect(v.el.textContent).toContain('No current conditions need attention')
  })
  it('polls the shared visible answer at thirty seconds and makes no signed-out request', async () => {
    vi.useFakeTimers(); focusManager.setFocused(true)
    const calls = responses(() => json(known))
    const v = mount(<><AttentionBadge /><AttentionPage /></>)
    const advance = async (ms: number) => { await act(async () => { await vi.advanceTimersByTimeAsync(ms) }) }
    for (let i = 0; i < 6; i++) await advance(1)
    expect(calls.filter(p => p === '/attention')).toHaveLength(1)
    await advance(29900); expect(calls.filter(p => p === '/attention')).toHaveLength(1)
    await advance(200); expect(calls.filter(p => p === '/attention')).toHaveLength(2)
    await act(async () => { v.client.setQueryData(getKey('/auth/me'), { mode: 'internal' }) })
    await advance(1)
    const before = calls.length; await advance(61000)
    expect(calls.slice(before)).not.toContain('/attention')
    expect(v.el.textContent).not.toContain('No current conditions need attention')
  })
  it('actual shell identity changes cancel old reads, discard late old-scope answers and remove sign-out count', async () => {
    let who = 'alice'; let hold = false; let release: (r: Response) => void = () => {}; let signal: AbortSignal | null | undefined
    vi.stubGlobal('fetch', vi.fn(async (url: unknown, init?: RequestInit) => {
      const path = pathOf(url)
      if (path === '/auth/me') return json({ mode: 'internal', user: who || undefined })
      if (path === '/authz/me') return json({ instance: { 'instance:updates': false } })
      if (path === '/attention') {
        if (hold) { hold = false; signal = init?.signal; return new Promise<Response>(resolve => { release = resolve }) }
        return json(who === 'alice' ? warning : known)
      }
      return json({})
    }))
    const v = mount(<App />); await settled(v.client)
    expect(badge(v.el).getAttribute('aria-label')).toBe('Attention: 2 conditions')
    hold = true
    let pending: Promise<void> = Promise.resolve()
    act(() => { pending = v.client.refetchQueries({ queryKey: getKey('/attention') }) })
    who = 'bob'
    await act(async () => { v.client.setQueryData(getKey('/auth/me'), { mode: 'internal', user: who }); await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(signal?.aborted).toBe(true)
    await settled(v.client)
    await act(async () => { release(json(warning)); await pending }); await settled(v.client)
    expect(v.el.textContent).not.toContain('Worker unavailable')
    expect(badge(v.el).getAttribute('aria-label')).toBe('Attention: 0 conditions')
    who = ''
    await act(async () => { v.client.setQueryData(getKey('/auth/me'), { mode: 'internal' }); await new Promise(resolve => setTimeout(resolve, 0)) }); await settled(v.client)
    expect(v.el.querySelector('a[aria-label^="Attention:"]')).toBeNull()
    expect(v.client.getQueryData(getKey('/attention'))).toBeUndefined()
  })
  it.each(['failed', 'unknown', 'stale', 'known'] as const)('Home quietness includes %s operational coverage even when all other groups are empty', async mode => {
    responses(() => mode === 'failed' ? json({ error: 'no observation' }, 503) : json({ ...known, sources: [{ ...known.sources[0], unknown: mode === 'unknown', stale: mode === 'stale' }] }), {
      '/activity': emptyActivity, '/dashboard': emptyDashboard,
      '/projects?recent=6': [{ id: 'project', slug: 'game', name: 'Game' }],
    })
    const v = mount(<Dashboard />, '/'); await settled(v.client)
    const lead = v.el.querySelector('.dash-lead')?.textContent
    expect(lead).toBeTruthy()
    if (mode === 'known') expect(lead).toContain('All quiet')
    else expect(lead).not.toContain('All quiet')
  })
  it('adds Operations without replacing Home approvals and blocked work, and refuses all-quiet on missing operations', async () => {
    const active = [{ runId: 'run-blocked', runNumber: 1, changeId: 'change-blocked', changeNumber: 1, workflow: 'implement', title: 'Blocked change', status: 'blocked', stage: 'gate', state: 'blocked', detail: 'Needs repair', project: 'game', startedAt: at, durationS: 10 }]
    responses(() => json({ error: 'cannot read operations' }, 503), {
      '/activity': { ...emptyActivity, active },
      '/dashboard': { ...emptyDashboard, pendingApprovals: [{ id: 'approval', changeId: 'change-review', changeNumber: 2, runId: 'run-review', itemTitle: 'Review change', projectSlug: 'game', stepName: 'accept' }] },
      '/projects?recent=6': [{ id: 'project', slug: 'game', name: 'Game' }],
    })
    const v = mount(<Dashboard />, '/'); await settled(v.client)
    expect(v.el.textContent).toContain('Operations')
    expect(v.el.textContent).toContain('Needs you (2)')
    expect(v.el.textContent).toContain('Blocked change')
    expect(v.el.textContent).toContain('Review change')
    expect(v.el.textContent).not.toContain('All quiet')
  })
})
