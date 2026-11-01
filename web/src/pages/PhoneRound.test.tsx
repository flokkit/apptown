import { fmtStamp } from '../format'
// @vitest-environment jsdom
import { expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { getKey, newQueryClient } from '../data'
import { AgentsCard, UsersCard } from './People'
import WorkflowLibrary from './WorkflowLibrary'
import { StoreSection } from './DataProtection'
import Deployments from './Deployments'
import Dashboard from './Dashboard'
import { HistoryTab } from './change/Tabs'
import type { ChangeView } from '../api'
import { MembersTab } from './project/settings/Members'
import { projectFact } from './conformance/Projects'
import type { ConformanceView, ProjectStanding } from '../wire.gen'
import type { Account } from '../wire.gen'

function page(node: React.ReactNode, data: Record<string, unknown> = {}) {
  const client = newQueryClient()
  for (const [path, answer] of Object.entries(data)) client.setQueryData(getKey(path), answer)
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}>{node}</QueryClientProvider></MemoryRouter>)
  client.clear()
  return el
}
function firstView(el: HTMLElement) {
  const copy = el.cloneNode(true) as HTMLElement
  copy.querySelectorAll('details:not([open])').forEach(d => {
    const summary = d.querySelector(':scope > summary')
    for (const child of [...d.childNodes]) if (child !== summary) child.remove()
  })
  return copy.textContent ?? ''
}

it('groups duplicate agents with a connected date while preserving every credential and disconnect action', () => {
  const connections = Array.from({ length: 5 }, (_, i) => ({ id: String(i), clientName: 'ChatGPT', user: 'sam', scopes: ['apptown:user:read'], createdAt: `2026-10-${20 + i}T01:00:00Z` }))
  const el = page(<AgentsCard canEdit />, { '/auth/delegations?all=1': { delegations: connections } })
  expect(el.querySelectorAll('.agent-group')).toHaveLength(1)
  expect(firstView(el)).toContain('ChatGPT ×5 as sam')
  expect(firstView(el)).toContain('Latest connection 24 Oct')
  expect(firstView(el)).not.toContain('disconnect')
  expect([...el.querySelectorAll('button')].filter(b => b.textContent === 'disconnect')).toHaveLength(5)
  for (let day = 20; day <= 24; day++) expect(el.textContent).toContain(`Connected ${day} Oct`)
})

it('labels account dates as last seen and states when no sign-in was recorded', () => {
  const users = [{ username: 'sam', lastSeen: `${new Date().getFullYear()}-10-31T01:00:00Z`, groups: [], hasPassword: true }, { username: 'old', lastSeen: '2023-03-03T01:00:00Z', groups: [], hasPassword: true }, { username: 'tom', lastSeen: '', groups: [], hasPassword: true }] as unknown as Account[]
  const el = page(<UsersCard users={users} act={() => {}} canEdit={false} invites={false} sso={false} me="sam" />)
  expect(el.textContent).toContain('seen 31 Oct')
  expect(el.textContent).toContain(`seen ${fmtStamp(users[1].lastSeen!)}`)
  expect(el.textContent).not.toContain(`seen 31 Oct ${new Date().getFullYear()}`)
  expect(el.textContent).toContain('never signed in')
})

it('says when the selected workflow group is empty', () => {
  const el = page(<WorkflowLibrary />, { '/workflows/library': [], '/system-workflows': { bindings: [], candidates: [] } })
  expect(el.textContent).toContain('No workflows in this group.')
  expect(el.textContent).toContain('New workflow')
})

it('groups repeated history events while keeping each exact event and its time in Details', () => {
  const view = { runs: [], detail: { events: Array.from({ length: 6 }, (_, i) => ({ id: String(i), kind: 'integration.resolved', at: '2026-10-31T01:00:00Z', payload: { connection: `resource-${i}` } })) } } as unknown as ChangeView
  const el = page(<HistoryTab view={view} runHref={() => '/work'} />)
  expect(firstView(el)).toContain('Connection checked ×6')
  expect(el.querySelectorAll('.event-log details')).toHaveLength(1)
  expect(el.querySelector('.event-log summary')?.textContent).toContain('Connection checked ×6')
  expect(el.querySelector('.event-log summary')?.textContent).not.toContain('Details')
  expect(el.querySelector('.event-log details')?.hasAttribute('open')).toBe(false)
  for (let i = 0; i < 6; i++) expect(el.textContent).toContain(`resource-${i}`)
  expect(firstView(el)).not.toContain('run 5')
})


