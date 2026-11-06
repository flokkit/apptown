// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { settled } from '../settled'
import Projects from './Projects'
import TopBar from '../TopBar'
import Packs from './Packs'
import Deployments from './Deployments'
import { ResourceTables } from './Infrastructure'
import { UpdatesPage } from './Updates'
import { ProjectsList } from './conformance/Projects'
import { ProjectPage as ConformanceProject } from './conformance/ProjectPage'
import type { ConformanceView } from '../wire.gen'
import { StandingChip, StateChip } from './conformance/parts'
import { Status } from '../Status'
import type { InfrastructureView } from '../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const cleanup: (() => void)[] = []
afterEach(() => { cleanup.splice(0).forEach(close => close()); vi.unstubAllGlobals() })
async function show(ui: ReactNode, replies: Record<string, unknown> = {}, narrow = false, path = '/') {
  vi.stubGlobal('matchMedia', () => ({ matches: narrow, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(replies[url.replace('/api/v1', '')] ?? {}))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}>{ui}</MemoryRouter></QueryClientProvider>))
  cleanup.push(() => { act(() => root.unmount()); client.clear() })
  await settled(client)
  return el
}
const press = async (el: HTMLElement, text: string) => act(async () => { [...el.querySelectorAll('button')].find(b => b.textContent === text)!.click() })

it('uses amber finding counts and reserves failed for checks that could not run', async () => {
  const el = await show(<><StandingChip label="failed" findings={2} help="Does not conform" /><StateChip label="failed" finding help="warning" /><StateChip label="failed" help="could not execute" /></>)
  expect(el.querySelectorAll('.finding-count')).toHaveLength(2)
  expect(el.textContent).toContain('2 findings')
  expect(el.textContent).toContain('1 finding')
  expect(el.querySelectorAll('.chip.fail')).toHaveLength(1)
  expect(el.querySelector('.chip.fail')?.textContent).toContain('failed')
})
it('gives neutral states a grey dot, never a colour', async () => {
  const el = await show(<Status word="none" />)
  expect(el.textContent).toBe('● not configured')
  expect(el.querySelector('.chip')!.className).toBe('chip ')
})
// Not applicable keeps a grey dot so it lines up with the states around it.
it('gives not applicable a neutral dot', async () => {
  const el = await show(<Status word="not applicable" />)
  expect(el.querySelector('.chip')!.className).toBe('chip ')
  expect(el.querySelector('[aria-hidden]')?.textContent).toBe('● ')
})
it.each([false, true])('gives each project identity, a live address and the last change title (phone=%s)', async narrow => {
  const el = await show(<Projects />, { '/projects?limit=50&offset=0&q=': { total: 1, projects: [{ id: 'one', slug: 'calendar', name: 'Calendar', icon: '', activeItems: 0, doneRuns: 2, lastChangeTitle: 'Add school holidays', lastRunAt: '', hasDeployments: true, observation: 'running', observationStale: false, productionUrl: 'https://calendar.example', prodRunning: true }] } }, narrow)
  expect(el.querySelector('a[href="/projects/_new"]')?.textContent).toBe('+ Create project')
  expect(el.querySelector('.project-avatar')?.textContent).toBe('C')
  expect(el.querySelector('a[href="https://calendar.example"]')?.textContent).toBe('Open app ↗')
  expect(el.textContent).toContain('Add school holidays')
  expect(el.textContent).toContain('2 shipped')
  expect(el.querySelector('.project-state')?.textContent).toContain('Not checked yet')
  expect(el.textContent).not.toContain('Updated No changes yet')
  expect(el.querySelector('tbody tr[aria-expanded]')).toBeNull()
  expect(el.textContent).not.toContain('0 in progress')
  expect(el.querySelector('.project-row')).not.toBeNull()
})
it.each([true, false])('links to the pack and leaves only Copy in its row menu (enabled=%s)', async enabled => {
  const el = await show(<Packs />, { '/packs': [{ name: 'household', origin: 'custom', enabled, requires: [], guidance: [], workflows: [], policies: [], declaration: { guidance: [], workflows: [], policies: [] } }] })
  expect(el.querySelector('a[href="/packs/household"]')).not.toBeNull()
  await act(async () => { (el.querySelector('button[aria-label="More actions for household"]') as HTMLButtonElement).click() })
  expect([...document.querySelectorAll('.popover button')].map(b => b.firstChild?.textContent)).toEqual(['Copy'])
})
// The fleet table is one row per app: its placements are counted in one
// meta line, never folded inside the row; the app's page lists each one.
it.each([false, true])('counts an app\'s deployments in one line without a fold (phone=%s)', async narrow => {
  const app = { slug: 'calendar', name: 'Calendar', deploymentId: 'main', deploymentLabel: 'Main', observation: 'running', observationStale: false, running: true, url: 'https://calendar.example' }
  const el = await show(<Routes><Route path="/deployments" element={<Deployments />} /></Routes>, { '/tiers': { tiers: [{ name: 'main', title: 'Main', apps: [app] }] }, '/projects': [app], '/projects/calendar/deployments': [{ id: 'main', name: 'main', generation: 1, observation: 'running' }], '/projects/calendar/deployments/main/diagnostics': { parts: [] }, '/projects/calendar/releases': [] }, narrow, '/deployments')
  expect(el.textContent).toContain('View deployment')
  expect(el.querySelector('.card.row details:not(.status-help)')).toBeNull()
})
it.each([false, true])('expands resource evidence by row and preserves remove confirmation (phone=%s)', async narrow => {
  const target = { target: 'kitchen', live: true, resources: [{ kind: 'container', name: 'old-calendar', id: 'container-id', record: 'unrecorded', status: 'running', statusLabel: 'running', detail: 'provider evidence' }] } as InfrastructureView
  const el = await show(<ResourceTables t={target} onChanged={() => {}} />, {}, narrow)
  expect(el.querySelector('[aria-expanded]')).toBeNull()
  expect(el.textContent).toContain('provider evidence')
  await press(el, 'Remove')
  expect(el.textContent).toContain('Remove container old-calendar from kitchen?')
  expect(vi.mocked(fetch).mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false)
})
it('explains development updates instead of an empty release channel', async () => {
  const el = await show(<UpdatesPage />, { '/authz/me': { instance: { 'instance:updates': true } }, '/instance-updates': { build: 'dev-abcdef', platform: 'linux/amd64', state: 'no-release', statusLabel: 'none', channel: 'stable' } })
  expect(el.textContent).toContain('This AppTown runs a development build. Updates come by deployment.')
  expect(el.textContent).not.toContain('No releases in this channel')
})

