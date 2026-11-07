import { withFeatures } from './features.fixture'
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { QueryClientProvider, focusManager } from '@tanstack/react-query'
import { newQueryClient, getKey } from './data'
import { settled } from './settled'
import { attentionState, AttentionLights } from './attention'
import AttentionPage from './pages/Attention'
import Dashboard, { homeStatusParts } from './pages/Dashboard'
import { ProjectListRow } from './ProjectListRow'
import { renderToStaticMarkup } from 'react-dom/server'
import type { ProjectRow, InstanceActivity } from './wire.gen'
import TopBar from './TopBar'
import App, { SidebarNav } from './App'
import type { AttentionView } from './wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const at = '2026-10-26T00:00:00Z'
const known: AttentionView = { items: [], sources: [{ href: '/infrastructure', nextStep: 'An administrator can check this worker.', key: 'workers', name: 'Worker availability', observedAt: at, unknown: false, stale: false }] }
const warning: AttentionView = { ...known, items: [
  { key: 'workers/a', title: 'Worker unavailable', detail: 'Work is waiting.', href: '/infrastructure', statusLabel: 'failed', severity: 'warning', observedAt: at, stale: false },
  { key: 'protection/a', title: 'Recovery is untested', detail: 'No recovery evidence yet.', href: '/projects/game/settings/data', statusLabel: 'recovery not rehearsed', severity: 'advisory', observedAt: at, stale: false },
] }
const emptyDashboard = { recentAdrs: [], endedOperations: [] }
const emptyActivity = { waitingOnYou: [], agents: [], active: [], history: [], deployments: [] }
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
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: unknown, init?: RequestInit) => {
    const path = pathOf(url); calls.push(path)
    if (path === '/attention') return read(init)
    if (path === '/auth/me') return json({ mode: 'internal', user: 'alice' })
    if (path === '/authz/me') return json({ instance: { 'instance:updates': false, 'platform:manage-deploy-targets': true } })
    if (path in extra) return json(extra[path])
    if (path === '/setup') return json({ modelConfigured: true })
    return json({})
  })))
  return calls
}
// The lit lights: present only while something is confirmed broken; their label is the count to fix.
const badge = (el: HTMLElement) => el.querySelector('.attention-light:not(.off)') ? el.querySelector('.attention-lights')! : null!