it('keeps project role names short with the complete description outside the native picker', () => {
  const description = 'Opens changes and contributes revisions for other people to review.'
  const el = page(<MembersTab slug="shop" />, {
    '/authz/me?project=shop': { project: { 'project:manage-members': true } },
    '/authz/roles': { roles: [{ name: 'project-contributor', title: 'Project contributor', scope: 'project', surfaced: true, description }] },
    '/projects/shop': { requireIndependentApproval: false }, '/projects/shop/members': { roles: [] },
    '/users': { users: [] }, '/groups': { groups: [] },
  })
  expect(el.querySelector('select[aria-label="Project role"] option')?.textContent).toBe('contributor')
  expect(el.querySelector('select[aria-label="Person or group to add"]')?.closest('label')?.textContent).toContain('Person or group')
  expect(el.querySelector('select[aria-label="Project role"]')?.closest('label')?.textContent).toContain('Role')
  expect(el.textContent).toContain(description)
})

it('puts the failed check reason on the project row and keeps an empty assessment unknown', () => {
  const p = { slug: 'shop', lanes: {}, total: 0 } as ProjectStanding
  expect(projectFact(p, { byProject: { shop: [{ state: 'does-not-conform', detail: 'README missing' }] } } as unknown as ConformanceView)).toBe('README missing')
  expect(projectFact(p, { byProject: {} } as ConformanceView)).toBe('No checks recorded.')
})


it('uses the check title on a phone row instead of a long technical diagnostic', () => {
  const p = { slug: 'shop', lanes: { findings: 1 }, total: 1 } as ProjectStanding
  const view = { byProject: { shop: [{ block: 'runtime', version: 1, check: 'ready', state: 'does-not-conform', detail: '.apptown/app.yaml: ' + 'raw technical detail '.repeat(20) }] }, checkDefs: { ready: { block: 'runtime', version: 1, check: 'ready', title: 'Add an app readiness check' } } } as unknown as ConformanceView
  expect(projectFact(p, view)).toBe('Fix: Add an app readiness check')
})


it('keeps backup endpoint and replacement action in store details', () => {
  const el = page(<StoreSection canAttach />, { '/backup-store': { store: { bucket: 'household-copies', endpoint: 'https://storage.example.test', attachedBy: 'sam', attachedAt: '2026-10-31T01:00:00Z', accessKeyFrom: 'STORE_KEY', secretKeyFrom: 'STORE_SECRET' } } })
  expect(firstView(el)).toContain('Backups go to household-copies')
  expect(firstView(el)).not.toContain('https://storage.example.test')
  expect(firstView(el)).not.toContain('Attach another')
  expect(el.textContent).toContain('Attach another')
  expect(el.textContent).toContain('STORE_KEY')
})

it('shows the project release number in the phone departure fact', () => {
  vi.stubGlobal('matchMedia', () => ({ matches: true }))
  try {
    const el = page(<Deployments />, { '/tiers/departures': { departures: [{ slug: 'shop', name: 'Shop', releaseNumber: 6, state: 'deployed', at: '2026-10-31T01:00:00Z', tier: 'main', label: 'Live app', url: 'https://shop.example.test' }] } })
    expect(firstView(el)).toContain('Release 6')
    expect(el.querySelector('.glance-fact a')?.getAttribute('href')).toBe('/projects/shop/releases/6')
  } finally { vi.unstubAllGlobals() }
})

it('asks for review of an unconfirmed AI flag before asking to fix it', () => {
  const p = { slug: 'shop', standing: 'needs-a-look', statusLabel: 'unknown', lanes: { flagged: 1 }, total: 1 } as ProjectStanding
  const view = {
    adjudications: {},
    byProject: { shop: [{ id: 'ai-flag', block: 'docs', version: 1, check: 'readme', evaluator: 'llm', state: 'does-not-conform', statusLabel: 'failed' }] },
    checkDefs: { readme: { block: 'docs', version: 1, check: 'readme', title: 'Explain how to run the app' } },
  } as unknown as ConformanceView
  expect(projectFact(p, view)).toMatch(/review/i)
  expect(projectFact(p, view)).not.toMatch(/^Fix:/)
  view.adjudications['ai-flag'] = { verdictId: 'ai-flag', kind: 'confirmed' } as unknown as ConformanceView['adjudications'][string]
  expect(projectFact({ ...p, standing: 'does-not-conform', lanes: { findings: 1 } } as ProjectStanding, view)).toBe('Fix: Explain how to run the app')
})

it('does not describe an evaluator error as a confirmed problem in the app', () => {
  const p = { slug: 'shop', lanes: { broken: 1 }, total: 1 } as ProjectStanding
  const view = { byProject: { shop: [{ state: 'error', detail: 'scanner unavailable' }] } } as unknown as ConformanceView
  expect(projectFact(p, view)).toBe('A check could not run. Open for details.')
})

it('labels the project activity age on Home with what the timestamp measures', () => {
  const el = page(<Dashboard />, {
    '/activity': { agents: [], active: [], history: [], deployments: [] }, '/dashboard': {},
    '/projects?limit=5&order=mine': { projects: [{ id: 'shop', slug: 'shop', name: 'Shop', hasDeployments: false, lastRunAt: '2026-10-31T01:00:00Z' }], total: 1 },
  })
  expect(el.querySelector('.project-state > .muted')?.textContent).toMatch(/^Work updated /)
})