it('counts a long list of placements without folding them into the row', async () => {
  const apps = Array.from({ length: 5 }, (_, n) => ({ slug: 'calendar', name: 'Calendar', deploymentId: `placement-${n}`, deploymentLabel: `Office ${n}`, observation: 'running', observationStale: false, running: true, url: `https://office-${n}.example` }))
  const el = await show(<Routes><Route path="/deployments" element={<Deployments />} /></Routes>, { '/tiers': { tiers: [{ name: 'main', title: 'Main', apps }] }, '/projects': [apps[0]], '/projects/calendar/deployments': apps.map((a, i) => ({ id: a.deploymentId, name: `office-${i}`, displayLabel: a.deploymentLabel, generation: 1, observation: 'running' })), ...Object.fromEntries(apps.map((_, i) => [`/projects/calendar/deployments/office-${i}/diagnostics`, { parts: [] }])), '/projects/calendar/releases': [] }, false, '/deployments')
  expect(el.querySelectorAll('[role=listitem]')).toHaveLength(5)
  expect(el.textContent).not.toContain('more deployments')
  expect(el.querySelectorAll('[role=listitem]')).toHaveLength(5)
})

it('keeps a broken check red beside amber findings', async () => {
  const el = await show(<StandingChip label="failed" findings={2} broken={1} help="Mixed results" />)
  expect(el.querySelector('.finding-count')?.textContent).toContain('2 findings')
  expect(el.querySelector('.conformance-broken')?.textContent).toContain("· 1 not known")
})

