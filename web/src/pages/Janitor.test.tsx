// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { settled } from '../settled'
import { StaleScope } from '../ReadGate'
import Janitor from './Janitor'
import { dependencyDefinition, packDefinition } from './janitor/RuleConsent'
import type { JanitorView, JanitorHistoryView, JanitorProject, StandingRevision } from '../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const at = '2026-11-12T10:00:00Z'
const actor = { name: 'Sam', principal: 'person', via: 'session' }
const queue = (name: string, stopped = false) => ({ title: name === 'updates' ? 'Dependency updates' : name === 'packs' ? 'Pack updates' : 'Security fixes', queue: name, state: stopped ? 'stopped' : 'up_to_date', words: stopped ? 'stopped after 3 failed tries' : 'up to date', failed: stopped ? 3 : 0, reason: '', since: at, packages: [], changeNumber: 0 })
const project = (id: string): JanitorProject => ({ projectId: id, slug: id, name: `${id} app`, enabled: true, level: 'routine', state: { word: 'up to date', unknown: false, help: '' }, issuer: actor, grantedAt: at,
 queues: [queue('security'), queue('updates'), queue('packs')], scans: { main: 'abc', source: [], running: [], historical: [], noRepository: false, known: true, stale: false, lastScan: at },
 problem: '', procedures: [{ title: 'Declared dependency care', workflow: 'janitor-update', version: 7, repairRounds: 4, stopRule: 'Weekly updates stop after 3 failed tries; security fixes can still run.', sentence: 'Up to 4 repair sessions; then asks Sam.', problem: '' }], queueSentences: ['Security fixes stop after 3 failed tries.', 'Weekly updates stopped after 3 failed tries. Security fixes can still run.'], nextUpdate: at, nextReason: '', scanWorkflow: 'discover-issues', scanChangeNumber: 0, scanNow: '' })
const rule = (packs = false, enabled = true): StandingRevision => ({ ruleId: packs ? 'pack-rule' : 'dependency-rule', revision: 3, enabled, issuer: actor, enabledAt: at, changedBy: actor, changedAt: at, reason: '', question: { definition: packs ? packDefinition : dependencyDefinition(), shown: 'The exact approval meaning.', digest: 'meaning', projects: [], baseImageFindings: { images: 0, projects: 0, message: '' } } })
function fixture(): JanitorView {
 const stopped = project('Stopped'); stopped.queues[1] = queue('updates', true); stopped.state.word = stopped.queueSentences[1]
 const off = { ...project('Off'), level: 'off', state: { word: 'turned off for this project', unknown: false, help: '' }, nextReason: 'Turned off for this project', choice: { level: 'off', person: actor, at, reason: 'Waiting for a release' } }
 return { rules: [rule(), rule(true)], projects: [stopped, project('Held'), off], releaseHold: 'Stops after a failed release and never releases over a rollback.', deployments: [{ projectId: 'Held', deploymentId: 'dep', name: 'production', displayLabel: 'Customer office', setting: { mode: 'automatic' }, protected: false, automatic: true, sentence: 'Releases accepted updates automatically.', hold: { deploymentId: 'dep', deployment: 'production', displayLabel: 'Customer office', releaseNumber: 9, reason: 'release failed' } }] }
}
const history = (): JanitorHistoryView => ({ history: { changes: [{ changeId: 'landed', projectId: 'Held', slug: 'Held', project: 'Held app', number: 2, title: 'Update library', queue: 'updates', landedAt: at, packages: [{ ecosystem: 'npm', package: 'library', from: '1.0.0', to: '1.1.0' }], packVersions: [], rule: { kind: 'Dependency updates', scope: 'all projects' }, ruleId: 'original', ruleRevision: 1, issuer: actor, grantedAt: at, schedule: 'janitor-update' }], more: false }, releases: [{ changeId: 'landed', projectId: 'Held', number: 8, state: 'released', deployment: 'production', displayLabel: 'Customer office' }], since: at, until: at, offset: 0 })
const cleanup: (() => void)[] = []
afterEach(() => { cleanup.splice(0).forEach(f => f()); vi.unstubAllGlobals() })
async function mount(fetcher: (url: string, init?: RequestInit) => Promise<Response>, manage = true, project: Record<string, boolean> = { 'change:create': true, 'change:invoke-lane': true }) {
 vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => url.endsWith('/authz/me') ? new Response(JSON.stringify({ instance: { 'instance:manage-standing-approval': manage } }))
  : url.includes('/authz/me?project=') ? new Response(JSON.stringify({ instance: {}, project })) : fetcher(url, init)))
 const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
 client.setDefaultOptions({ queries: { retry: false } })
 cleanup.push(() => { act(() => root.unmount()); client.clear() })
 await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><StaleScope><Janitor /></StaleScope></MemoryRouter></QueryClientProvider>))
 return { client, el }
}
const json = (value: unknown) => new Response(JSON.stringify(value))

