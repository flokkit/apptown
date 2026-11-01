// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { settled } from '../settled'
import Projects from './Projects'
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
  expect(el.querySelectorAll('.chip.pending')).toHaveLength(2)
  expect(el.textContent).toContain('2 findings')
  expect(el.textContent).toContain('1 finding')
  expect(el.querySelectorAll('.chip.fail')).toHaveLength(1)
  expect(el.querySelector('.chip.fail')?.textContent).toContain('failed')
})
it('omits lights on neutral states', async () => {
  const el = await show(<Status word="none" tone="" text="Not deployed" />)
  expect(el.textContent).toBe('Not deployed')
  expect(el.querySelector('[aria-hidden]')).toBeNull()
})
it.each([false, true])('gives each project identity, a live address and the last change title (phone=%s)', async narrow => {
  const el = await show(<Projects />, { '/projects?limit=50&offset=0&q=': { total: 1, projects: [{ id: 'one', slug: 'calendar', name: 'Calendar', icon: '', activeItems: 0, doneRuns: 2, lastChangeTitle: 'Add school holidays', lastRunAt: '', hasDeployments: true, observation: 'running', observationStale: false, productionUrl: 'https://calendar.example', prodRunning: true }] } }, narrow)
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
it.each([true, false])('keeps every pack action on its row and preserves delete confirmation (enabled=%s)', async enabled => {
  const el = await show(<Packs />, { '/packs': [{ name: 'household', origin: 'custom', enabled, requires: [], guidance: [], workflows: [], policies: [], declaration: { guidance: [], workflows: [], policies: [] } }] })
  const actions = el.querySelector('.pack-actions')!
  expect(actions.closest('details')).toBeNull()
  expect([...actions.querySelectorAll('button')].map(b => b.textContent)).toEqual(['Contents', 'Copy', enabled ? 'Hide' : 'Make available', 'Edit', 'Delete'])
  await press(el, 'Delete')
  expect(el.textContent).toContain('Projects that applied it keep the versions they applied.')
  expect(vi.mocked(fetch).mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
})
it.each([false, true])('shows short deployment lists inline (phone=%s)', async narrow => {
  const app = { slug: 'calendar', name: 'Calendar', deploymentId: 'main', deploymentLabel: 'Main', observation: 'running', observationStale: false, running: true, url: 'https://calendar.example' }
  const el = await show(<Routes><Route path="/deployments/:gtab" element={<Deployments />} /></Routes>, { '/tiers': { tiers: [{ name: 'main', title: 'Main', apps: [app] }] }, '/projects': [app] }, narrow, '/deployments/table')
  const link = el.querySelector('a[href="https://calendar.example"]')!
  expect(link).not.toBeNull()
  expect(link.closest('details')).toBeNull()
  expect(el.textContent).not.toContain('Details')
})
it.each([false, true])('expands resource evidence by row and preserves remove confirmation (phone=%s)', async narrow => {
  const target = { target: 'kitchen', live: true, resources: [{ kind: 'container', name: 'old-calendar', id: 'container-id', record: 'unrecorded', status: 'running', statusLabel: 'running', detail: 'provider evidence' }] } as InfrastructureView
  const el = await show(<ResourceTables t={target} onChanged={() => {}} />, {}, narrow)
  expect([...el.querySelectorAll('summary')].map(s => s.textContent)).not.toContain('Details')
  const row = el.querySelector<HTMLTableRowElement>('tr[aria-expanded]')!
  expect(row).not.toBeNull()
  expect(row.getAttribute('aria-expanded')).toBe('false')
  await act(async () => { row.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })) })
  expect(row.getAttribute('aria-expanded')).toBe('true')
  expect(el.textContent).toContain('provider evidence')
  await press(el, 'remove')
  expect(row.getAttribute('aria-expanded')).toBe('true')
  expect(el.textContent).toContain('Remove container old-calendar from kitchen?')
  expect(vi.mocked(fetch).mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false)
})
it('explains development updates instead of an empty release channel', async () => {
  const el = await show(<UpdatesPage />, { '/authz/me': { instance: { 'instance:updates': true } }, '/instance-updates': { build: 'dev-abcdef', platform: 'linux/amd64', state: 'no-release', statusLabel: 'none', channel: 'stable' } })
  expect(el.textContent).toContain('This AppTown runs a development build. Updates come by deployment.')
  expect(el.textContent).not.toContain('No releases in this channel')
})

it('keeps three placements inline and folds only the remaining long list', async () => {
  const apps = Array.from({ length: 5 }, (_, n) => ({ slug: 'calendar', name: 'Calendar', deploymentId: `placement-${n}`, deploymentLabel: `Office ${n}`, observation: 'running', observationStale: false, running: true, url: `https://office-${n}.example` }))
  const el = await show(<Routes><Route path="/deployments/:gtab" element={<Deployments />} /></Routes>, { '/tiers': { tiers: [{ name: 'main', title: 'Main', apps }] }, '/projects': [apps[0]] }, false, '/deployments/table')
  const links = [...el.querySelectorAll('a[href^="https://office-"]')]
  expect(links.filter(a => !a.closest('details'))).toHaveLength(3)
  expect(links.filter(a => a.closest('details'))).toHaveLength(2)
  expect(el.textContent).toContain('2 more deployments')
})

it.each([false, true])('names an unlabelled deployment by its tier (phone=%s)', async narrow => {
  const app = { slug: 'calendar', name: 'Calendar', deploymentId: 'main', observation: 'running', running: true }
  const el = await show(<Routes><Route path="/deployments/:gtab" element={<Deployments />} /></Routes>, { '/tiers': { tiers: [{ name: 'main', title: 'Household', apps: [app] }] }, '/projects': [app] }, narrow, '/deployments/table')
  expect(el.querySelector('tbody')?.textContent).toContain('Household')
})
it('keeps a broken check red beside amber findings', async () => {
  const el = await show(<StandingChip label="failed" findings={2} broken={1} help="Mixed results" />)
  expect(el.querySelector('.chip.pending')?.textContent).toContain('2 findings')
  expect(el.querySelector('.conformance-broken')?.textContent).toContain("· 1 couldn't run")
})

it.each([false, true])('keeps failed execution beside findings in list and project notes (phone=%s)', async narrow => {
  const p = { slug: 'calendar', name: 'Calendar', standing: 'does-not-conform', statusLabel: 'failed', sentence: '2 findings · 1 could not run', lanes: { findings: 2, broken: 1, conforms: 0, excepted: 0, flagged: 0, unassessed: 0, na: 0 }, total: 3, adopted: [] }
  const view = { projects: [p], byProject: {}, exceptions: {}, adjudications: {}, checkDefs: {} } as unknown as ConformanceView
  // A phone list row puts the chip and its fact on one line, so the note shows once.
  for (const [ui, count] of [[<ProjectsList view={view} base="/conformance" />, narrow ? 1 : 2], [<ConformanceProject view={view} slug="calendar" readOnly onRunOne={() => {}} />, 2]] as const) {
    const el = await show(ui, {}, narrow)
    const notes = el.querySelectorAll('.conformance-broken')
    expect(notes).toHaveLength(count)
    expect([...notes].every(n => n.textContent === "· 1 couldn't run")).toBe(true)
    expect(el.querySelector('.chip.pending')?.textContent).toContain('2 findings')
  }
})