it.each([false, true])('keeps failed execution beside findings in list and project notes (phone=%s)', async narrow => {
  const p = { notKnown: [{ detail: 'scanner: no Docker executor', evaluator: 'tool' }], slug: 'calendar', name: 'Calendar', standing: 'does-not-conform', statusLabel: 'failed', sentence: '2 findings · 1 could not run', lanes: { findings: 2, broken: 1, conforms: 0, excepted: 0, flagged: 0, unassessed: 0, na: 0 }, total: 3, adopted: [] }
  const view = { openRemediations: [], projects: [p], byProject: {}, exceptions: {}, adjudications: {}, checkDefs: {} } as unknown as ConformanceView
  // The findings count is the status; what could not run is said once, in the meta line.
  for (const ui of [<ProjectsList view={view} base="/conformance" />, <ConformanceProject view={view} slug="calendar" readOnly onRunOne={() => {}} />]) {
    const el = await show(ui, {}, narrow)
    expect(el.textContent!.split("1 couldn't run")).toHaveLength(2)
    expect(el.textContent).toContain('2 findings')
  }
})

// A check that has not judged anything says why, in its own section; the
// owner's sentence is not repeated by a second couldn't-run count.
it('lists not-judged checks with their reason and says what could not run once', async () => {
  const p = { notKnown: [], slug: 'calendar', name: 'Calendar', standing: 'cannot-assess', statusLabel: 'unknown', sentence: 'Review incomplete · 1 not known: scanner', lanes: { findings: 0, broken: 1, conforms: 0, excepted: 0, flagged: 0, unassessed: 1, na: 0 }, total: 2, adopted: [] }
  const waiting = { id: 'w', lane: 'unassessed', state: 'cannot-assess', statusLabel: 'unknown', block: 'deploy', version: 1, check: 'serve', detail: 'judged on a standing deployment; there is none here' }
  const view = { openRemediations: [], projects: [p], byProject: { calendar: [waiting] }, exceptions: {}, adjudications: {}, priorConfirmations: {}, checkDefs: {} } as unknown as ConformanceView
  const el = await show(<ConformanceProject view={view} slug="calendar" readOnly onRunOne={() => {}} />, {}, false)
  const section = el.querySelector('section[aria-label="Not judged yet"]')!
  expect(section.textContent).toContain('Judged on a standing deployment; there is none here')
  expect(section.querySelector('.chip')).toBeNull()
  expect(el.textContent!.split('not known')).toHaveLength(2)
  expect(el.textContent).not.toContain("couldn't run")
})

it.each([false, true])('offers only uncovered fixes and keeps evidence and links (phone=%s)', async narrow => {
  const finding = { id: 'finding', projectId: 'project', projectSlug: 'calendar', block: 'hygiene', version: 1, check: 'clean', lane: 'findings', state: 'does-not-conform', statusLabel: 'failed', severity: 'high', detail: 'README missing', sha: '12345678', reviewId: 'review', evaluator: 'sh', at: '2026-01-01T00:00:00Z' }
  const p = { projectId: 'project', slug: 'calendar', name: 'Calendar', standing: 'does-not-conform', sentence: '2 findings', lanes: { findings: 2 }, notKnown: [] }
  const view = { openRemediations: [{ projectId: 'project', changeId: 'change', number: 12, block: 'hygiene', version: 1, check: 'clean' }], projects: [p], byProject: { calendar: [finding, { ...finding, id: 'other', check: 'other' }] }, exceptions: {}, adjudications: {}, priorConfirmations: {}, checkDefs: {} } as unknown as ConformanceView
  const el = await show(<ConformanceProject view={view} slug="calendar" readOnly={false} onRunOne={() => {}} />, {}, narrow)
  expect([...el.querySelectorAll('button')].map(b => b.textContent)).toContain('Fix 1 problem')
  expect(el.querySelector('a[href="/changes/change"]')?.textContent).toBe('change #12')
  expect(el.querySelector('a[href="/projects/calendar"]')).not.toBeNull()
  expect(el.textContent).toContain('README missing')
  expect(el.textContent).toContain('hygiene version 1 · clean')
  expect(el.querySelector('a[href="/conformance/reviews/review"]')).not.toBeNull()
  expect([...el.querySelectorAll('button')].filter(b => b.textContent === 'Open a change to fix')).toHaveLength(1)
})

it('names project creation in the top bar', async () => {
  const el = await show(<TopBar me={null} />, {}, false, '/projects/_new')
  expect(el.querySelector('.topbar-crumb.last a')?.textContent).toBe('Projects')
  expect(el.querySelector('.topbar-here')).toBeNull()
})