it('answers all four questions from records, with row links, grant attribution and release evidence', async () => {
 const { client, el } = await mount(async url => json(url.includes('/history') ? history() : fixture()))
 await settled(client)
 expect([...el.querySelectorAll('h2')].map(h => h.textContent)).toEqual(['What it is allowed to do', 'How it handles problems', 'What it is doing and waiting on', 'What it has done'])
 for (const text of ['Keep packs current', 'Routine · patch and minor', 'Up to 4 repair sessions', 'Security fixes can still run', 'Weekly updates stopped after 3 failed tries.', 'stopped: release 9 to Customer office failed', 'turned off for this project', 'Waiting for a release', 'library 1.0.0 → 1.1.0', 'Customer office · release 8', 'Dependency updates approval for all projects (revision 1), granted by Sam']) expect(el.textContent).toContain(text)
 expect(el.querySelector('a[href="/docs/guides/janitor"]')).not.toBeNull()
 expect(el.querySelector('table[aria-label="Projects’ janitor state"] a[href="/projects/Stopped#janitor"]')?.closest('tr')?.dataset.linked).toBe('true')
 expect(el.querySelector('a[href="/changes/landed?tab=details"]')).not.toBeNull()
 expect(el.querySelector('details')).toBeNull()
 expect([...el.querySelectorAll('button')].some(b => b.textContent === 'View')).toBe(false)
})

it('keeps loading, empty, failed and stale readings different in every section', async () => {
 let complete!: (r: Response) => void
 const pending = new Promise<Response>(resolve => { complete = resolve })
 const { client, el } = await mount(async url => url.includes('/history') ? json({ ...history(), history: { changes: [], more: false }, releases: [] }) : pending)
 expect(el.textContent).toContain('Loading…')
 expect(el.textContent).not.toContain('No projects yet')
 await act(async () => complete(json({ rules: [], projects: [], deployments: [], releaseHold: 'Release hold rule' })))
 await settled(client)
 expect(el.textContent).toContain('No projects yet.')
 expect(el.textContent).toContain('No project settings yet.')
 expect(el.textContent).toContain('No janitor Changes landed in the last 7 days.')
 vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({error:'read unavailable'}), { status: 503 })))
 await act(async () => client.invalidateQueries({ queryKey: ['get', 'janitor'] }))
 await settled(client)
 expect(el.textContent).toContain('Refresh failed. Last answer shown.')
 expect(el.textContent).toContain('No projects yet.')
 expect(el.querySelectorAll('.stale-note').length).toBe(4)
 expect([...el.querySelectorAll('button')].filter(b => b.textContent === 'Preview').every(b => b.disabled)).toBe(true)
})

it('shows errors without claiming an empty or up-to-date result', async () => {
 const { client, el } = await mount(async () => new Response(JSON.stringify({error:'unavailable'}), { status: 503 }))
 await settled(client)
 expect(el.textContent).not.toContain('No projects yet')
 expect(el.textContent).not.toContain('Up to date')
 expect(el.querySelectorAll('.error').length).toBe(4)
})

it('shows old scans and unknown findings separately from a clean result', async () => {
 const data = fixture(); data.projects[0].scans = { ...data.projects[0].scans, known: false, stale: true, reason: 'Scan older than three days' }
 const { client, el } = await mount(async url => json(url.includes('/history') ? history() : data), false)
 await settled(client)
 expect(el.textContent).toContain('Old scan')
 expect(el.textContent).toContain('Not known')
 expect([...el.querySelectorAll('button')].some(b => b.textContent === 'Revoke')).toBe(false)
})

it('reads a project with no repository as outside the janitor, not as unknown', async () => {
 const data = fixture(); data.projects[0].scans = { ...data.projects[0].scans, main: '', known: false, noRepository: true, reason: 'no repository yet, so there is nothing to scan' }; data.projects[0].state.word = 'no repository'
 const { client, el } = await mount(async url => json(url.includes('/history') ? history() : data), false)
 await settled(client)
 const row = [...el.querySelectorAll('tr')].find(r => r.textContent?.includes(data.projects[0].name))
 expect(row?.textContent).toContain('no repository')
})

