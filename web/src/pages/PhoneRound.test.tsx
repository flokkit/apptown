import { relTime } from '../format'
// @vitest-environment jsdom
import { expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { getKey, newQueryClient } from '../data'
import { UsersCard } from './People'
import { AgentsCard } from './AccountAccess'
import WaysOfWorking from './WaysOfWorking'
import * as Policies from './PoliciesPage'
import WorkflowLibrary, { NEW_TEMPLATE } from './WorkflowLibrary'
import { load as loadYaml } from 'js-yaml'
import { routeAncestors } from '../breadcrumbs'
import { StoreSection } from './DataProtection'
import Deployments from './Deployments'
import Dashboard from './Dashboard'
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
  expect(firstView(el)).toContain('disconnect')
  expect([...el.querySelectorAll('button')].filter(b => b.textContent === 'disconnect')).toHaveLength(5)
  for (let day = 20; day <= 24; day++) expect(el.textContent).toContain(`${day} Oct`)
})

it('labels account dates as last seen and states when no sign-in was recorded', () => {
  const users = [{ username: 'sam', lastSeen: `${new Date().getFullYear()}-10-31T01:00:00Z`, groups: [], hasPassword: true }, { username: 'old', lastSeen: '2023-03-03T01:00:00Z', groups: [], hasPassword: true }, { username: 'tom', lastSeen: '', groups: [], hasPassword: true }] as unknown as Account[]
  const el = page(<UsersCard users={users} act={() => {}} canEdit={false} invites={false} sso={false} me="sam" />)
  expect(el.textContent).toContain(relTime(users[0].lastSeen!))
  expect(el.textContent).toContain(relTime(users[1].lastSeen!))
  expect(el.textContent).not.toContain(`seen 31 Oct ${new Date().getFullYear()}`)
  expect(el.textContent).toContain('Last seen')
})

it('says when the workflow library is empty, and still offers a new workflow', () => {
  const el = page(<WaysOfWorking tab="workflows" />, { '/workflows/library': [], '/system-workflows': { bindings: [], candidates: [] } })
  expect(el.textContent).toContain('No workflows.')
  expect(el.textContent).toContain('New workflow')
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
  const p = { notKnown: [], slug: 'shop', lanes: {}, total: 0 } as unknown as ProjectStanding
  expect(projectFact(p, { byProject: { shop: [{ lane: 'findings', state: 'does-not-conform', detail: 'README missing' }] } } as unknown as ConformanceView)).toBe('README missing')
  expect(projectFact(p, { byProject: {} } as ConformanceView)).toBe('No checks recorded.')
})


it('uses the check title on a phone row instead of a long technical diagnostic', () => {
  const p = { notKnown: [], slug: 'shop', lanes: { findings: 1 }, total: 1 } as unknown as ProjectStanding
  const view = { byProject: { shop: [{ block: 'runtime', version: 1, check: 'ready', lane: 'findings', state: 'does-not-conform', detail: '.apptown/app.yaml: ' + 'raw technical detail '.repeat(20) }] }, checkDefs: { ready: { block: 'runtime', version: 1, check: 'ready', title: 'Add an app readiness check' } } } as unknown as ConformanceView
  expect(projectFact(p, view)).toBe('Fix: Add an app readiness check')
})


// The store's facts are shown, not folded; replacing it is the section's one action.
it('shows the backup store, its endpoint and keys, with Attach another as its action', () => {
  const el = page(<StoreSection canAttach />, { '/backup-store': { store: { bucket: 'household-copies', endpoint: 'https://storage.example.test', attachedBy: 'sam', attachedAt: '2026-10-31T01:00:00Z', accessKeyFrom: 'STORE_KEY', secretKeyFrom: 'STORE_SECRET' } } })
  expect(firstView(el)).toContain('Backups go to household-copies')
  expect(firstView(el)).toContain('https://storage.example.test')
  expect(el.querySelector('.section-action')?.textContent).toBe('Attach another')
  expect(el.textContent).toContain('STORE_KEY')
})

