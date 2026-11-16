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
const queue = (name: string, stopped = false) => ({ title: name === 'updates' ? 'Dependency updates' : name === 'packs' ? 'Pack updates' : 'Security fixes', queue: name, state: stopped ? 'stopped' : 'up_to_date', words: stopped ? 'AppTown could not finish this update' : 'up to date', failed: stopped ? 3 : 0, reason: '', since: at, files: [], packages: [], changeNumber: 0, changeSummary: '' })
const project = (id: string): JanitorProject => ({ projectId: id, slug: id, name: `${id} app`, level: 'routine', state: { word: 'up to date', unknown: false, help: '' },
 queues: [queue('security'), queue('updates'), queue('packs')],
 scheduleProblem: '' })
const rule = (packs = false, enabled = true): StandingRevision => ({ ruleId: 'choices', revision: 3, enabled, issuer: actor, at, choices: { dependencies: enabled ? 'aggressive' : 'off', securityFixes: enabled, baseImages: enabled, toolchainPatches: enabled, packs } })
function fixture(): JanitorView {
 const stopped = project('Stopped'); stopped.queues[1] = queue('updates', true); stopped.state.word = 'needs you'
 const off = { ...project('Off'), level: 'off', state: { word: 'off', unknown: false, help: '' }, choice: { level: 'off', person: actor, at } }
 return { unknown: [], choices: rule(true), projects: [stopped, project('Held'), off], deployments: [{ projectId: 'Held', deploymentId: 'dep', name: 'production', displayLabel: 'Customer office', automatic: true, hold: { deploymentId: 'dep', deployment: 'production', displayLabel: 'Customer office', releaseNumber: 9, reason: 'release failed' } }] }
}
const history = (): JanitorHistoryView => ({ history: { changes: [{ changeId: 'landed', projectId: 'Held', project: 'Held app', title: 'Update library', queue: 'updates', landedAt: at, packages: [{ ecosystem: 'npm', package: 'library', from: '1.0.0', to: '1.1.0' }], packVersions: [] }], more: false }, since: at, until: at })
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

it('shows decisions, exhausted retries and release holds with one route to their evidence', async () => {
 const data = fixture()
 data.projects[0].queues[1].changeId = 'stopped-change'
 data.projects[0].queues[1].reason = 'Technical fault must stay on the Change'
 const decision = data.projects[1].queues[1]
 Object.assign(decision, {state:'decide', words:'Report is missing', changeSummary:'Makes no change', changeId:'decision', decide:{act:'override',stepId:'check'}, files:[{path:'go.mod',class:'dependency'}]})
 data.projects[1].queues[0] = {...queue('security'), state:'retrying',words:'trying again',reason:'hidden retry'}
 const {client, el} = await mount(async url => json(url.includes('/history') ? history() : data))
 await settled(client)
 expect(el.querySelector('.page-head-line')?.textContent).toBe('2 projects need you')
 expect([...el.querySelectorAll('h2')].map(h => h.textContent)).toEqual(['Needs you', 'Recent updates'])
 const needs = el.querySelector('table[aria-label="Needs you"]')!
 expect(needs.querySelectorAll('tbody tr')).toHaveLength(3)
 expect(needs.textContent).toContain('AppTown could not finish this update')
 expect(needs.textContent).toContain('Makes no change · Report is missing')
 expect(needs.textContent).not.toContain('needs you')
 expect([...needs.querySelectorAll('button')].map(b => b.textContent)).toEqual(['Try again','Accept','Close'])
 expect(needs.textContent).toContain('Release 9 to Customer office failed')
 expect(needs.querySelector('a[href="/changes/decision"]')?.closest('tr')?.dataset.linked).toBe('true')
 expect(needs.querySelector('a[href="/projects/Held/deployments/production?tab=history"]')?.closest('tr')?.querySelector('button')).toBeNull()
 expect(el.querySelector('a[href="/changes/landed?tab=changes"]')?.closest('tr')?.dataset.linked).toBe('true')
 for (const text of ['Technical fault', 'hidden retry', 'Last checked', 'Projects’ janitor state', 'approval', 'Run now for all projects', 'Released', 'Close and try again']) expect(el.textContent).not.toContain(text)
 expect(el.querySelector('details, summary')).toBeNull()
})