describe('Attention surfaces share authorized current evidence', () => {
  it('shares one answer and request between narrow topbar and actual page, with actionable severity and links', async () => {
    const calls = responses(() => json(warning))
    const v = mount(<><TopBar me={{ mode: 'internal', user: 'alice' }} narrow /><AttentionPage /></>)
    await settled(v.client)
    expect(calls.filter(p => p === '/attention')).toHaveLength(1)
    expect(v.el.querySelector('.topbar')?.contains(badge(v.el))).toBe(true)
    expect(badge(v.el).getAttribute('aria-label')).toBe('2 to fix')
        expect(v.el.querySelectorAll('article')).toHaveLength(2)
    expect(v.el.querySelector('a[href="/infrastructure"]')?.textContent).toBe('Worker unavailable')
    expect(v.el.querySelector('a[href="/projects/game/settings/data"]')?.textContent).toBe('Recovery is untested')
    // Not checked yet is neutral: a fact about AppTown, not a warning.
    expect(v.el.querySelectorAll('article .chip.pending')).toHaveLength(0)
    expect(v.el.querySelectorAll('article')[1].textContent).toContain('recovery not rehearsed')
  })
  it.each(['unknown', 'failed'] as const)('does not report all-clear for %s empty observations', async mode => {
    responses(() => mode === 'failed' ? json({ error: 'observation read failed' }, 503) : json({ ...known, sources: [{ ...known.sources[0], unknown: mode === 'unknown', stale: false }] }))
    const v = mount(<><AttentionLights /><AttentionPage /></>); await settled(v.client)
    expect(badge(v.el)).toBeNull()
    expect(v.el.textContent).not.toContain('No current conditions need attention')
    expect(v.el.textContent).toContain(mode === 'failed' ? 'failed' : 'not known')
  })
  it('retains conditions across a failed refresh and clears them only after successful recovery', async () => {
    let answer = () => json(warning)
    responses(() => answer())
    const v = mount(<><AttentionLights /><AttentionPage /></>); await settled(v.client)
    answer = () => json({ error: 'offline' }, 503)
    await act(async () => { await v.client.refetchQueries({ queryKey: getKey('/attention') }) }); await settled(v.client)
    expect(v.el.querySelectorAll('article')).toHaveLength(2)
    expect(badge(v.el).getAttribute('aria-label')).toBe('2 to fix')
    expect(v.el.textContent).toContain('unknown')
    answer = () => json(known)
    await act(async () => { await v.client.refetchQueries({ queryKey: getKey('/attention') }) }); await settled(v.client)
    expect(v.el.querySelectorAll('article')).toHaveLength(0)
    expect(badge(v.el)).toBeNull()
    expect(v.el.textContent).not.toContain('Coverage incomplete')
  })
  it('keeps every kind in place, grey while nothing of that kind is broken', async () => {
    responses(() => json(known))
    const v = mount(<AttentionLights />); await settled(v.client)
    const lights = [...v.el.querySelectorAll('.attention-light')]
    expect(lights.map(l => l.getAttribute('href'))).toEqual(['/attention#hosts', '/attention#backups', '/attention#integrations', '/attention#checks'])
    expect(lights.every(l => l.classList.contains('off'))).toBe(true)
    expect(lights[0].getAttribute('title')).toBe('Hosts and deployments: no known issues')
    expect(v.el.querySelector('.attention-lights')?.getAttribute('aria-label')).toBe('All quiet')
  })
  it('shows known conditions beside incomplete host coverage in the topbar', async () => {
    responses(() => json({ ...warning, sources: [{ ...known.sources[0], unknown: true }] }))
    const v = mount(<AttentionLights />); await settled(v.client)
    expect(badge(v.el).querySelector('a')!.getAttribute('href')).toMatch(/^\/attention#/)
    expect(badge(v.el).getAttribute('aria-label')).toBe('2 to fix')
  })
  it('polls the shared visible answer at thirty seconds and makes no signed-out request', async () => {
    vi.useFakeTimers(); focusManager.setFocused(true)
    const calls = responses(() => json(known))
    const v = mount(<><AttentionLights /><AttentionPage /></>)
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
    vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: unknown, init?: RequestInit) => {
      const path = pathOf(url)
      if (path === '/auth/me') return json({ mode: 'internal', user: who || undefined })
      if (path === '/authz/me') return json({ instance: { 'instance:updates': false, 'platform:manage-deploy-targets': true } })
      if (path === '/attention') {
        if (hold) { hold = false; signal = init?.signal; return new Promise<Response>(resolve => { release = resolve }) }
        return json(who === 'alice' ? warning : known)
      }
      return json({})
    })))
    const v = mount(<App />); await settled(v.client)
    expect(badge(v.el).getAttribute('aria-label')).toBe('2 to fix')
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
      '/projects?limit=5&order=mine': { projects: [{ id: 'project', slug: 'game', name: 'Game', productionUrl: 'https://game.example.test', deployed: true, hasDeployments: true, status: { word: 'unreachable', reason: '', stale: false }, observation: 'unreachable', targetName: 'Club host', observationStale: false }], total: 1 },
      '/projects/game/deployments': [],
    })
    const v = mount(<Dashboard />, '/'); await settled(v.client)
    const lead = v.el.querySelector('.dash-lead')?.textContent
    if (mode === 'known' || mode === 'stale') expect(lead).toContain('All quiet')
    else expect(lead).not.toContain('All quiet')
  })
  it('keeps live apps visible without calling incomplete Attention a missing server reading', async () => {
    responses(() => json({ ...known, sources: [
      { key: 'disks', name: 'AppTown and worker disks', unknown: false, stale: false },
      { key: 'capacity', name: 'Deployment host capacity', unknown: true, stale: false },
    ] }), {
      '/activity': { ...emptyActivity, deployments: [{ kind: 'standing', projectStatus: { word: 'working', reason: '', stale: false }, project: 'game', name: 'main', url: 'https://game.example.test', running: true, observationStale: false }] },
      '/dashboard': emptyDashboard, '/projects?limit=5&order=mine': { projects: [{ id: 'project', slug: 'game', name: 'Game', hasDeployments: true, status: { word: 'working', reason: '', stale: false } }], total: 1 },
      '/projects/game/deployments': [],
    })
    const v = mount(<><AttentionLights /><Dashboard /></>, '/'); await settled(v.client)
    const lead = v.el.querySelector('.dash-lead')?.textContent
    // A condition that could not be read is listed on Attention, not counted
    // on Home; it still keeps Home from saying All quiet.
    expect(lead).toBe('Apps live1')
    expect(v.el.querySelector('.dash-lead a[href="/attention"]')).toBeNull()
    expect(v.el.querySelector('.dash-lead a[href="/deployments"]')?.textContent).toBe('Apps live1')
    expect(v.el.textContent).not.toContain('All quiet')
    // A check that could not run lights nothing in the top bar.
    expect(badge(v.el)).toBeNull()
  })
  it('renders the server Home partition and count without moving other people’s work', async () => {
    const row = (id: string, kind: string, says: string, you: string) => ({ runId: id, runNumber: 1, changeId: id, changeNumber: 1, workflow: 'work', title: id, status: 'running', stage: 'check', state: 'blocked', project: 'game', startedAt: at, durationS: 10, next: [], waitingOn: { kind, says, you, tone: kind === 'check' ? 'bad' : 'warn', run: id, step: 'review', checks: 1, who: [] } })
    responses(() => json(known), {
      '/activity': { ...emptyActivity, active: [row('Another check', 'check', 'review failed', 'no'), row('Another approval', 'approval', 'waiting for Priya', 'no')], waitingOnYou: [row('My check', 'check', 'review failed', 'yes')] },
      '/dashboard': emptyDashboard,
      '/projects?limit=5&order=mine': { projects: [{ id: 'project', slug: 'game', name: 'Game', hasDeployments: false }], total: 1 }, '/projects/game/deployments': [],
    })
    const v = mount(<Dashboard />, '/'); await settled(v.client)
    const personal = v.el.querySelector('section[aria-label="Waiting on you"]')!
    expect(personal.querySelector('.section-count')?.textContent).toBe('1')
    const workLinks = [...v.el.querySelectorAll('.dash-lead a')]
    expect(workLinks.slice(0, 2).map(a => [a.children[0]?.textContent, a.children[1]?.textContent, a.getAttribute('href')])).toEqual([
      ['Waiting on you', '1', '#waiting-on-you'], ['Other work', '2', '#other-work'],
    ])
    for (const a of workLinks.slice(0, 2)) expect(v.el.querySelector(a.getAttribute('href')!)?.querySelector('section')).not.toBeNull()
    expect(personal.textContent).toContain('My check')
    expect(personal.textContent).toContain('review failed')
    expect(personal.textContent).not.toContain('Another')
    expect(v.el.querySelectorAll('a[href="/changes/My check"]').length).toBe(2)
    expect(v.el.textContent).toContain('Another check')
    expect(v.el.textContent).toContain('waiting for Priya')
  })})

