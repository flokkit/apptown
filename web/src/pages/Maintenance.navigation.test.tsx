// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import App from '../App'
import { newQueryClient } from '../data'
import { settled } from '../settled'
import { enabledFeatures } from '../features.fixture'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function Address() { const l = useLocation(); return <output data-address>{l.pathname + l.search + l.hash}</output> }

async function mount(path: string, projects = true, conformance = true, off: string[] = []) {
  const reads: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    reads.push(url)
    const data = url.endsWith('/features') ? enabledFeatures.map(f => ({ ...f, enabled: !off.includes(f.area) }))
      : url.endsWith('/authz/me') ? { instance: { 'project:read': projects, 'conformance:read': conformance } }
      : url.endsWith('/auth/me') ? { mode: 'open', user: 'sam' }
      : url.endsWith('/janitor') ? { projects: [], deployments: [], unknown: [], choices: null }
      : url.includes('/janitor/history') ? { history: { changes: [], more: false } }
      : url.endsWith('/conformance') ? { projects: [], reviews: [], byProject: {}, checkDefs: {}, summary: { findings: 0, flagged: 0, incompleteProjects: 0 }, lens: { kind: 'current' } }
      : url.endsWith('/conformance/reviews') ? { reviews: [] } : {}
    return new Response(JSON.stringify(data))
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><App /><Address /></MemoryRouter></QueryClientProvider>))
  await settled(client)
  return { el, reads, client, close: () => { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() } }
}

it.each(['/maintenance', '/maintenance/updates', '/maintenance/settings', '/maintenance/standards'])('opens %s through the four Maintenance tabs', async path => {
  const m = await mount(path)
  try {
    expect([...m.el.querySelectorAll('[aria-label="Maintenance"] a')].map(a => [a.textContent, a.getAttribute('href')])).toEqual([
      ['Needs you', '/maintenance'], ['Standards', '/maintenance/standards'], ['Updates', '/maintenance/updates'], ['Settings', '/maintenance/settings'],
    ])
    expect(m.el.querySelectorAll('h1')).toHaveLength(1)
    expect(m.el.querySelector('h1')?.textContent).toBe('Maintenance')
    expect(m.el.textContent).toContain(path.endsWith('/settings') ? 'What to keep updated' : path.endsWith('/updates') ? 'Last 7 days' : path.endsWith('/standards') ? 'Check now' : 'Nothing needs you.')
    if (path.endsWith('/standards')) expect([...m.el.querySelectorAll('[aria-label="Standards"] a')].map(a => a.textContent)).toEqual(['Projects', 'Checks', 'Reviews', 'Remediations', 'Pack upgrades'])
  } finally { m.close() }
})

it.each([[false, []], [true, ['conformance']]] as const)('hides Standards without its read or feature', async (read, off) => {
  const m = await mount('/maintenance', true, read, [...off])
  try { expect(m.el.querySelector('[aria-label="Maintenance"] a[href="/maintenance/standards"]')).toBeNull() } finally { m.close() }
})

it.each([[[]], [['conformance-estate', 'what-ifs']]])('opens Maintenance on Standards for a Conformance-only reader (%s)', async off => {
  const m = await mount('/features', false, true, off)
  try {
    await act(async () => m.el.querySelector<HTMLAnchorElement>('a[href="/maintenance"]')!.click())
    await settled(m.client)
    expect(m.el.querySelector('[aria-label="Maintenance"] .active')?.textContent).toBe('Standards')
    expect(m.el.textContent).not.toContain('Not available to you')
    expect(m.reads.some(u => u.includes('/janitor'))).toBe(false)
    expect([...m.el.querySelectorAll('[aria-label="Standards"] a')].map(a => a.textContent)).toEqual(off.length ? ['Reviews', 'Remediations'] : ['Projects', 'Checks', 'Reviews', 'Remediations', 'Pack upgrades'])
  } finally { m.close() }
})

it.each([['/janitor', 'Needs you'], ['/janitor/settings', 'Settings']])('redirects %s to its Maintenance tab', async (path, tab) => {
  const m = await mount(path)
  try { expect(m.el.querySelector('[aria-label="Maintenance"] .active')?.textContent).toBe(tab) } finally { m.close() }
})

// Compatibility redirects preserve the entire bookmarked browser address.
it.each(['/janitor', '/janitor/settings'])('keeps search and hash when redirecting %s', async path => {
  const m = await mount(path + '?x=1#saved-section')
  try {
    expect(m.el.querySelector('[data-address]')?.textContent).toBe(path.replace('/janitor', '/maintenance') + '?x=1#saved-section')
  } finally { m.close() }
})