it.each([false, true])('offers Run now for unreadable schedules even beside attention=%s', async attention => {
 const data = fixture(); data.projects = [project('one')]; data.deployments = []
 data.projects[0].scheduleProblem = 'queue security was not read'
 if (attention) data.projects[0].queues[0] = queue('security', true)
 const {client, el} = await mount(async url => json(url.includes('/history') ? history() : data))
 await settled(client)
 expect(el.querySelector('.page-head-line')?.textContent).toBe('1 project needs you')
 const row = [...el.querySelectorAll('tbody tr')].find(r => r.textContent?.includes("AppTown could not read this project's update schedule"))!
 expect(row.textContent).toContain('queue security was not read')
 expect(row.querySelector('button')?.textContent).toBe('Run now')
})

it('leaves scan uncertainty to Attention and excludes projects without queues', async () => {
 const data = fixture(); data.deployments=[]; data.projects=[project('one')]
 data.projects[0].state={word:'not known',unknown:true,help:'Scan not known'}
 data.projects.push({...project('empty'),queues:[],state:{word:'not known',unknown:true,help:'No scan'}})
 const {client,el}=await mount(async url=>json(url.includes('/history')?history():data)); await settled(client)
 expect(el.textContent).not.toContain('Scan not known')
 expect(el.textContent).not.toContain('empty app')
 expect(el.querySelector('.page-head-line')?.textContent).toBe('Keeping 1 project up to date')
})

it('names updated ways of working without versions', async () => {
 const h = history()
 h.history.changes.push({...h.history.changes[0], changeId:'packs',packages:[],packVersions:[{pack:'apptown',version:42},{pack:'wordpress',version:38}]})
 const {client, el} = await mount(async url => json(url.includes('/history') ? h : fixture()))
 await settled(client)
 expect(el.textContent).toContain('Ways of working: apptown, wordpress')
 for (const text of ['v42','v38','Released']) expect(el.textContent).not.toContain(text)
 expect([...el.querySelectorAll('table[aria-label="Landed janitor Changes"] th')].map(h=>h.textContent)).toEqual(['Project','What changed','When'])
})

it('keeps only update choices in settings', async () => {
 const data=fixture()
 data.deployments.push({...data.deployments[0],deploymentId:'manual',displayLabel:'Manual copy',automatic:false,hold:undefined})
 const {client,el}=await mount(async()=>json(data),true,undefined,true); await settled(client)
 expect([...el.querySelectorAll('h2')].map(h=>h.textContent)).toEqual(['What to keep updated'])
 expect(el.textContent).toContain('All deployments except Held app / Manual copy')
 for (const text of ['How it handles problems','Repair limits','Version','workflow','09:00']) expect(el.textContent).not.toContain(text)
})

it('never claims automatic acceptance for a person-controlled deployment', async () => {
 const data = fixture()
 data.deployments[0] = { ...data.deployments[0], automatic: false, hold: undefined }
 const { client, el } = await mount(async () => json(data), true, undefined, true)
 await settled(client)
 expect(el.textContent).toContain('Automatic releasesNone')
 expect(el.textContent).not.toContain('All deployments accept updates automatically.')
 expect(el.querySelector('table[aria-label="Release settings"]')).toBeNull()
})

it.each(['absent','all off','keeping','keeping one'])('states the top line for %s choices', async mode => {
 const data=fixture(); data.deployments=[]; data.projects=[project('one'),project('two'),{...project('off'),state:{word:'off',unknown:false,help:''}},{...project('no-repo'),state:{word:'no repository',unknown:false,help:''}}]
 if(mode==='keeping one') data.projects=[project('one')]
 if(mode==='absent') data.choices=null
 if(mode==='all off') data.choices=rule(false,false)
 const {client,el}=await mount(async url=>json(url.includes('/history')?history():data)); await settled(client)
 expect(el.querySelector('.page-head-line')?.textContent).toBe(mode==='keeping'?'Keeping 2 projects up to date':mode==='keeping one'?'Keeping 1 project up to date':'Janitor is off')
 expect(el.querySelector('table[aria-label="Needs you"]')).toBeNull()
})

it('keeps loading, empty, failed and stale answers distinct', async () => {
 let complete!: (r:Response)=>void
 const pending = new Promise<Response>(resolve => {complete=resolve})
 const {client, el}=await mount(async url => url.includes('/history') ? json({...history(), history:{changes:[],more:false}}) : pending)
 expect(el.textContent).toContain('Loading…')
 expect(el.textContent).not.toContain('No projects yet')
 await act(async()=>complete(json({choices:null,projects:[],deployments:[],unknown:[]}))); await settled(client)
 expect(el.textContent).toContain('Janitor is off')
 expect(el.querySelector('.page-head-line')?.textContent).toBe('Janitor is off')
 expect(el.querySelector('table[aria-label="Needs you"]')).toBeNull()
 vi.stubGlobal('fetch', vi.fn(async()=>new Response(JSON.stringify({error:'unavailable'}), {status:503})))
 await act(async()=>{await client.invalidateQueries({queryKey:['get','janitor']})}); await settled(client)
 expect(el.textContent).toContain('Refresh failed. Last answer shown.')
 expect(el.querySelector('.page-head-line')?.textContent).toBe('Janitor is off')
 expect(el.textContent).not.toContain('Run now for all projects')
})