it.each([[true, true], [true, false], [false, true], [false, false]])('Home keeps unreachable visible with production %s on phone %s', async (production, narrow) => {
 vi.stubGlobal('matchMedia', () => ({ matches: narrow, addEventListener() {}, removeEventListener() {} }))
 responses(() => json(known), {
  '/activity': emptyActivity, '/dashboard': emptyDashboard,
  '/projects?limit=5&order=mine': { projects: [{ id: 'project', slug: 'game', name: 'Game', productionUrl: production ? 'https://game.example.test' : '', deployed: production, hasDeployments: true, status: { word: 'unreachable', reason: '', stale: false }, observation: 'unreachable', targetName: 'Club host', observationStale: false }], total: 1 },
  '/projects/game/deployments': [{ id: 'preview-id', name: 'preview-machine-id', displayLabel: 'Preview', url: 'https://preview.example.test', kind: 'preview', running: null, status: { word: 'unreachable', reason: '', stale: false }, observation: 'unreachable', targetName: 'Club host', observationStale: false }],
 })
 const v = mount(<Dashboard />, '/'); await settled(v.client)
 expect(v.el.querySelector('.project-action a')).toBeNull()
 if (narrow) expect(v.el.querySelector('.project-rows')?.textContent).not.toContain('Details')
 expect(v.el.querySelector('.project-rows')?.textContent).not.toContain('Not deployed')
 // A failed attempt was a check: the row must not also say it was never checked.
 expect(v.el.querySelector('.project-rows')?.textContent).not.toContain('Not checked yet')
 expect(v.el.textContent).not.toContain('preview-machine-id')
 expect(v.el.querySelector('.chip.fail')?.textContent).toContain('unreachable')
 expect(v.el.querySelectorAll('.project-rows .chip')).toHaveLength(1)
})

