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
import JanitorSettings from './janitor/Settings'
import type { JanitorView, JanitorHistoryView, JanitorProject, StandingRevision } from '../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const at = '2026-11-12T10:00:00Z'
const actor = { name: 'Sam', principal: 'person', via: 'session' }
const queue = (name: string, stopped = false) => ({ title: name === 'updates' ? 'Dependency updates' : name === 'packs' ? 'Pack updates' : 'Security fixes', queue: name, state: stopped ? 'stopped' : 'up_to_date', words: stopped ? 'stopped after 3 failed tries' : 'up to date', failed: stopped ? 3 : 0, reason: '', since: at, files: [], packages: [], changeNumber: 0 })
const project = (id: string): JanitorProject => ({ projectId: id, slug: id, name: `${id} app`, level: 'routine', state: { word: 'up to date', unknown: false, help: '' },
 queues: [queue('security'), queue('updates'), queue('packs')], scans: { lastScan: at },
 procedures: [{ title: 'Declared dependency care', workflow: 'janitor-update', version: 7, stopRule: 'Weekly updates stop after 3 failed tries; security fixes can still run.', sentence: 'Up to 4 repair sessions; then asks Sam.', problem: '' }], scheduleProblem: '', runNow: '' })
const rule = (packs = false, enabled = true): StandingRevision => ({ ruleId: 'choices', revision: 3, enabled, issuer: actor, at, choices: { dependencies: enabled ? 'aggressive' : 'off', securityFixes: enabled, baseImages: enabled, toolchainPatches: enabled, packs } })
function fixture(): JanitorView {
 const stopped = project('Stopped'); stopped.queues[1] = queue('updates', true); stopped.state.word = 'needs you'
 const off = { ...project('Off'), level: 'off', state: { word: 'off', unknown: false, help: '' }, choice: { level: 'off', person: actor, at, reason: 'Waiting for a release' } }
 return { unknown: [], choices: rule(true), projects: [stopped, project('Held'), off], releaseHold: 'Stops after a failed release and never releases over a rollback.', deployments: [{ projectId: 'Held', deploymentId: 'dep', name: 'production', displayLabel: 'Customer office', setting: { mode: 'automatic' }, automatic: true, hold: { deploymentId: 'dep', deployment: 'production', displayLabel: 'Customer office', releaseNumber: 9, reason: 'release failed' } }] }
}
const history = (): JanitorHistoryView => ({ history: { changes: [{ changeId: 'landed', projectId: 'Held', project: 'Held app', title: 'Update library', queue: 'updates', landedAt: at, packages: [{ ecosystem: 'npm', package: 'library', from: '1.0.0', to: '1.1.0' }], packVersions: [] }], more: false }, releases: [{ changeId: 'landed', state: 'released', displayLabel: 'Customer office' }], since: at, until: at })
const cleanup: (() => void)[] = []
afterEach(() => { cleanup.splice(0).forEach(f => f()); vi.unstubAllGlobals() })
async function mount(fetcher: (url: string, init?: RequestInit) => Promise<Response>, manage = true, project: Record<string, boolean> = { 'change:create': true, 'change:invoke-lane': true }, settings = false) {
 vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => url.endsWith('/authz/me') ? new Response(JSON.stringify({ instance: { 'instance:manage-standing-approval': manage } }))
  : url.includes('/authz/me?project=') ? new Response(JSON.stringify({ instance: {}, project })) : fetcher(url, init)))
 const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
 client.setDefaultOptions({ queries: { retry: false } })
 cleanup.push(() => { act(() => root.unmount()); client.clear() })
 await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><StaleScope>{settings ? <JanitorSettings /> : <Janitor />}</StaleScope></MemoryRouter></QueryClientProvider>))
 return { client, el }
}
const json = (value: unknown) => new Response(JSON.stringify(value))




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









// Queue policy is shared; only differing adopted versions identify projects.