it('reports first-read failure without claiming an empty result', async()=>{
 const {client,el}=await mount(async()=>new Response(JSON.stringify({error:'unavailable'}),{status:503}))
 await settled(client)
 expect(el.querySelector('[data-help*="unavailable"]')).not.toBeNull()
 expect(el.textContent).not.toContain('No projects yet')
 expect(el.textContent).not.toContain('up to date')
})

it('retries the exact stopped change and reports the refusal on its Needs-you row', async () => {
 const data=fixture(); data.projects[0].queues[1].changeId='blocked-change'
 const posts:unknown[]=[]
 const {client,el}=await mount(async(url,init)=>{
  if(init?.method==='POST'){posts.push(JSON.parse(String(init.body)));return json({starts:[{queue:'updates',refused:'The update could not start'}]})}
  return json(url.includes('/history')?history():data)
 },true,{'change:create':true,'change:invoke-lane':true,'change:abandon':true})
 await settled(client)
 const row=el.querySelector('table[aria-label="Needs you"] a[href="/changes/blocked-change"]')!.closest('tr')!
 await act(async()=>[...row.querySelectorAll('button')].find(b=>b.textContent==='Try again')!.click());await settled(client)
 expect(posts).toEqual([{queue:'updates',again:true,changeId:'blocked-change'}])
 expect(row.querySelector('[data-label="Why"]')?.textContent).toContain('The update could not start')
 expect(row.querySelector('.record-action')?.textContent).toBe('Try again')
})

it('saves each switch and select change once at the read revision and refreshes attribution', async () => {
 const calls: unknown[]=[]
 const data=fixture()
 const {client,el}=await mount(async(_url,init)=>{
  if(init?.method==='PUT') {
   const body=JSON.parse(String(init.body)); calls.push(body)
   data.choices={...data.choices!,choices:body.choices,revision:data.choices!.revision+1,issuer:{...actor,name:'Priya'}}
  }
  return json(data)
 },true,undefined,true)
 await settled(client)
 expect(el.textContent).toContain('Set by Sam')
 expect([...el.querySelectorAll('button')].some(b=>b.textContent==='Save')).toBe(false)
 const security=el.querySelector<HTMLButtonElement>('[role="switch"][aria-label="Security fixes"]')!
 await act(async()=>security.click()); await settled(client)
 expect(calls).toEqual([{choices:{...rule(true).choices,securityFixes:false,toolchainPatches:false},expectedRevision:3}])
 expect(security.getAttribute('aria-checked')).toBe('false')
 expect(el.querySelector<HTMLLabelElement>(`label[for="${security.id}"]`)?.textContent).toBe('Security fixes')
 expect(el.textContent).toContain('Set by Priya')
 const select=el.querySelector('select')!
 await act(async()=>{select.value='off';select.dispatchEvent(new Event('change',{bubbles:true}))}); await settled(client)
 expect(calls).toHaveLength(2)
 expect(calls[1]).toEqual({choices:{...data.choices!.choices,dependencies:'off',securityFixes:false,toolchainPatches:false},expectedRevision:4})
})
it('shows the conflict and current value without resubmitting a lost race',async()=>{
 const data=fixture(); let writes=0
 const {client,el}=await mount(async(_url,init)=>{
  if(init?.method==='PUT') {
   writes++
   data.choices={...data.choices!,revision:4,issuer:{...actor,name:'Priya'}}
   return new Response(JSON.stringify({error:'The choices changed. Read current choices.'}),{status:409})
  }
  return json(data)
 },true,undefined,true)
 await settled(client)
 await act(async()=>el.querySelector<HTMLButtonElement>('[role="switch"][aria-label="Packs"]')!.click()); await settled(client)
 expect(writes).toBe(1)
 expect(el.querySelector('[role="alert"]')?.textContent).toContain('The choices changed')
 expect(el.querySelector('[aria-label="Packs"]')?.getAttribute('aria-checked')).toBe('true')
 expect(el.textContent).toContain('Set by Priya')
 const reader=await mount(async()=>json(data),false,undefined,true); await settled(reader.client)
 expect([...reader.el.querySelectorAll<HTMLButtonElement>('[role="switch"]')].every(b=>b.disabled)).toBe(true)
})