it('shows the project release number in the phone departure fact', () => {
  vi.stubGlobal('matchMedia', () => ({ matches: true }))
  try {
    const el = page(<Deployments page="releases" />, { '/tiers/departures': { departures: [{ slug: 'shop', name: 'Shop', releaseNumber: 6, state: 'deployed', at: '2026-10-31T01:00:00Z', tier: 'main', label: 'Live app', url: 'https://shop.example.test' }] } })
    expect(firstView(el)).toContain('#6')
    expect(el.querySelector('a[href="/projects/shop/releases/6"]')?.textContent).toBe('#6')
  } finally { vi.unstubAllGlobals() }
})

it('asks for review of an unconfirmed AI flag before asking to fix it', () => {
  const p = { notKnown: [], slug: 'shop', standing: 'needs-a-look', statusLabel: 'unknown', lanes: { flagged: 1 }, total: 1 } as unknown as ProjectStanding
  const view = {
    adjudications: {},
    byProject: { shop: [{ id: 'ai-flag', block: 'docs', version: 1, check: 'readme', evaluator: 'llm', lane: 'flagged', state: 'does-not-conform', statusLabel: 'failed' }] },
    checkDefs: { readme: { block: 'docs', version: 1, check: 'readme', title: 'Explain how to run the app' } },
  } as unknown as ConformanceView
  expect(projectFact(p, view)).toMatch(/review/i)
  expect(projectFact(p, view)).not.toMatch(/^Fix:/)
  view.byProject.shop[0].lane = 'findings' // The refreshed owner projection includes the confirmation.
  expect(projectFact({ ...p, standing: 'does-not-conform', lanes: { findings: 1 } } as unknown as ProjectStanding, view)).toBe('Fix: Explain how to run the app')
})

it('does not describe an evaluator error as a confirmed problem in the app', () => {
  const p = { slug: 'shop', lanes: { broken: 1 }, notKnown: [{ detail: 'scanner: scanner unavailable', evaluator: 'tool' }], total: 1 } as unknown as ProjectStanding
  const view = { byProject: { shop: [{ lane: 'broken', state: 'error', detail: 'scanner unavailable' }] } } as unknown as ConformanceView
  expect(projectFact(p, view)).toBe("1 check couldn't run")
})

it('labels the project activity age on Home with what the timestamp measures', () => {
  const el = page(<Dashboard />, {
    '/activity': { agents: [], active: [], history: [], deployments: [] }, '/dashboard': {},
    '/projects?limit=5&order=mine': { projects: [{ id: 'shop', slug: 'shop', name: 'Shop', hasDeployments: false, lastRunAt: '2026-10-31T01:00:00Z' }], total: 1 },
  })
  expect(el.querySelector('.project-state > .muted')?.textContent).toMatch(/^Work updated /)
})

// A new policy is a page of its own, so its heading names it and the top bar
// leads back to Policies; its rule is a choice whose words wrap on a phone.
it('opens a new policy on its own page, with the rules as choices', () => {
  const list = page(<WaysOfWorking tab="policies" />, { '/policy-library': { library: [] }, '/projects': [] })
  const add = [...list.querySelectorAll('a')].find(a => a.textContent === 'New policy')
  expect(add?.getAttribute('href')).toBe('/policies/new')
  expect(routeAncestors('/policies/new').at(-1)).toEqual({ to: '/policies', label: 'Policies' })
  const New = (Policies as Record<string, unknown>).PolicyNewPage as () => React.ReactElement
  const form = page(<New />)
  expect(form.querySelector('h1')?.textContent).toBe('New policy')
  expect(form.querySelector('select')).toBeNull()
  const choices = [...form.querySelectorAll('input[type="radio"]')].map(r => r.closest('label')?.textContent)
  expect(choices).toEqual(['step X requires step Y in an earlier stage', 'capability must have a binding', 'scan failOn may not be looser than…'])
  expect(form.querySelector('input[type="radio"]:checked')?.closest('label')?.textContent).toBe('step X requires step Y in an earlier stage')
  // The fields say what each step is; the saved keys stay uses and requires.
  const labels = [...form.querySelectorAll('.policies-page-span > label')].map(l => l.textContent)
  expect(labels).toEqual(['Step X: the step that needs it (e.g. build)', 'Step Y: must run in an earlier stage (e.g. scan)'])
  expect(form.querySelector('input[aria-label="Step X: the step that needs it (e.g. build)"]')).not.toBeNull()
})

