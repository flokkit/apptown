// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import type { ChangeView } from '../../api'
import { HttpError } from '../../api'
import { getKey, newQueryClient } from '../../data'
import ChangePage from '../ChangePage'
import { Route, Routes } from 'react-router-dom'
import { LivePanel } from './LivePanel'
import { GoesTo } from './GoesTo'
import { ChangeHeader } from './Head'
import { releaseProgress } from './DeployScene'

const release = {number:1, deployment:'docker', state:'releasing', statusLabel:'deploying', stage:'building', stageElapsedMs:1200, progressUncertain:false, hostOnly:false} as ChangeView['evidence']['landingReleases'][number]
const view = (): ChangeView => ({change:{id:'change', number:1, title:'Calendar', state:'shipped', body:'', createdAt:''}, goesTo:{branch:'trunk', deployments:[{name:'docker',displayLabel:'Staff',targetLabel:'Host',audience:'team'}]}, waitingOn:{kind:'closed',says:'landed',tone:'ok',you:'no',who:[],checks:0}, checks:{tone:'ok',text:'All checks passed',started:true}, evidence:{landingReleases:[{...release}], releases:[], targets:[], merged:true}, projectSlug:'club',runs:[],commits:[],adrs:[],usage:{total:0,costMicros:0},editing:0, next:[], committedDiff:{status:'empty'},detail:null} as unknown as ChangeView)
const dom = (v: ChangeView, current = true) => {
 const el = document.createElement('div'); el.innerHTML = renderToStaticMarkup(<MemoryRouter><LivePanel view={v} progressCurrent={current} /></MemoryRouter>); return el
}

it('keeps every release endpoint beside its state and its own stage list', () => {
 const v = view()
 v.evidence.landingReleases = [{...release,url:'https://staff.example.test'}, {...release,number:2,deployment:'fly',state:'failed',statusLabel:'failed',failedStage:'starting',failure:'Health check failed',url:'https://public.example.test'}]
 const el = dom(v)
 expect(el.querySelectorAll('[aria-label="Deployment stages"]')).toHaveLength(2)
 expect([...el.querySelectorAll('a')].filter(a=>a.textContent==='Open app').map(a=>a.getAttribute('href'))).toEqual(['https://staff.example.test','https://public.example.test'])
 expect(el.textContent).toContain('failed')
 expect(el.textContent).toContain('Health check failed')
 expect(el.querySelector('a[href="/projects/club/releases/2"]')).not.toBeNull()
})

it('offers Open app only for reachable addresses', () => {
 const v = view()
 expect(dom(v).textContent).toContain('Address not known yet')
 expect(dom(v).querySelector('a.btn')).toBeNull()
 v.evidence.landingReleases[0] = {...release,url:'http://localhost:8080',hostOnly:true}
 expect(dom(v).textContent).toContain('host only')
 expect(dom(v).textContent).toContain('http://localhost:8080')
 expect(dom(v).querySelector('a.btn')).toBeNull()
 v.evidence.landingReleases[0].hostOnly = false
 expect(dom(v).querySelector('a.btn')?.getAttribute('href')).toBe('http://localhost:8080')
})

it('pauses uncertain and old progress rather than claiming execution or completion', () => {
 const v = view()
 v.evidence.landingReleases[0] = {...release,stage:undefined,lastStage:'building',progressUncertain:true}
 for (const el of [dom(v), dom(view(), false)]) {
  expect(el.textContent).toContain('Deployment progress uncertain')
  expect(el.querySelector('.busy')).toBeNull()
  expect(el.querySelector('[aria-current="step"] .ok')).toBeNull()
 }
 expect(releaseProgress(v.evidence.landingReleases[0]).outcome).toBe('paused')
 expect(dom(view(), false).textContent).toContain('Old reading')
})

it('keeps empty release requests neutral even when a deployment follows main', () => {
 const v = view(); v.evidence.landingReleases = []
 expect(dom(v).textContent).toContain('No release requested.')
 expect(dom(v).textContent).not.toContain('no deployment follows main')
 v.change.state = 'landing'
 expect(dom(v).textContent).toContain('No release requested yet.')
})

it('shows current destinations in the change header with audience and none wording', () => {
 const v = view(); v.change.state = 'in_progress'
 v.goesTo.deployments = ['team','public','partners','organisation'].map(audience=>({name:audience,displayLabel:audience,targetLabel:'Host',audience}))
 const client = newQueryClient()
 const el = document.createElement('div'); el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><ChangeHeader view={v} compact /></QueryClientProvider></MemoryRouter>)
 expect(el.textContent).toContain('Goes to: trunk → team, public (public), partners (partners), organisation (organisation)')
 expect(el.querySelectorAll('a')).toHaveLength(4)
 v.goesTo.deployments = []
 expect(renderToStaticMarkup(<MemoryRouter><GoesTo view={v} /></MemoryRouter>)).toContain('no deployment follows main')
 v.change.state = 'shipped'
 expect(renderToStaticMarkup(<MemoryRouter><GoesTo view={v} /></MemoryRouter>)).toBe('')
})

it('shows a release on the next cockpit poll and pauses retained facts after a failed refresh', async () => {
 ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
 const v = view(); v.evidence.landingReleases = []
 let answer = v, failed = false
 vi.stubGlobal('fetch', vi.fn(async (url: RequestInfo | URL) => {
  if (String(url).endsWith('/changes/change')) {
   if (failed) throw new HttpError(503, 'Cannot read change')
   return new Response(JSON.stringify(answer))
  }
  return new Response(JSON.stringify({state:'none',features:[],sessions:[]}))
 }))
 const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
 try {
  await act(async () => root.render(<MemoryRouter initialEntries={['/changes/change']}><QueryClientProvider client={client}><Routes><Route path="/changes/:id" element={<ChangePage />} /></Routes></QueryClientProvider></MemoryRouter>))
  await vi.waitFor(() => expect(el.textContent).toContain('No release requested.'))
  answer = view()
  await act(async () => { await client.invalidateQueries({queryKey:getKey('/changes/change')}) })
  await vi.waitFor(() => expect(el.querySelector('[aria-label="Deployment stages"]')).not.toBeNull())
  expect(el.querySelector('.busy')).not.toBeNull()
  expect(el.querySelector('dialog, #decision')).toBeNull()
  failed = true
  await act(async () => { await client.invalidateQueries({queryKey:getKey('/changes/change')}) })
  await vi.waitFor(() => expect(el.textContent).toContain('Old reading'))
  expect(el.querySelector('[aria-label="Deployment stages"]')).not.toBeNull()
  expect(el.querySelector('.busy')).toBeNull()
  expect(el.textContent).not.toContain('No release requested')
 } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