it.each([false, true])('Home lists landed changes by title, project and age, with no status chip (phone=%s)', async narrow => {
 vi.stubGlobal('matchMedia', () => ({ matches: narrow, addEventListener() {}, removeEventListener() {} }))
 responses(() => json(known), {
  '/activity': { ...emptyActivity, history: [
   { changeId: 'calendar', title: 'Add a shared calendar', project: 'Household', statusLabel: 'landed', when: at },
   { changeId: 'photos', title: 'Fix photo uploads', project: 'Household', statusLabel: 'failed', when: at },
  ] },
  '/dashboard': emptyDashboard,
  '/projects?limit=5&order=mine': { projects: [{ id: 'project', slug: 'household', name: 'Household', hasDeployments: false }], total: 1 },
 })
 const v = mount(<Dashboard />, '/'); await settled(v.client)
 // The feed shows landed changes only.
 expect(v.el.querySelector('a[href="/changes/photos"]')).toBeNull()
 const row = v.el.querySelector('a[href="/changes/calendar"]')!
 expect(row?.querySelector('strong')?.textContent).toBe('Add a shared calendar')
 // Every row here landed, so no row repeats it as a status.
 expect(row?.querySelector('.row-status')).toBeNull()
 expect(row?.querySelector('.small')?.textContent).toMatch(/^Household · .+ago$/)
 expect([...row!.children].map(c => c.tagName)).toEqual(['STRONG', 'SPAN'])
})

it('keeps pending attention distinct from a failed read', async () => {
 let release!: (r: Response) => void
 responses(() => new Promise<Response>(resolve => { release = resolve }))
 const v = mount(<AttentionLights />)
 await vi.waitFor(async () => { await act(async () => {}); expect(release).toBeTypeOf('function') })
 expect(badge(v.el)).toBeNull()
 await act(async () => { release(json({ error: 'unavailable' }, 403)) }); await settled(v.client)
 expect(badge(v.el)).toBeNull()
})
it.each([false, true])('search has its own visible control (narrow=%s)', async narrow => {
 responses(() => json(known))
 const v = mount(<TopBar me={{ mode: 'internal', user: 'alice' }} narrow={narrow} />)
 await settled(v.client)
 const search = v.el.querySelector('button[aria-label="Search"]')!
 expect(search).not.toBeNull()
 expect(search.closest('details')).not.toBe(v.el.querySelector('summary[aria-label="Account menu"]')?.closest('details'))
 act(() => search.dispatchEvent(new MouseEvent('click', { bubbles: true })))
 expect(search.getAttribute('aria-expanded')).toBe('true')
 expect(document.getElementById(search.getAttribute('aria-controls')!)?.querySelector('input[aria-label="find"]')).not.toBeNull()
})
it('identifies sidebar links while the project name loads and after a failed read', async () => {
 let release!: (r: Response) => void
 vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: unknown) => pathOf(url) === '/projects/club-newsletter'
  ? new Promise<Response>(resolve => { release = resolve }) : json({ instance: {} }))))
 const v = mount(<SidebarNav />, '/projects/club-newsletter')
 expect(v.el.querySelector('.sidebar-project strong')?.textContent).toBe('club-newsletter')
 await act(async () => { release(json({ error: 'forbidden' }, 403)) }); await settled(v.client)
 expect(v.el.querySelector('.sidebar-project strong')?.textContent).toBe('club-newsletter')
})