// A policy's source block is headed Source, under its plain reading.
it('heads a policy source block Source', () => {
  const policy = { name: 'scan-before-build', description: '', rule: 'requireStepBefore', with: { uses: 'build', requires: 'scan' }, origin: 'stock', adoptedBy: 0, nonConformant: 0, packs: [], reading: 'A workflow with a "build" step must have a "scan" step in an earlier stage.', fix: 'Add a "scan" step.' }
  const client = newQueryClient()
  client.setQueryData(getKey('/policy-library'), { library: [policy] }); client.setQueryData(getKey('/projects'), [])
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter initialEntries={['/policies/scan-before-build']}><QueryClientProvider client={client}><Routes><Route path="/policies/:name" element={<Policies.default page />} /></Routes></QueryClientProvider></MemoryRouter>)
  const heads = [...el.querySelectorAll('h3')].map(h => h.textContent)
  expect(heads).toContain('Source')
  expect(heads).not.toContain('definition')
  expect(el.textContent).toContain('uses: "build"')
})

// A workflow's pacing is said in the server's words, not its kind key.
it.each([['pipeline', 'Runs straight through'], ['lifecycle', 'Paced by people']])('says a %s workflow is %s', (kind, words) => {
  const client = newQueryClient()
  client.setQueryData(getKey('/workflows/library'), [{ name: 'review', description: '', tier: 'core', latestVersion: 1, stages: ['work', 'check'], kind, yaml: 'name: review', origin: 'stock' }])
  client.setQueryData(getKey('/system-workflows'), { bindings: [], candidates: [] })
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter initialEntries={['/workflows/review']}><QueryClientProvider client={client}><Routes><Route path="/workflows/:name" element={<WorkflowLibrary page />} /></Routes></QueryClientProvider></MemoryRouter>)
  expect(el.textContent).toContain(`${words} · stages: work → check`)
  expect(el.textContent).not.toContain(`${kind} ·`)
})

// The new-workflow template explains its choices in YAML comments and still
// reads as the same workflow.
it('explains the new workflow template in comments that leave it valid YAML', () => {
  expect(NEW_TEMPLATE).toContain('  # core: on the short list\n  # extra: the longer list\n  tier: extra')
  expect(NEW_TEMPLATE).toContain('# AI agent works\n          uses: agent/pi')
  // A pipeline still waits for failing checks and approvals: the hint keeps that condition.
  expect(NEW_TEMPLATE).toContain('# pipeline: next stage\n  #   once checks pass')
  expect(NEW_TEMPLATE).not.toContain('never pauses')
  // Each comment line fits a phone-width editor (about 30 characters at large text) whole.
  for (const line of NEW_TEMPLATE.split('\n').filter(l => l.includes('#') || l.includes('description:'))) expect(line.length).toBeLessThanOrEqual(28)
  const doc = loadYaml(NEW_TEMPLATE) as { metadata: { tier: string }; spec: { kind: string; stages: { steps: { uses: string }[] }[] } }
  expect(doc.metadata.tier).toBe('extra')
  expect(doc.spec.kind).toBe('lifecycle')
  expect(doc.spec.stages[0].steps.map(s => s.uses)).toEqual(['agent/pi', 'approval'])
})