it('previews and enables the shown pack meaning, then revokes its exact revision', async () => {
 let data: JanitorView = { ...fixture(), rules: [] }
 const calls: [string, unknown][] = []
 const { client, el } = await mount(async (url, init) => {
  if (init?.method === 'POST') {
   const body = JSON.parse(String(init.body)); calls.push([url, body])
   if (url.endsWith('/preview')) return json({ ...rule(true).question, definition: body, portfolio: [{ projectId: 'Held', project: 'Held', covered: false, needsPackUpgrade: ['language'], onlyChecksStarts: true, deployments: [] }] })
   data = { ...data, rules: url.endsWith('/disable') ? [] : [rule(true)] }; return json({})
  }
  return json(url.includes('/history') ? history() : data)
 })
 await settled(client)
 const packs = el.querySelector('table[aria-label="Janitor approvals"] tbody tr:last-child')!
 await act(async () => packs.querySelector('button')!.click())
 expect(calls[0][1]).toMatchObject({ categories: { waysOfWorking: true, dependencies: '' } }); expect(calls[0][1]).not.toHaveProperty('schedules')
 expect(el.textContent).toContain('The janitor will move these projects when Keep packs current is on.')
 const button = (word: string) => [...el.querySelectorAll('button')].find(b => b.textContent === word)!
 await act(async () => button('Enable').click()); await settled(client)
 expect(calls[1][1]).toMatchObject({ shownDigest: 'meaning', expectedRevision: 0 })
 await act(async () => button('Revoke').click()); await settled(client)
 expect(calls[2]).toEqual(['/api/v1/standing-approvals/pack-rule/disable', { expectedRevision: 3 }])
})

it('pages older history using the returned boundary and offset', async () => {
 const requests: string[] = []
 const { client, el } = await mount(async url => { requests.push(url); const h = history(); h.history.more = true; return json(url.includes('/history') ? h : fixture()) })
 await settled(client)
 const button = (text: string) => [...el.querySelectorAll('button')].find(b => b.textContent === text)!
 await act(async () => button('Older history').click()); await settled(client)
 expect(requests.some(url => url.includes('period=older') && url.includes('before='))).toBe(true)
 await act(async () => button('Next 20').click()); await settled(client)
 expect(requests.some(url => url.includes('period=older') && url.includes('offset=20'))).toBe(true)
})

it('keeps release destinations inside the enabled approval’s exact scope', async () => {
 const data = fixture()
 const exact = rule(); exact.question.definition = { ...exact.question.definition, scope: '', projects: [{ projectId: 'Held', workflow: 'janitor-update', environments: [] }] }
 data.rules = [exact]
 const { client, el } = await mount(async url => json(url.includes('/history') ? history() : data))
 await settled(client)
 const approval = el.querySelector('table[aria-label="Janitor approvals"] tbody tr')!
 expect(approval.textContent).not.toContain('Customer office')
 expect(approval.textContent).toContain('None')
})

it('enables against the revision that was previewed when a poll brings a newer grant', async () => {
 let current = rule(true, false)
 const commands: unknown[] = []
 const { client, el } = await mount(async (url, init) => {
  if (init?.method === 'POST') {
   const body = JSON.parse(String(init.body))
   if (url.endsWith('/preview')) return json({ ...current.question, definition: body })
   commands.push(body)
   return new Response(JSON.stringify({ error: 'The approval changed. Preview it again.' }), { status: 409 })
  }
  return json(url.includes('/history') ? history() : { ...fixture(), rules: [current] })
 })
 await settled(client)
 const button = (word: string) => [...el.querySelectorAll('button')].find(b => b.textContent === word)!
 await act(async () => button('Preview').click())
 current = { ...current, revision: 4 }
 await act(async () => { await client.invalidateQueries({ queryKey: ['get', 'janitor'] }) })
 await settled(client)
 await act(async () => button('Enable').click())
 expect(commands[0]).toMatchObject({ ruleId: 'pack-rule', expectedRevision: 3, shownDigest: 'meaning' })
 await vi.waitFor(() => expect(el.textContent).toContain('The approval changed. Preview it again.'))
})

it('counts apps with image-only findings once and shows landed pack versions', async () => {
 const data = fixture(), h = history()
 const scan = { findings: [], projectFindings: [], unclassifiedFindings: [], imageOnlyFindings: [{ fingerprint: 'base-one' }, { fingerprint: 'base-two' }] } as unknown as JanitorProject['scans']['running'][number]
 data.projects[0].scans.running = [scan, scan]
 data.projects[1].scans.running = [scan]
 h.history.changes[0] = { ...h.history.changes[0], queue: 'packs', title: 'Update packs', packages: [], packVersions: [{ pack: 'language', version: 3 }] }
 const { client, el } = await mount(async url => json(url.includes('/history') ? h : data))
 await settled(client)
 expect(el.textContent).toContain('2 apps’ images need a base-image update; the janitor does not do this yet.')
 expect(el.textContent).toContain('language version 3')
})