it('hides the topbar badge when no known condition needs attention', async () => {
 responses(() => json(known))
 const v = mount(<AttentionLights />); await settled(v.client)
 expect(badge(v.el)).toBeNull()
})
it.each([0, 2])('keeps %s known conditions beside unknown source coverage', async count => {
 responses(() => json({ items: warning.items.slice(0, count), sources: [...known.sources, { key: 'protection/game', name: 'Data protection', unknown: true, stale: true }] }))
 const v = mount(<AttentionLights />); await settled(v.client)
 // Not known lights nothing; only confirmed conditions do.
 if (count) expect(badge(v.el).getAttribute('aria-label')).toBe(`${count} to fix`)
 else expect(badge(v.el)).toBeNull()
})

it('shows retained known failed readings with their age without unknown chips', async () => {
 responses(() => json({ items: [{ ...warning.items[0], title: 'Host: disk check failed', detail: 'The host disk check failed.', observedAt: new Date(Date.now()-30*60000).toISOString(), stale:true, unknown:false }], sources:[{...known.sources[0],stale:true}] }))
 const v=mount(<><AttentionLights/><AttentionPage/></>);await settled(v.client)
 expect(badge(v.el).getAttribute('aria-label')).toBe('1 to fix')
 expect(v.el.textContent).toContain('last seen 30m ago')
 expect(v.el.textContent).not.toContain('not known')
 expect(v.el.querySelector('article .chip.unknown')).toBeNull()
})
it('raises no unknown badge for an old successful reading', async () => {
 responses(() => json({...known,sources:[{...known.sources[0],stale:true}]}))
 const v=mount(<><AttentionLights/><AttentionPage/></>);await settled(v.client)
 expect(badge(v.el)).toBeNull()
 expect(v.el.textContent).not.toContain('not known')
})

it('counts failures across integrations and app checks, groups named rows, and gives unknowns a next step', async () => {
 const items=[...warning.items,{...warning.items[0],key:'integrations/registry',title:'Container registry',href:'/integrations/registry'}, {...warning.items[0],key:'checks/calendar',count:2,title:'Calendar app checks',href:'/conformance/projects/calendar'}]
 responses(()=>json({items,sources:[{key:'capacity/host',name:'Attached host',href:'/infrastructure/attached-host',unknown:true,stale:false,observedAt:at,reason:'Memory use is unavailable.',nextStep:'Ask an administrator to check host diagnostics.'}]}))
 const v=mount(<><AttentionLights/><AttentionPage/></>);await settled(v.client)
 expect(badge(v.el).getAttribute('aria-label')).toBe('4 to fix')
 for(const group of ['Hosts and deployments','Backups','Integrations','App checks']) expect(v.el.querySelector(`section[aria-label="${group}"]`)).not.toBeNull()
 expect(v.el.textContent).toContain('Memory use is unavailable.')
 expect(v.el.textContent).toContain('Ask an administrator to check host diagnostics.')
 expect(v.el.querySelector('a[href="/integrations/registry"]')?.textContent).toBe('Container registry')
})