it.each(['approve', 'override', 'close'])('acts on the supplied decision identity: %s', async operation => {
 const data=fixture(); data.projects=[project('one')]; data.deployments=[]
 Object.assign(data.projects[0].queues[1], {state:'decide',words:'needs your yes',changeId:'decision',decide:{act:operation==='approve'?'approve':'override',stepId:'exact-step'}})
 const calls: [string, unknown][]=[]
 const {client,el}=await mount(async(url,init)=>{
  if(init?.method==='POST'){calls.push([url,JSON.parse(String(init.body))]);return json({})}
  if(url.endsWith('/question')) return json({step:'exact-step',status:'pending',means:'The checked update is ready',instructions:'Read the changed files',landingMeans:['The checked code may land'],adds:{digest:'shown-digest',rows:['secrets','services','app parts'].map(name=>({name,added:[],removed:[]}))}})
  return json(url.includes('/history')?history():data)
 },true,{'change:approve':true,'change:override':true,'change:abandon':true})
 await settled(client)
 const button=(text:string)=>[...el.querySelectorAll('button')].find(b=>b.textContent===text)!
 const row=button('Close').closest('tr')!
 const why=row.querySelector('[data-label="Why"]')!
 const actions=row.querySelector('.record-action')!
 expect(actions.querySelector('input')).toBeNull()
 if(operation==='close') {
  await act(async()=>button('Close').click())
  expect(calls).toEqual([['/api/v1/projects/one/janitor/updates/skip',{changeId:'decision'}]])
 } else if(operation==='approve') {
  expect(why.textContent).toContain('needs your yes')
  expect(why.textContent).toContain('The checked update is ready')
  expect(why.textContent).toContain('Read the changed files')
  expect(actions.textContent).toBe('AcceptClose')
  expect(el.textContent).not.toContain('Approving means')
  expect(why.textContent).toContain('The checked code may land')
  await act(async()=>button('Accept').click())
  expect(calls).toEqual([['/api/v1/steps/exact-step/approve',{user:expect.any(String),decision:'approve',comment:'',alsoApproves:['The checked code may land'],adds:'shown-digest'}]])
 } else {
  await act(async()=>button('Accept').click())
  expect(why.textContent).toContain('This records an exception in your name')
  expect(actions.textContent).toBe('Accept anywayCancelClose')
  expect(actions.querySelector('input')).toBeNull()
  expect(why.querySelector('input[aria-label="Why (required)"]')).not.toBeNull()
  expect(button('Accept anyway').disabled).toBe(true)
  const input=el.querySelector('input[aria-label="Why (required)"]')!
  await act(async()=>{Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value')!.set!.call(input,'Checked the change');input.dispatchEvent(new Event('input',{bubbles:true}))})
  await act(async()=>button('Accept anyway').click())
  expect(calls).toEqual([['/api/v1/steps/exact-step/override',{user:expect.any(String),reason:'Checked the change'}]])
 }
})

it('disables decisions after a failed refresh', async()=>{
 const data=fixture(); data.projects=[project('one')];data.deployments=[]
 Object.assign(data.projects[0].queues[1],{state:'decide',words:'needs you',changeId:'decision',decide:{act:'override',stepId:'check'}})
 let failed=false
 const {client,el}=await mount(async url=>failed?new Response(JSON.stringify({error:'unavailable'}),{status:503}):json(url.includes('/history')?history():data),true,{'change:override':true,'change:abandon':true})
 await settled(client); failed=true
 await act(async()=>{await client.invalidateQueries({queryKey:['get','janitor']})});await settled(client)
 expect([...el.querySelectorAll('button')].filter(b=>['Accept','Close'].includes(b.textContent!)).every(b=>b.disabled)).toBe(true)
})

it('links both settings notes and names the project exception at its settings', async () => {
 const { client, el } = await mount(async () => json(fixture()), true, {}, true)
 await settled(client)
 expect(el.querySelector('a[href="/projects"]')?.textContent).toBe('change per project in its settings')
 expect(el.querySelector('a[href="/deployments"]')?.textContent).toBe('change per deployment in its settings')
 const off = el.querySelector('a[href="/projects/Off/settings/general#janitor"]')!
 expect(off.textContent).toBe('Off app')
 expect(off.parentElement?.textContent).toContain('Off app · Off · Sam')
})