it('previews a changed dependency level against the existing disabled rule', async () => {
 const commands: unknown[] = []
 const data = { ...fixture(), rules: [rule(false, false)] }
 const { client, el } = await mount(async (url, init) => {
  if (init?.method === 'POST') { const body = JSON.parse(String(init.body)); commands.push(body); return json({ ...rule().question, definition: body, portfolio: [{ projectId: 'Held', project: 'Go code', covered: true, overridesInstance: true, level: 'routine', noMajorCommand: ['server part'], deployments: [], needsPackUpgrade: [] }] }) }
  return json(url.includes('/history') ? history() : data)
 })
 await settled(client)
 const select = el.querySelector<HTMLSelectElement>('select[aria-label="Dependency update level"]')!
 await act(async () => { select.value = 'aggressive'; select.dispatchEvent(new Event('change', { bubbles: true })) })
 await act(async () => el.querySelector<HTMLButtonElement>('table[aria-label="Janitor approvals"] tbody tr button')!.click())
 expect(commands[0]).toMatchObject({ categories: { dependencies: 'aggressive', waysOfWorking: false } })
 expect(el.textContent).toContain('Covered projects')
 expect(el.textContent).toContain('Go code · routine patch and minor updates')
 expect(el.textContent).toContain('major versions are not offered for server part')
})

it('reads current rules after a lost save reply before offering another enable', async () => {
 let saved = false, reads = 0, saves = 0
 const { client, el } = await mount(async (url, init) => {
  if (init?.method === 'POST') {
   const body = JSON.parse(String(init.body))
   if (url.endsWith('/preview')) return json({ ...rule(true).question, definition: body })
   saved = true; saves++; throw new TypeError('The save reply was lost')
  }
  if (url.includes('/history')) return json(history())
  reads++; return json({ ...fixture(), rules: saved ? [rule(true)] : [] })
 })
 await settled(client)
 await act(async () => el.querySelector<HTMLButtonElement>('table[aria-label="Janitor approvals"] tbody tr:last-child button')!.click())
 await act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Enable')!.click())
 await settled(client)
 expect(saves).toBe(1)
 expect(reads).toBeGreaterThan(1)
 expect([...el.querySelectorAll('button')].some(b => b.textContent === 'Revoke')).toBe(true)
 expect([...el.querySelectorAll('button')].some(b => b.textContent === 'Enable')).toBe(false)
})

// Queue policy is shared; only differing adopted versions identify projects.
it('shows queue rules once, version exceptions and only eligible release settings', async () => {
 const data = fixture()
 const other = project('Older'); other.procedures[0].version = 6
 data.projects.push(other)
 data.deployments.push({ ...data.deployments[0], deploymentId: 'manual', displayLabel: 'Manual copy', automatic: false, setting: { mode: 'person' } })
 const { client, el } = await mount(async url => json(url.includes('/history') ? history() : data))
 await settled(client)
 const policies = el.querySelector('table[aria-label="Repair limits"]')!
 expect(policies.querySelectorAll('tbody tr')).toHaveLength(2)
 expect(policies.textContent).toContain('janitor-update · version 7')
 expect([...policies.querySelectorAll('a')].map(a => a.textContent)).toEqual(['Older app'])
 expect(el.querySelector('table[aria-label="When updates stop"]')).toBeNull()
 const settings = el.querySelector('table[aria-label="Release settings"]')!
 expect(settings.textContent).toContain('Automatic')
 expect(settings.textContent).not.toContain('Manual copy')
 expect(settings.textContent).not.toContain('Releases accepted updates')
 const off = el.querySelector('table[aria-label="Projects’ janitor state"] a[href="/projects/Off#janitor"]')!.closest('tr')!
 expect(off.querySelector('td[data-label="Next update"]')?.textContent).toBe('Turned off for this project')
})

it('omits identical project lists from shared queue policy and shows both off-rule preview doors', async () => {
 const data = { ...fixture(), rules: [rule(false, false), rule(true, false)] }
 const { client, el } = await mount(async url => json(url.includes('/history') ? history() : data))
 await settled(client)
 const policies = el.querySelector('table[aria-label="Repair limits"]')!
 expect(policies.querySelectorAll('tbody tr')).toHaveLength(1)
 expect(policies.querySelectorAll('a')).toHaveLength(0)
 for (const row of el.querySelectorAll('table[aria-label="Janitor approvals"] tbody tr')) {
  expect([...row.querySelectorAll('button')].find(b => b.textContent === 'Preview')?.disabled).toBe(false)
 }
})