it('does not count an unknown item as a confirmed problem or duplicate its unknown source', async () => {
 responses(()=>json({items:[{...warning.items[0],key:'capacity/host/read',unknown:true,title:'Host capacity',detail:'The host did not answer. Check its connection.'}],sources:[{...known.sources[0],key:'capacity/host',unknown:true}]}))
 const v=mount(<><AttentionLights/><AttentionPage/></>);await settled(v.client)
 expect(badge(v.el)).toBeNull()
 expect(v.el.querySelectorAll('article')).toHaveLength(1)
 expect(v.el.textContent).toContain('Check its connection.')
})

it('counts the rows shown consistently in the header, Attention and Home', async () => {
 const answer = {...warning, items: warning.items.map(i => ({...i, count: 7})), sources: [...known.sources,{...known.sources[0], key:'capacity', unknown:true}]}
 responses(() => json(answer), {'/dashboard':emptyDashboard,'/activity':emptyActivity,'/projects?limit=5&order=mine':{ projects: [{ id: 'project', slug: 'game', name: 'Game', hasDeployments: false }], total: 1 }})
 const v = mount(<><AttentionLights/><AttentionPage/><Dashboard/></>); await settled(v.client)
 expect(badge(v.el).getAttribute('aria-label')).toBe('2 to fix')
 expect(v.el.querySelectorAll('article')).toHaveLength(3)
 expect(v.el.textContent).not.toContain('host issues')
 // Home keeps the same row counts as Attention's summary.
 expect(v.el.textContent).toContain('2 to fix · 1 not known')
 expect([...v.el.querySelectorAll('.dash-lead a[href="/attention"]')].map(a => [a.children[0]?.textContent, a.children[1]?.textContent])).toEqual([
   ['Problems', '2'],
 ])
 expect(v.el.querySelector('article small')?.textContent).toContain('checked ')
})

it('keeps low disk amber in Attention', async () => {
 responses(()=>json({...warning,items:[{...warning.items[0],statusLabel:'low'}]}))
 const v=mount(<AttentionPage/>);await settled(v.client)
 expect(v.el.querySelector('article .chip.pending')?.textContent).toContain('low')
 expect(v.el.querySelector('article .chip.fail')).toBeNull()
})

it('counts an expired item once when its owner coverage has the same key', () => {
 const item = { ...warning.items[0], key: 'integrations/mail', unknown: true }
 const source = { ...known.sources[0], key: item.key, unknown: true }
 const result = attentionState({ data: { items: [item], sources: [source] }, refreshFailed: false, paused: false })
 expect(result.unknown).toBe(1)
 expect(result.count).toBe(0)
})

// A check that has never been read is not checked yet, never working.
it('shows a never-checked source as not checked yet in the full list', async () => {
 responses(() => json({ items: [], sources: [{ ...known.sources[0], observedAt: '0001-01-01T00:00:00Z' }, { ...known.sources[0], key: 'checked', name: 'Checked source' }] }))
 const v = mount(<AttentionPage checks />); await settled(v.client)
 const rows = [...v.el.querySelectorAll('section[aria-label="All checks"] .attention-row')]
 const never = rows.find(r => r.textContent!.includes('Worker availability'))!
 expect(never.querySelector('.row-status')!.textContent).toContain('not checked yet')
 expect(never.textContent).not.toContain('working')
 expect(rows.find(r => r.textContent!.includes('Checked source'))!.querySelector('.row-status')!.textContent).toContain('working')
})

// Only people who can fix conditions see the lights; members see none.
it('shows the lights to operators only, one per kind of condition, linking to its group', async () => {
 responses(() => json(warning))
 const v = mount(<AttentionLights />); await settled(v.client)
 const lights = [...v.el.querySelectorAll('.attention-light:not(.off)')]
 expect(lights.map(l => l.getAttribute('href'))).toEqual(['/attention#hosts', '/attention#backups'])
 expect(lights[0].getAttribute('title')).toBe('Hosts and deployments: 1 to fix')
 vi.unstubAllGlobals()
 responses(() => json(warning))
 vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: unknown) => {
  const path = pathOf(url)
  if (path === '/attention') return json(warning)
  if (path === '/auth/me') return json({ mode: 'internal', user: 'kid' })
  if (path === '/authz/me') return json({ instance: { 'project:read': true } })
  return json({})
 })))
 const member = mount(<AttentionLights />); await settled(member.client)
 expect(member.el.querySelector('.attention-lights')).toBeNull()
})