it('shows only decisions on the overview and opens evidence by row and release link', async () => {
 const data = fixture(); data.projects[0].queues[1].changeId = 'stuck-change'; data.projects[0].queues[1].reason = 'Checks failed\nLong evidence'
 const {client, el} = await mount(async url => json(url.includes('/history') ? history() : data))
 await settled(client)
 expect([...el.querySelectorAll('h2')].map(h => h.textContent)).toEqual(['Needs you', 'Projects', 'Recent work'])
 const needs = el.querySelector('table[aria-label="Needs you"]')!
 expect(needs.querySelectorAll('tbody tr')).toHaveLength(2)
 expect(needs.textContent).toContain('Checks failed')
 expect(needs.textContent).not.toContain('Long evidence')
 expect(needs.textContent).toContain('Close and try again')
 expect(needs.textContent).toContain('Release 9 to Customer office failed')
 expect(needs.querySelector('a[href="/changes/stuck-change"]')?.closest('tr')?.dataset.linked).toBe('true')
 expect(needs.querySelector('a[href="/projects/Held/deployments/production?tab=history"]')).not.toBeNull()
 const projects = el.querySelector('table[aria-label="Projects’ janitor state"]')!
 expect(projects.querySelectorAll('tbody tr')).toHaveLength(3)
 expect([...projects.querySelectorAll('th')].map(h => h.textContent)).toEqual(['Project', 'State', 'Last checked'])
 expect(projects.querySelector('a[href="/projects/Stopped#janitor"]')?.closest('tr')?.dataset.linked).toBe('true')
 expect(el.textContent).toContain('library 1.0.0 → 1.1.0')
 expect(el.textContent).toContain('Released to Customer office')
 expect(el.querySelector('a[href="/changes/landed?tab=changes"]')?.closest('tr')?.dataset.linked).toBe('true')
 expect(el.querySelector('a[href="/changes/landed?tab=details"]')).not.toBeNull()
 expect(el.querySelector('a[href="/janitor/settings"]')).not.toBeNull()
 expect(el.querySelector('details, summary')).toBeNull()
 expect([...el.querySelectorAll('button')].map(b => b.textContent)).not.toContain('Run now')
 for (const word of ['Revoke', 'Level', 'Findings', 'Granted by', 'View']) expect(el.textContent).not.toContain(word)
})

it.each([false, true])('keeps scheduler uncertainty visible beside attention=%s', async attention => {
 const data = fixture(); data.projects = [project('one')]; data.deployments = []
 data.projects[0].scheduleProblem = 'queue security was not read'
 if (attention) data.projects[0].queues[0] = queue('security', true)
 const {client, el} = await mount(async url => json(url.includes('/history') ? history() : data))
 await settled(client)
 const state = el.querySelector('td[data-label="State"]')!
 expect(state.textContent).toContain('not known')
 expect(state.querySelector('[data-help="queue security was not read"]')).not.toBeNull()
 expect(state.textContent).not.toContain('up to date')
 expect(!!el.querySelector('table[aria-label="Needs you"]')).toBe(attention)
 if (attention) expect(state.textContent).toContain('needs you')
})

it('requires successful releases and summarizes pack versions', async () => {
 const data = fixture(), h = history()
 h.history.changes.push({...h.history.changes[0], changeId: 'packs', packages: [], packVersions: [{pack:'apptown', version:42}, {pack:'frontend-only', version:38}]})
 h.releases[0].state = 'failed'
 h.releases.push({...h.releases[0], changeId:'packs', state:'released', displayLabel:'main'}, {...h.releases[0], changeId:'packs', state:'pending', displayLabel:'staging'})
 const {client, el} = await mount(async url => json(url.includes('/history') ? h : data))
 await settled(client)
 expect(el.querySelector('a[href="/changes/landed?tab=details"]')?.textContent).toBe('Not released')
 expect(el.querySelector('a[href="/changes/packs?tab=details"]')?.textContent).toBe('Released to main')
 expect(el.textContent).toContain('Pack versions: apptown v42, frontend-only v38')
})

