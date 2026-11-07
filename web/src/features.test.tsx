import { SidebarNav } from './App'
// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { newQueryClient } from './data'
import { settled } from './settled'
import { FeatureGate } from './features'
import Features from './pages/Features'
import { enabledFeatures } from './features.fixture'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const cleanups: (() => void)[] = []
afterEach(() => { cleanups.splice(0).forEach(f => f()); vi.unstubAllGlobals() })
async function mount(node: React.ReactNode, at = '/') {
 const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
 cleanups.push(() => { act(() => root.unmount()); client.clear() })
 await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[at]}>{node}</MemoryRouter></QueryClientProvider>))
 await settled(client)
 return { el, client }
}
it.each([true, false])('refuses a disabled area and links Features only for an administrator (%s)', async admin => {
 vi.stubGlobal('fetch', vi.fn(async (path: string) => new Response(JSON.stringify(
  path.endsWith('/authz/me') ? { instance: { 'identity:manage-auth': admin } } : enabledFeatures.map(f => f.area === 'evals' ? { ...f, name: 'Evals', enabled: false, tier: 'beta' } : f)
 ))))
 const page = vi.fn(() => <p>Evaluation data</p>)
 const Page = page
 const { el } = await mount(<FeatureGate area="evals"><Page /></FeatureGate>)
 expect(page).not.toHaveBeenCalled()
 expect(el.textContent).toContain('Evals is turned off; an administrator can turn it on in Features')
 expect(el.querySelector('a')?.getAttribute('href') ?? null).toBe(admin ? '/features' : null)
})
it('shows the beta tag and uses the settings API for an administrator switch', async () => {
 const calls: { path: string; body: unknown }[] = []
 vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
  if (path.endsWith('/authz/me')) return new Response(JSON.stringify({ instance: { 'identity:manage-auth': true } }))
  if (init?.method === 'PUT') { calls.push({ path, body: JSON.parse(String(init.body)) }); return new Response('{}') }
  return new Response(JSON.stringify(enabledFeatures.map(f => f.area === 'evals' ? { ...f, name: 'Evals', enabled: false, tier: 'beta' } : f)))
 }))
 const { el, client } = await mount(<Features />)
 expect(el.querySelectorAll('tbody tr').length).toBe(enabledFeatures.filter(f => f.tier !== 'always').length)
 expect(el.textContent).toContain('Beta')
 expect([...el.querySelectorAll('h2')].map(h => h.textContent)).toEqual(['On by default', 'Beta'])
 expect(el.querySelectorAll('a')).toHaveLength(1)
 expect(el.querySelector('a')?.getAttribute('href')).toBe('/docs/guides/features')
 for (const area of ['ways', 'usage', 'previews', 'complaints']) expect(el.querySelector(`[aria-label^="${area}:"]`)).toBeNull()
 const button = el.querySelector<HTMLButtonElement>('[role="switch"][aria-label="Evals"]')!
 expect(button.disabled).toBe(false)
 expect(button.getAttribute('aria-checked')).toBe('false')
 expect(button.textContent).toBe('')
 const onButton = el.querySelector<HTMLButtonElement>('[role="switch"][aria-label="backups"]')!
 expect(onButton.getAttribute('aria-checked')).toBe('true')
 expect(onButton.textContent).toBe('✓')
 await act(async () => button.click())
 await settled(client)
 expect(calls).toEqual([{ path: '/api/v1/features/evals', body: { expected: false, enabled: true } }])
})

it('hides disabled navigation while keeping the core project record reachable', async () => {
 const flags = enabledFeatures.map(f => ['evals', 'memory', 'chats', 'conformance-estate'].includes(f.area) ? { ...f, enabled: false } : f)
 vi.stubGlobal('fetch', vi.fn(async (path: string) => new Response(JSON.stringify(
  path === '/api/v1/features' ? flags : path.includes('/authz/me') ? { instance: {}, project: { 'change:create': true, 'change:invoke-lane': true } } : path === '/api/v1/auth/me' ? { mode: 'open', user: 'boss' } : { slug: 'p', name: 'Project', chatAvailable: true }
 ))))
 const project = await mount(<SidebarNav />, '/projects/p')
 expect(project.el.querySelector('a[href="/projects/p/memory"]')).toBeNull()
 expect([...project.el.querySelectorAll('button')].some(b => b.textContent === 'Chat')).toBe(false)
 expect(project.el.querySelector('a[href="/projects/p/memory/decisions"]')?.textContent).toBe('Decisions')
 const home = await mount(<SidebarNav />)
 expect(home.el.querySelector('a[href="/evals"]')).toBeNull()
 expect(home.el.querySelector('a[href="/conformance"]')).toBeNull()
 expect(home.el.querySelector('a[href="/conformance/reviews"]')).not.toBeNull()
})
