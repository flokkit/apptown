// @vitest-environment jsdom
import { expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, matchPath } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from './data'
import TopBar from './TopBar'
import { PAGE_ROUTES, routeAncestors } from './breadcrumbs'
import app from './App.tsx?raw'

vi.mock('./data', async original => ({ ...await original<typeof import('./data')>(), useGet: () => ({ data: null }) }))
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('walks every route with one linked trail ending at its parent page', () => {
  const routes = [...app.matchAll(/<Route path="([^"]+)"/g)].map(m => m[1]).filter(p => !p.includes('*'))
  expect([...PAGE_ROUTES]).toEqual(routes)
  for (const route of routes) {
    const path = route.replace(/:([a-zA-Z]+)/g, 'example-$1')
    const expected = routes.filter(r => r !== route).map(r => {
      const n = r.split('/').filter(Boolean).length
      return { r, prefix: '/' + path.split('/').filter(Boolean).slice(0, n).join('/') }
    }).filter(({r, prefix}) => !/^\/projects\/[^/]+\/releases$/.test(prefix) && (prefix === '/' || path.startsWith(prefix + '/')) && matchPath({path:r,end:true}, prefix))
      .map(({prefix}) => prefix)
    if (/^\/(targets|releases|backups)(\/|$)/.test(path)) expected.push('/deployments')
    const trail = routeAncestors(path)
    expect(trail.map(c => c.to), route).toEqual([...new Set(expected)].sort((a,b) => a.split('/').filter(Boolean).length-b.split('/').filter(Boolean).length || (a === '/deployments' ? -1 : b === '/deployments' ? 1 : 0)))
    const client = newQueryClient()
    try {
      const doc = document.createElement('div')
      const root = createRoot(doc)
      act(() => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><TopBar me={null} /></MemoryRouter></QueryClientProvider>))
      expect([...doc.querySelectorAll('.topbar-crumb a')].map(a => a.getAttribute('href')), route).toEqual(trail.filter(c => c.to !== '/').map(c => c.to))
      expect(doc.querySelector('.topbar-here'), route).toBeNull()
      act(() => root.unmount())
    } finally { client.clear() }
  }
})

it('uses supplied dynamic names without letting a page rewrite the ancestors', () => {
  expect(routeAncestors('/integrations/profiles/standard/projects', {'/integrations/profiles/standard':'Careful work', '/integrations':'Other area'}).map(c => c.label)).toEqual(['AppTown','Integrations','Model profiles','Careful work'])
})

it('skips release-history groupings that have no collection page', () => {
  expect(routeAncestors('/projects/club/releases/12/log').map(c=>c.to)).toEqual(['/', '/projects', '/projects/club', '/projects/club/releases/12'])
})

it('keeps a breadcrumb trail on standalone account pages without exposing invite tokens', () => {
  const client = newQueryClient(), doc = document.createElement('div'), root = createRoot(doc)
  try {
    act(() => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/invite/secret-token']}><TopBar standalone me={null} /></MemoryRouter></QueryClientProvider>))
    expect(doc.querySelectorAll('nav[aria-label="Breadcrumb"]')).toHaveLength(1)
    expect(doc.querySelector('nav')?.textContent).not.toContain('secret-token')
    expect(doc.querySelector('a[href="/invite"]')).not.toBeNull()
    expect(doc.querySelector('button')).toBeNull()
  } finally { act(() => root.unmount()); client.clear() }
})


it('retains the work-session identity in its parent link', () => {
  const client = newQueryClient(), doc = document.createElement('div'), root = createRoot(doc)
  try {
    act(() => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/changes/c/work/s/records/a?run=r&attempt=s-1&theme=dark']}><TopBar me={null} /></MemoryRouter></QueryClientProvider>))
    expect(doc.querySelector('a[href^="/changes/c/work/s"]')?.getAttribute('href')).toBe('/changes/c/work/s?run=r&attempt=s-1')
  } finally { act(() => root.unmount()); client.clear() }
})