it('keeps approvals and exception-only deployments in settings', async () => {
 const data=fixture()
 data.deployments.push({...data.deployments[0], deploymentId:'manual', displayLabel:'Manual copy', automatic:false, hold:undefined})
 data.deployments[0].setting={mode:'window', days:'Mon', from:'09:00', to:'17:00', zone:'UTC'}
 const {client, el}=await mount(async () => json(data), true, undefined, true)
 await settled(client)
 expect(el.textContent).toContain('What to keep updated')
 expect(el.textContent).toContain('All deployments except Held app / Manual copy')
 const settings=el.querySelector('table[aria-label="Release settings"]')!
 expect(settings.textContent).toContain('Mon, 09:00–17:00 (UTC)')
 expect(settings.textContent).not.toContain('Manual copy')
 data.deployments = [data.deployments[0]]; data.deployments[0].setting={mode:'automatic'}
 await act(async () => { await client.invalidateQueries({queryKey:['get','janitor']}) }); await settled(client)
 expect(el.textContent).toContain('All deployments')
 expect(el.textContent).not.toContain('All deployments accept updates automatically.')
 expect(el.querySelector('table[aria-label="Release settings"]')).toBeNull()
})

it('never claims automatic acceptance for a person-controlled deployment', async () => {
 const data = fixture()
 data.deployments[0] = { ...data.deployments[0], setting: { mode: 'person' }, automatic: false, hold: undefined }
 const { client, el } = await mount(async () => json(data), true, undefined, true)
 await settled(client)
 expect(el.textContent).toContain('Automatic releasesNone')
 expect(el.textContent).not.toContain('All deployments accept updates automatically.')
 expect(el.querySelector('table[aria-label="Release settings"]')).toBeNull()
})

it('lists time windows even without an enabled release approval', async () => {
 const data = fixture(); data.choices = null
 data.deployments[0].setting = { mode: 'window', days: 'Mon', from: '09:00', to: '17:00', zone: 'UTC' }
 const { client, el } = await mount(async () => json(data), true, undefined, true)
 await settled(client)
 const settings = el.querySelector('table[aria-label="Release settings"]')
 expect(settings).not.toBeNull()
 expect(settings?.textContent).toContain('Held app / Customer office')
 expect(settings?.textContent).toContain('Mon, 09:00–17:00 (UTC)')
})



it('groups instance dependency and pack approvals under all projects', async () => {
 const data = fixture()
 data.choices = rule(true)
 const { client, el } = await mount(async url => json(url.includes('/history') ? history() : data))
 await settled(client)
 expect(el.querySelector('.page-head-line')?.textContent).toBe('Dependencies including major, security fixes, base images, toolchain patches, packs')
  expect(el.querySelector('.page-head-actions a.primary')?.getAttribute('href')).toBe('/janitor/settings')
})

it('keeps loading, empty, failed and stale answers distinct', async () => {
 let complete!: (r:Response)=>void
 const pending = new Promise<Response>(resolve => {complete=resolve})
 const {client, el}=await mount(async url => url.includes('/history') ? json({...history(), history:{changes:[],more:false}}) : pending)
 expect(el.textContent).toContain('Loading…')
 expect(el.textContent).not.toContain('No projects yet')
 await act(async()=>complete(json({rules:[],projects:[],deployments:[],releaseHold:''}))); await settled(client)
 expect(el.textContent).toContain('Turned off')
 expect(el.textContent).toContain('No projects yet.')
 expect(el.querySelector('table[aria-label="Needs you"]')).toBeNull()
 vi.stubGlobal('fetch', vi.fn(async()=>new Response(JSON.stringify({error:'unavailable'}), {status:503})))
 await act(async()=>{await client.invalidateQueries({queryKey:['get','janitor']})}); await settled(client)
 expect(el.textContent).toContain('Refresh failed. Last answer shown.')
 expect(el.textContent).toContain('No projects yet.')
 expect([...el.querySelectorAll('button')].find(b=>b.textContent==='Run now for all projects')?.disabled).toBe(true)
})

it('reports first-read failure without claiming an empty result', async()=>{
 const {client,el}=await mount(async()=>new Response(JSON.stringify({error:'unavailable'}),{status:503}))
 await settled(client)
 expect(el.querySelector('[data-help*="unavailable"]')).not.toBeNull()
 expect(el.textContent).not.toContain('No projects yet')
 expect(el.textContent).not.toContain('up to date')
})

