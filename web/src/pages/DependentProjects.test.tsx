import { HowJudged } from './conformance/parts'
// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { settled } from '../settled'
import { CheckProjects, GuidanceProjects, IntegrationProjects, PackProjects, PolicyProjects } from './DependentProjects'
import { ModelProfiles, ModelProfilePage } from './integrations/ModelSlots'
import GuidanceBlockPage from './GuidanceBlockPage'
import PoliciesPage from './PoliciesPage'

// A status's details ride in its title, not in the page text.
const details = (root: Element) => [...root.querySelectorAll('[data-help], [title]')].map(e => e.getAttribute('data-help') ?? e.getAttribute('title')).join(' | ')

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const cleanups: (() => void)[] = []
afterEach(() => { cleanups.splice(0).forEach(f => f()); vi.unstubAllGlobals() })
async function mount(path: string, routes: ReactNode, answers: Record<string, unknown>) {
  const reads: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    const path = url.replace('/api/v1', '')
    reads.push(path)
    return new Response(JSON.stringify(answers[path] ?? {error:'not found'}), {status: path in answers ? 200 : 404})
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  cleanups.push(() => { act(() => root.unmount()); client.clear() })
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><Routes>{routes}</Routes></MemoryRouter></QueryClientProvider>))
  await settled(client)
  return {el, reads, click: async (selector: string) => {
    await act(async () => (el.querySelector(selector) as HTMLAnchorElement).click())
    await settled(client)
  }}
}
const projects = Array.from({length:100}, (_,i) => ({slug:`app-${i}`,name:`Project ${i}`,profile:'standard'}))

it('keeps a hundred profile dependents out of the row and makes every project reachable on its page', async () => {
  const {el,click} = await mount('/integrations/profiles', <>
    <Route path="/integrations/profiles" element={<ModelProfiles title="Model profiles" />} />
    <Route path="/integrations/profiles/:profile/projects" element={<ModelProfilePage projects />} />
  </>, {'/model-slots':{profiles:['standard'],slots:[],projects}, '/model-catalog':{models:[],unlisted:[]}})
  expect(el.textContent).not.toContain('Project 0')
  expect(el.querySelector('a[href$="/standard/projects"]')?.textContent).toBe('100 projects')
  await click('a[href$="/standard/projects"]')
  expect(el.querySelector('h1')?.textContent).toBe('Projects')
  expect(el.querySelectorAll('a[href^="/projects/app-"]')).toHaveLength(100)
})

it('reads policy adopters only on their projects page and preserves the reasons a project does not conform', async () => {
  const {el,reads,click} = await mount('/policies', <>
    <Route path="/policies" element={<PoliciesPage />} />
    <Route path="/policies/:name/projects" element={<PolicyProjects />} />
  </>, {'/projects':[], '/policy-library':{library:[{name:'review',origin:'stock',description:'Review changes',adoptedBy:100,nonConformant:1,packs:[],with:{}}]},
    '/policy-library/review/usage':{usedBy:projects.map((p,i)=>({slug:p.slug,project:p.name,conformant:i>0,violations:i>0?[]:[{workflow:'ship',detail:'A review is missing'}]}))}})
  expect(el.textContent).not.toContain('Project 0')
  expect(reads).not.toContain('/policy-library/review/usage')
  await click('a[href="/policies/review/projects"]')
  expect(el.querySelectorAll('a[href^="/projects/app-"]')).toHaveLength(100)
  expect(el.textContent).toContain('A review is missing')
})

it('links guidance usage to a page retaining each version and turned-off state', async () => {
  const block = {block:{id:'guide',name:'House rules',origin:'stock',bodyMd:'Be clear.',tags:'',bytes:9,latestVersion:2},shownVersion:2,versions:[],checks:[],usedBy:projects.map((p,i)=>({projectSlug:p.slug,version:i+1,disabled:i===0}))}
  const {el,click} = await mount('/guidance/guide', <>
    <Route path="/guidance/:id" element={<GuidanceBlockPage />} />
    <Route path="/guidance/:id/projects" element={<GuidanceProjects />} />
  </>, {'/guidance/blocks/guide':block})
  expect(el.textContent).not.toContain('app-0')
  await click('a[href="/guidance/guide/projects"]')
  expect(el.querySelectorAll('a[href^="/projects/app-"]')).toHaveLength(100)
  expect(el.textContent).toContain('app-0 · v1 · turned off')
})

// A pack's projects say which version each is on and whether it is behind;
// each opens the project's Way of working settings, where it is updated.
it("lists a pack's projects with their version and opens their adoption settings", async () => {
  const fleet = {packs:[{pack:'house',recommended:2,versions:[{version:1,projects:['older']},{version:2,projects:['newer']}]}]}
  const {el} = await mount('/packs/house/projects', <Route path="/packs/:name/projects" element={<PackProjects />} />, {'/wow':fleet})
  const row = (slug: string) => el.querySelector(`a[href="/projects/${slug}/settings/working"]`)!.closest('tr')!
  expect(row('older').textContent).toContain('v1')
  expect(row('older').textContent).toContain('update available')
  expect(row('newer').textContent).toContain('current')
})

it("filters a pack's projects to one version", async () => {
  const fleet = {packs:[{pack:'house',recommended:2,versions:[{version:1,projects:['older']},{version:2,projects:['newer']}]}]}
  const {el} = await mount('/packs/house/projects?version=1', <Route path="/packs/:name/projects" element={<PackProjects />} />, {'/wow':fleet})
  expect(el.querySelector('a[href="/projects/older/settings/working"]')).not.toBeNull()
  expect(el.querySelector('a[href="/projects/newer/settings/working"]')).toBeNull()
})

it('keeps service project and deployment links on the dependent page and reports an unreadable list', async () => {
  const {el} = await mount('/services/errors/projects', <Route path="/services/:name/projects" element={<IntegrationProjects />} />, {
    '/integrations':{integrations:[{name:'errors',usedBy:{projects:[{slug:'club',name:'Club'}],deployments:[{project:'club',name:'main',target:'host'}]}}]},
  })
  expect(el.querySelector('a[href="/projects/club"]')?.textContent).toBe('Club')
  expect(el.querySelector('a[href="/projects/club/deployments/main"]')).not.toBeNull()
  expect(el.querySelector('a[href="/targets/host"]')).not.toBeNull()
  const failed = await mount('/services/errors/projects', <Route path="/services/:name/projects" element={<IntegrationProjects />} />, {})
  expect(details(failed.el)).toContain('could not be read')
  expect(failed.el.textContent).not.toContain('directly')
})

it('keeps check adopters behind a count link, including adopters without a result', async () => {
  const def = {key:'clear',title:'Clear text',block:'guide',version:1,check:'clear',expects:'Clear text',evaluator:'sh',judgedWhere:'at review',severity:'warn',scope:'main',adopters:projects.map(p=>p.slug),files:[]}
  const {el,click} = await mount('/conformance/checks/clear', <>
    <Route path="/conformance/checks/:checkKey" element={<HowJudged def={def} />} />
    <Route path="/conformance/checks/:checkKey/projects" element={<CheckProjects />} />
  </>, {'/conformance':{checkDefs:{clear:def},lens:{kind:'current'}}})
  expect(el.textContent).not.toContain('app-0')
  expect(el.querySelector('a[href$="/clear/projects"]')?.textContent).toBe('100 projects')
  await click('a[href$="/clear/projects"]')
  expect(el.querySelectorAll('a[href^="/conformance/projects/app-"]')).toHaveLength(100)
})