it('shows a failed backup attempt as last tried, while a first check remains pending', async () => {
 responses(() => json({items: [], sources: [
  { ...known.sources[0], key: 'protection/pending', name: 'Lunch · backups', unknown: true, observedAt: '', reason: 'Office: Backups have not been checked yet.', nextStep: 'AppTown schedules a background check every five minutes.' },
  { ...known.sources[0], key: 'protection/failed', name: 'Orders · backups', unknown: true, observedAt: '', attemptedAt: at, reason: "Office: The deployment's data could not be read because its platform's data inventory is unavailable.", nextStep: 'AppTown schedules a background check every five minutes.' }, // gitleaks:allow (a test fixture's reading key)
 ]}))
 const v = mount(<AttentionPage />); await settled(v.client)
 const rows = v.el.querySelectorAll('article')
 expect(rows).toHaveLength(2)
 expect(rows[0].textContent).toContain('not checked yet')
 expect(rows[0].textContent).toContain('every five minutes')
 expect(rows[0].textContent).not.toContain('could not be read')
 expect(rows[1].textContent).toContain('last tried')
 expect(rows[1].textContent).not.toContain('not checked yet')
 expect(rows[1].textContent).not.toContain('Ask an administrator')
})

// An operator whose Attention read failed is told so, not shown an all-clear.
it('shows a not-known light when Attention cannot be read', async () => {
 responses(() => json({ error: 'down' }, 500))
 const v = mount(<AttentionLights />); await settled(v.client)
 const light = v.el.querySelector('.attention-unread')
 expect(light?.getAttribute('href')).toBe('/attention')
 expect(light?.textContent).toContain('unknown')
})

// Working checks fold into one line; the ones that are not working stay in view.
it('keeps working checks inline with the checks needing attention', async () => {
 responses(() => json({ items: [], sources: [
  { ...known.sources[0], key: 'ok-1', name: 'Fine one' },
  { ...known.sources[0], key: 'protection/ok-2', name: 'Fine two' },
  { ...known.sources[0], key: 'never', name: 'Never read', observedAt: '0001-01-01T00:00:00Z' },
 ] }))
 const v = mount(<AttentionPage checks />); await settled(v.client)
 const all = v.el.querySelector('section[aria-label="All checks"]')!
 const fold = all.querySelector('section.attention-working')!
 expect(fold.hasAttribute('open')).toBe(false)
 expect(fold.querySelector('h3')!.textContent).toBe('2 more checks working')
 expect(fold.textContent).toContain('Fine one')
 // Grouped by the same kinds as the problems above.
 expect(fold.textContent).toContain('Hosts and deployments · 1')
 expect(fold.textContent).toContain('Backups · 1')
 const outside = [...all.querySelectorAll(':scope > * .attention-row, :scope .attention-row')].filter(r => !fold.contains(r))
 expect(outside.map(r => r.textContent)).toEqual([expect.stringContaining('Never read')])
})