it('runs each enabled row once with at most two projects in flight and lists refusals by project', async () => {
 const data = fixture()
 data.projects = [project('one'), project('two'), project('three'), { ...project('off'), runNow: 'Turned off for this project' }]
 const posts: { url: string; body: unknown }[] = []
 let active = 0, most = 0
 const release: (() => void)[] = []
 const { client, el } = await mount(async (url, init) => {
  if (init?.method === 'POST') {
   posts.push({ url, body: JSON.parse(String(init.body)) }); active++; most = Math.max(most, active)
   await new Promise<void>(resolve => release.push(resolve))
   active--
   return json({ starts: [{ queue: 'packs', refused: 'Cannot start' }] })
  }
  return json(url.includes('/history') ? history() : data)
 })
 await settled(client)
 await act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Run now for all projects')!.click())
 expect(posts).toHaveLength(2)
 await act(async () => release.splice(0).forEach(done => done()))
 await vi.waitFor(() => expect(posts).toHaveLength(3))
 await act(async () => release.splice(0).forEach(done => done()))
 await settled(client)
 expect(most).toBe(2)
 expect(posts.map(p => p.url).sort()).toEqual(['one', 'two', 'three'].map(s => `/api/v1/projects/${s}/janitor/run`).sort())
 for (const p of posts) expect(p.body).toEqual({ queue: '', again: false, changeId: '' })
 for (const name of ['one', 'two', 'three']) expect(el.textContent).toContain(`${name} app · Cannot start`)
})

it('retries the exact stuck change and reports the refusal on its Needs-you row', async () => {
 const data=fixture(); data.projects[0].queues[1].changeId='blocked-change'
 const posts:unknown[]=[]
 const {client,el}=await mount(async(url,init)=>{
  if(init?.method==='POST'){posts.push(JSON.parse(String(init.body)));return json({starts:[{queue:'updates',refused:'The update could not start'}]})}
  return json(url.includes('/history')?history():data)
 },true,{'change:create':true,'change:invoke-lane':true,'change:abandon':true})
 await settled(client)
 const row=el.querySelector('table[aria-label="Needs you"] a[href="/changes/blocked-change"]')!.closest('tr')!
 await act(async()=>[...row.querySelectorAll('button')].find(b=>b.textContent==='Close and try again')!.click());await settled(client)
 expect(posts).toEqual([{queue:'updates',again:true,changeId:'blocked-change'}])
 expect(row.textContent).toContain('The update could not start')
})










it('shows choices and attribution, saves only edits at the expected revision', async () => {
 const calls: [string, unknown][]=[]
 const data=fixture()
 const {client,el}=await mount(async(url,init)=>{
  if(init?.method==='PUT'){const body=JSON.parse(String(init.body));calls.push([url,body]);data.choices={...data.choices!,choices:body.choices,revision:4};return json(data.choices)}
  return json(data)
 },true,undefined,true)
 await settled(client)
 expect(el.textContent).toContain('Set by Sam')
 expect(el.textContent).toContain('All projects, including new ones')
 expect(el.textContent).toContain('Off app · Turned off for this project · Sam')
 for(const word of ['Preview','Enable','Revoke','Changed meaning']) expect(el.textContent).not.toContain(word)
 const save=[...el.querySelectorAll('button')].find(b=>b.textContent==='Save')!
 expect(save.disabled).toBe(true)
 const select=el.querySelector('select')!
 await act(async()=>{select.value='routine';select.dispatchEvent(new Event('change',{bubbles:true}))})
 expect(save.disabled).toBe(false)
 await act(async()=>save.click())
 expect(calls).toEqual([['/api/v1/janitor/choices',{choices:{...data.choices!.choices,dependencies:'routine'},expectedRevision:3}]])
})
it('hides Save from readers and prevents a stale editor overwriting newer choices',async()=>{
 let data=fixture()
 const {client,el}=await mount(async()=>json(data),true,undefined,true)
 await settled(client)
 const select=el.querySelector('select')!
 await act(async()=>{select.value='routine';select.dispatchEvent(new Event('change',{bubbles:true}))})
 data={...data,choices:{...data.choices!,revision:4}}
 await act(async()=>{await client.invalidateQueries({queryKey:['get','janitor']})});await settled(client)
 expect([...el.querySelectorAll('button')].find(b=>b.textContent==='Save')?.disabled).toBe(true)
 expect(el.textContent).toContain('The choices changed')
 const reader=await mount(async()=>json(data),false,undefined,true);await settled(reader.client)
 expect([...reader.el.querySelectorAll('button')].some(b=>b.textContent==='Save')).toBe(false)
})
