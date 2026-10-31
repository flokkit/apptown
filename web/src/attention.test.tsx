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
import App, { SidebarNav } from './App'
import type { AttentionView } from './wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const at = '2026-10-26T00:00:00Z'
const known: AttentionView = { items: [], sources: [{ key: 'workers', name: 'Worker availability', observedAt: at, unknown: false, stale: false }] }
const warning: AttentionView = { ...known, items: [
  { key: 'workers/a', title: 'Worker unavailable', detail: 'Work is waiting.', href: '/infrastructure', statusLabel: 'waiting', severity: 'warning', observedAt: at, stale: false },
  { key: 'protection/a', title: 'Recovery is untested', detail: 'No recovery evidence yet.', href: '/projects/game/settings/data', statusLabel: 'waiting', severity: 'advisory', observedAt: at, stale: false },
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
const badge = (el: HTMLElement) => el.querySelector('a.attention-badge')!

describe('Attention surfaces share authorized current evidence', () => {
  it('shares one answer and request between narrow topbar and actual page, with actionable severity and links', async () => {
    const calls = responses(() => json(warning))
    const v = mount(<><TopBar me={{ mode: 'internal', user: 'alice' }} narrow /><AttentionPage /></>)
    await settled(v.client)
    expect(calls.filter(p => p === '/attention')).toHaveLength(1)
    expect(v.el.querySelector('.topbar')?.contains(badge(v.el))).toBe(true)
    expect(badge(v.el).getAttribute('aria-label')).toBe('2 need attention')
    expect(badge(v.el).querySelector('.attention-words')?.textContent).toBe('2 need attention')
    expect(badge(v.el).querySelector('.attention-compact')?.textContent).toBe('⚠ 2')
    expect(v.el.querySelectorAll('article')).toHaveLength(2)
    expect(v.el.querySelector('a[href="/infrastructure"]')?.textContent).toBe('Worker unavailable')
    expect(v.el.querySelector('a[href="/projects/game/settings/data"]')?.textContent).toBe('Recovery is untested')
    expect(v.el.querySelectorAll('article .chip.pending')).toHaveLength(2)
    expect(v.el.querySelectorAll('article')[1].textContent).toContain('waiting')
  })
  it.each(['unknown', 'stale', 'failed'] as const)('does not report all-clear for %s empty observations', async mode => {
    responses(() => mode === 'failed' ? json({ error: 'observation read failed' }, 503) : json({ ...known, sources: [{ ...known.sources[0], unknown: mode === 'unknown', stale: mode === 'stale' }] }))
    const v = mount(<><AttentionBadge /><AttentionPage /></>); await settled(v.client)
    expect(badge(v.el).getAttribute('aria-label')).toBe(mode === 'failed' ? 'Attention unknown' : '0 need attention; 1 not known')
    expect(v.el.textContent).not.toContain('No current conditions need attention')
    expect(v.el.textContent).toContain(mode === 'failed' ? 'failed' : 'Some checks are still unknown.')
  })
  it('retains conditions across a failed refresh and clears them only after successful recovery', async () => {
    let answer = () => json(warning)
    responses(() => answer())
    const v = mount(<><AttentionBadge /><AttentionPage /></>); await settled(v.client)
    answer = () => json({ error: 'offline' }, 503)
    await act(async () => { await v.client.refetchQueries({ queryKey: getKey('/attention') }) }); await settled(v.client)
    expect(v.el.querySelectorAll('article')).toHaveLength(2)
    expect(badge(v.el).getAttribute('aria-label')).toBe('2 need attention; 1 not known')
    expect(v.el.textContent).toContain('unknown')
    answer = () => json(known)
    await act(async () => { await v.client.refetchQueries({ queryKey: getKey('/attention') }) }); await settled(v.client)
    expect(v.el.querySelectorAll('article')).toHaveLength(0)
    expect(badge(v.el)).toBeNull()
    expect(v.el.textContent).not.toContain('Coverage incomplete')
  })
  it('shows known conditions beside incomplete host coverage in the topbar', async () => {
    responses(() => json({ ...warning, sources: [{ ...known.sources[0], unknown: true }] }))
    const v = mount(<AttentionBadge />); await settled(v.client)
    expect(badge(v.el).querySelector('.attention-words')?.textContent).toBe('2 · 1 not known')
    expect(badge(v.el).querySelector('.attention-compact')?.textContent).toBe('⚠ 2 · 1 not known')
    expect(badge(v.el).getAttribute('href')).toBe('/attention')
    expect(badge(v.el).getAttribute('aria-label')).toBe('2 need attention; 1 not known')
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
    expect(badge(v.el).getAttribute('aria-label')).toBe('2 need attention')
    hold = true
    let pending: Promise<void> = Promise.resolve()
    act(() => { pending = v.client.refetchQueries({ queryKey: getKey('/attention') }) })
    who = 'bob'
    await act(async () => { v.client.setQueryData(getKey('/auth/me'), { mode: 'internal', user: who }); await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(signal?.aborted).toBe(true)
    await settled(v.client)
    await act(async () => { release(json(warning)); await pending }); await settled(v.client)
    expect(v.el.textContent).not.toContain('Worker unavailable')
    expect(badge(v.el)).toBeNull()
    who = ''
    await act(async () => { v.client.setQueryData(getKey('/auth/me'), { mode: 'internal' }); await new Promise(resolve => setTimeout(resolve, 0)) }); await settled(v.client)
    expect(v.el.querySelector('a.attention-badge')).toBeNull()
    expect(v.client.getQueryData(getKey('/attention'))).toBeUndefined()
  })
  it.each(['failed', 'unknown', 'stale', 'known'] as const)('Home quietness includes %s operational coverage even when all other groups are empty', async mode => {
    responses(() => mode === 'failed' ? json({ error: 'no observation' }, 503) : json({ ...known, sources: [{ ...known.sources[0], unknown: mode === 'unknown', stale: mode === 'stale' }] }), {
      '/activity': emptyActivity, '/dashboard': emptyDashboard,
      '/projects?recent=6': [{ id: 'project', slug: 'game', name: 'Game', productionUrl: 'https://game.example.test', deployed: true, hasDeployments: true, observation: 'unreachable', targetName: 'Club host', observationStale: false }],
      '/projects/game/deployments': [],
    })
    const v = mount(<Dashboard />, '/'); await settled(v.client)
    const lead = v.el.querySelector('.dash-lead')?.textContent
    if (mode === 'known') expect(lead).toContain('All quiet')
    else expect(lead).not.toContain('All quiet')
  })
  it('keeps live apps visible without calling incomplete Attention a missing server reading', async () => {
    responses(() => json({ ...known, sources: [
      { key: 'disks', name: 'AppTown and worker disks', unknown: false, stale: false },
      { key: 'capacity', name: 'Deployment host capacity', unknown: true, stale: false },
    ] }), {
      '/activity': { ...emptyActivity, deployments: [{ project: 'game', name: 'main', url: 'https://game.example.test', running: true, observationStale: false }] },
      '/dashboard': emptyDashboard, '/projects?recent=6': [{ id: 'project', slug: 'game', name: 'Game', hasDeployments: true }],
      '/projects/game/deployments': [],
    })
    const v = mount(<><AttentionBadge /><Dashboard /></>, '/'); await settled(v.client)
    const lead = v.el.querySelector('.dash-lead')?.textContent
    expect(lead).toBe('1 app live →')
    expect(v.el.textContent).not.toContain('All quiet')
    expect(badge(v.el)).not.toBeNull()
  })
  it('keeps Home approvals and blocked work, and refuses all-quiet on missing host observations', async () => {
    const active = [{ runId: 'run-blocked', runNumber: 1, changeId: 'change-blocked', changeNumber: 1, workflow: 'implement', title: 'Blocked change', status: 'blocked', stage: 'gate', state: 'blocked', detail: 'Needs repair', project: 'game', startedAt: at, durationS: 10 }]
    responses(() => json({ error: 'cannot read operations' }, 503), {
      '/activity': { ...emptyActivity, active },
      '/dashboard': { ...emptyDashboard, pendingApprovals: [{ id: 'approval', changeId: 'change-review', changeNumber: 2, runId: 'run-review', itemTitle: 'Review change', projectSlug: 'game', stepName: 'accept' }] },
      '/projects?recent=6': [{ id: 'project', slug: 'game', name: 'Game', productionUrl: 'https://game.example.test', deployed: true, hasDeployments: true, observation: 'unreachable', targetName: 'Club host', observationStale: false }],
      '/projects/game/deployments': [],
    })
    const v = mount(<Dashboard />, '/'); await settled(v.client)
    expect(v.el.textContent).not.toContain('server status not available')
    expect(v.el.textContent).not.toContain('Operations')
    expect(v.el.textContent).toContain('Needs you (2)')
    expect(v.el.textContent).toContain('Blocked change')
    expect(v.el.textContent).toContain('Review change')
    expect(v.el.textContent).not.toContain('All quiet')
  })
})

it.each([[true, true], [true, false], [false, true], [false, false]])('Home keeps unreachable visible with production %s on phone %s', async (production, narrow) => {
 vi.stubGlobal('matchMedia', () => ({ matches: narrow, addEventListener() {}, removeEventListener() {} }))
 responses(() => json(known), {
  '/activity': emptyActivity, '/dashboard': emptyDashboard,
  '/projects?recent=6': [{ id: 'project', slug: 'game', name: 'Game', productionUrl: production ? 'https://game.example.test' : '', deployed: production, hasDeployments: true, observation: 'unreachable', targetName: 'Club host', observationStale: false }],
  '/projects/game/deployments': [{ id: 'preview-id', name: 'preview-machine-id', displayLabel: 'Preview', url: 'https://preview.example.test', kind: 'preview', running: null, observation: 'unreachable', targetName: 'Club host', observationStale: false }],
 })
 const v = mount(<Dashboard />, '/'); await settled(v.client)
 if (production && !narrow) expect(v.el.querySelector('.dash-app')?.getAttribute('href')).toBe('https://game.example.test')
 else expect(v.el.querySelector('.dash-app')).toBeNull()
 if (narrow) expect(v.el.querySelector('.dash-projects')?.textContent).not.toContain('Details')
 expect(v.el.querySelector('.dash-projects')?.textContent).not.toContain('Not deployed')
 expect(v.el.querySelector('.dash-projects')?.textContent).toContain('No changes yet')
 expect(v.el.textContent).not.toContain('preview-machine-id')
 expect(v.el.querySelector('.chip.fail')?.textContent).toContain('unreachable')
 expect(v.el.querySelectorAll('.dash-projects .chip')).toHaveLength(1)
})

it.each([false, true])('Home names each finished change before its project, state and age (phone=%s)', async narrow => {
 vi.stubGlobal('matchMedia', () => ({ matches: narrow, addEventListener() {}, removeEventListener() {} }))
 responses(() => json(known), {
  '/activity': { ...emptyActivity, history: [
   { changeId: 'calendar', title: 'Add a shared calendar', project: 'Household', statusLabel: 'landed', when: at },
   { changeId: 'photos', title: 'Fix photo uploads', project: 'Household', statusLabel: 'failed', when: at },
  ] },
  '/dashboard': emptyDashboard,
  '/projects?recent=6': [{ id: 'project', slug: 'household', name: 'Household', hasDeployments: false }],
 })
 const v = mount(<Dashboard />, '/'); await settled(v.client)
 for (const [id, title, state] of [['calendar', 'Add a shared calendar', 'landed'], ['photos', 'Fix photo uploads', 'failed']]) {
  const row = v.el.querySelector(`a[href="/changes/${id}"]`)!
  expect(row?.querySelector('strong')?.textContent).toBe(title)
  expect(row?.querySelector('.small')?.textContent).toMatch(new RegExp(`^Household · ${state} · .+ago$`))
  expect(row?.firstElementChild?.tagName).toBe('STRONG')
 }
})

it('keeps pending attention distinct from a failed read', async () => {
 let release!: (r: Response) => void
 responses(() => new Promise<Response>(resolve => { release = resolve }))
 const v = mount(<AttentionBadge />)
 await act(async () => { await new Promise(resolve => setTimeout(resolve, 10)) })
 expect(badge(v.el).querySelector('.attention-words')?.textContent).toBe('Attention …')
 await act(async () => { release(json({ error: 'unavailable' }, 403)) }); await settled(v.client)
 expect(badge(v.el).querySelector('.attention-words')?.textContent).toBe('Attention unknown')
})
it.each([false, true])('search has its own visible control (narrow=%s)', async narrow => {
 responses(() => json(known))
 const v = mount(<TopBar me={{ mode: 'internal', user: 'alice' }} narrow={narrow} />)
 await settled(v.client)
 const search = v.el.querySelector('summary[aria-label="Search"]')!
 expect(search).not.toBeNull()
 expect(search.closest('details')).not.toBe(v.el.querySelector('summary[aria-label="Account menu"]')?.closest('details'))
 act(() => search.dispatchEvent(new MouseEvent('click', { bubbles: true })))
 expect(v.el.querySelector<HTMLInputElement>('input[aria-label="find"]')?.closest('details')?.open).toBe(true)
})
it('identifies sidebar links while the project name loads and after a failed read', async () => {
 let release!: (r: Response) => void
 vi.stubGlobal('fetch', vi.fn(async (url: unknown) => pathOf(url) === '/projects/club-newsletter'
  ? new Promise<Response>(resolve => { release = resolve }) : json({ instance: {} })))
 const v = mount(<SidebarNav />, '/projects/club-newsletter')
 expect(v.el.querySelector('.sidebar-project')?.textContent).toBe('club-newsletter')
 await act(async () => { release(json({ error: 'forbidden' }, 403)) }); await settled(v.client)
 expect(v.el.querySelector('.sidebar-project')?.textContent).toBe('club-newsletter')
})

it('hides the topbar badge when no known condition needs attention', async () => {
 responses(() => json(known))
 const v = mount(<AttentionBadge />); await settled(v.client)
 expect(v.el.querySelector('a')).toBeNull()
})
it.each([0, 2])('keeps %s known conditions beside unknown source coverage', async count => {
 responses(() => json({ items: warning.items.slice(0, count), sources: [...known.sources, { key: 'protection/game', name: 'Data protection', unknown: true, stale: true }] }))
 const v = mount(<AttentionBadge />); await settled(v.client)
 expect(badge(v.el).textContent).toContain(`${count} · 1 not known`)
 expect(badge(v.el).getAttribute('href')).toBe('/attention')
 expect(badge(v.el).getAttribute('aria-label')).toBe(`${count} need attention; 1 not known`)
})