it('starts a project’s scan from its row and greys the action with the reason it cannot', async () => {
 const data = fixture()
 data.projects[1].scanNow = 'Turned off'
 data.projects[2] = { ...data.projects[2], level: 'routine', choice: undefined, scanNow: 'Already running: change #9', scanChangeId: 'scan-change', scanChangeNumber: 9, queues: [{ ...queue('security'), state: 'updating', changeId: 'scan-change', changeNumber: 9 }, queue('updates'), queue('packs')] }
 const posts: string[] = []
 let refuse = false
 const { client, el } = await mount(async (url, init) => {
  if (init?.method === 'POST') { posts.push(url); return refuse ? new Response(JSON.stringify({ error: 'already running: change #4' }), { status: 409 }) : json({ changeId: 'new', changeNumber: 10 }) }
  return json(url.includes('/history') ? history() : data)
 })
 await settled(client)
 const row = (slug: string) => el.querySelector(`table[aria-label="Projects’ janitor state"] a[href="/projects/${slug}#janitor"]`)!.closest('tr')!
 const scan = (slug: string) => [...row(slug).querySelectorAll('button')].find(b => b.textContent === 'Scan now')!
 expect(scan('Held').disabled).toBe(true)
 expect(row('Held').textContent).toContain('Turned off')
 expect(scan('Off').disabled).toBe(true)
 expect(row('Off').querySelector('a[href="/changes/scan-change"]')?.textContent).toBe('change #9')
 expect(scan('Stopped').disabled).toBe(false)
 await act(async () => scan('Stopped').click()); await settled(client)
 expect(posts).toEqual(['/api/v1/projects/Stopped/schedules/discover-issues/run'])
 refuse = true
 await act(async () => scan('Stopped').click()); await settled(client)
 await vi.waitFor(() => expect(row('Stopped').textContent).toContain('already running: change #4'))
})

it('greys the scan action when the person lacks either permission to start work', async () => {
 for (const project of [{ 'change:create': false, 'change:invoke-lane': true }, { 'change:create': true, 'change:invoke-lane': false }]) {
  const { client, el } = await mount(async url => json(url.includes('/history') ? history() : fixture()), true, project)
  await settled(client)
  const row = el.querySelector('table[aria-label="Projects’ janitor state"] a[href="/projects/Stopped#janitor"]')!.closest('tr')!
  expect([...row.querySelectorAll('button')].find(b => b.textContent === 'Scan now')!.disabled).toBe(true)
  expect(row.textContent).toContain('Needs permission to start work')
  cleanup.splice(0).forEach(f => f())
 }
})

it('preserves an exact project pack approval when previewing its categories', async () => {
 const exact = rule(true, false)
 exact.question.definition = { ...exact.question.definition, scope: '', projects: [{ projectId: 'Held', workflow: 'keep-packs', environments: [] }] }
 let posted: unknown
 const { client, el } = await mount(async (url, init) => {
  if (init?.method === 'POST') { posted = JSON.parse(String(init.body)); return json({ ...exact.question, definition: posted }) }
  return json(url.includes('/history') ? history() : { ...fixture(), rules: [exact] })
 })
 await settled(client)
 const row = [...el.querySelectorAll('table[aria-label="Janitor approvals"] tbody tr')].find(r => r.textContent?.includes('Keep packs current'))!
 await act(async () => row.querySelector<HTMLButtonElement>('button')!.click())
 expect(posted).toMatchObject({ scope: '', categories: { waysOfWorking: true, dependencies: '' }, projects: [{ projectId: 'Held', workflow: 'keep-packs' }] })
})

 it('shows the owner’s state beside a deployment hold', async () => {
 const data = fixture()
 data.projects[1].state = { word: 'waiting for a person', unknown: false, help: '' }
 const { client, el } = await mount(async url => json(url.includes('/history') ? history() : data))
 await settled(client)
 const row = el.querySelector('table[aria-label="Projects’ janitor state"] a[href="/projects/Held#janitor"]')!.closest('tr')!
 const state = row.querySelector('td[data-label="State"]')!
 expect(state.textContent).toContain('waiting for a person')
 expect(state.textContent).toContain('stopped: release 9 to Customer office failed')
 })