it('Find keeps focus while typing, follows the first match on Enter and returns focus to its trigger', async () => {
 responses(() => json(known), { '/projects?limit=6&q=library': { projects: [{ slug: 'library', name: 'Library' }] } })
 function Address() { return <output>{useLocation().pathname}</output> }
 const v = mount(<><TopBar me={{ mode: 'internal', user: 'alice' }} /><Address /></>)
 const trigger = v.el.querySelector<HTMLButtonElement>('[aria-label="Search"]')!
 act(() => trigger.click())
 const input = document.querySelector<HTMLInputElement>('#find-projects')!
 expect(document.activeElement).toBe(input)
 act(() => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'library')
  input.dispatchEvent(new Event('input', { bubbles: true }))
 })
 await settled(v.client)
 expect(document.activeElement).toBe(input)
 expect(document.querySelector('.topbar-find-menu')?.textContent).toContain('Library')
 const enter = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
 act(() => input.dispatchEvent(enter))
 expect(enter.defaultPrevented).toBe(true)
 expect(v.el.querySelector('output')?.textContent).toBe('/projects/library')
 expect(document.querySelector('#find-projects')).toBeNull()
 expect(document.activeElement).toBe(trigger)
 act(() => trigger.click())
 act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
 expect(trigger.getAttribute('aria-expanded')).toBe('false')
 expect(document.activeElement).toBe(trigger)
})


it('shows a pending release on Home and Attention with its deployment as the action door', async () => {
 const href = '/projects/game/deployments/production'
 const task = { project: 'game', href, deploymentLabel: 'Production service', release: { id: 'release-12', number: 12, deployment: 'production', state: 'waiting for approval' } }
 responses(() => json({ ...known, items: [{ key: 'release-approval/release-12', title: 'Release 12 to Production service needs your approval', detail: 'game', href, statusLabel: 'waiting for approval', severity: 'warning', observedAt: at, stale: false }] }), {
  '/activity': { ...emptyActivity, releaseApprovals: [task] },
  '/dashboard': emptyDashboard,
  '/projects?limit=5&order=mine': { projects: [{ id: 'project', slug: 'game', name: 'Game', hasDeployments: false }], total: 1 }, '/projects/game/deployments': [],
 })
 const v = mount(<><AttentionLights /><AttentionPage /><Dashboard /></>); await settled(v.client)
 const article = v.el.querySelector('article')!
 expect(article.querySelector('.chip.pending')?.textContent).toContain('waiting for approval')
 expect(article.querySelector('.chip.fail')).toBeNull()
 expect(article.querySelector('a')?.getAttribute('href')).toBe(href)
 expect(v.el.querySelector('.attention-light.low')).not.toBeNull()
 const personal = v.el.querySelector('section[aria-label="Waiting on you"]')!
 expect(personal.querySelector('.section-count')?.textContent).toBe('1')
 expect(personal.querySelector('a')?.getAttribute('href')).toBe(href)
 expect(personal.textContent).toContain('Release 12 to Production service needs your approval')
})

it('Home and Projects use the standing roll-up when a preview runs beside a stopped app', () => {
 const status = { word: 'stopped', reason: 'main: the application is stopped', stale: false } as const
 const deployments = [{project:'game',kind:'standing',name:'main',running:false,projectStatus:status}, {project:'game',kind:'preview',name:'preview-1',running:true,projectStatus:status}] as InstanceActivity['deployments']
 const parts = homeStatusParts({needsYou:0,running:0,deployments,operations:{known:true,count:0}})
 expect(parts.find(p=>p.label==='Apps live')?.value).toBe(0)
 const p = {slug:'game',name:'Game',hasDeployments:true,productionUrl:'https://game.example.test',prodRunning:false,status,observation:'stopped',stale:false} as unknown as ProjectRow
 const html = renderToStaticMarkup(<MemoryRouter><ProjectListRow project={p}/></MemoryRouter>)
 expect(html).toContain('stopped')
 expect(html).not.toContain('Open app')
})

it.each(['stopped', 'recovery not rehearsed', 'recovery not tested on its platform'] as const)('keeps the server word %s visible when the reading is old', async statusLabel => {
 responses(()=>json({...warning,items:[{...warning.items[1],statusLabel,stale:true}]}))
 const v=mount(<AttentionPage/>);await settled(v.client)
 expect(v.el.querySelector('article .chip')?.textContent).toContain(statusLabel)
 expect(v.el.querySelector('article .chip')?.textContent).not.toContain('not checked yet')
 expect(v.el.querySelector('article .attention-seen')?.textContent).toContain('last seen')
})
