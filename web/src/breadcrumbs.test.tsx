// @vitest-environment jsdom
import { expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, matchPath } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from './data'
import { ParentLink } from './PageBar'
import { PAGE_ROUTES, TAB_ROUTES, routeAncestors } from './breadcrumbs'
import app from './App.tsx?raw'
import { useSetProjectScope } from './projectScope'

vi.mock('./data', async original => ({ ...await original<typeof import('./data')>(), useGet: (path: string | null) => ({ data: path === '/projects/club' ? { name: 'Club Newsletter' } : null }) }))
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('walks every route with one trail of ancestors ending at its parent page', () => {
  const routes = [...app.matchAll(/<Route path="([^"]+)"/g)].map(m => m[1]).filter(p => !p.includes('*') && !p.startsWith('/janitor'))
  expect([...PAGE_ROUTES]).toEqual(routes)
  for (const route of routes) {
    const path = route.replace(/:([a-zA-Z]+)/g, 'example-$1')
    const expected = routes.filter(r => r !== route).map(r => {
      const n = r.split('/').filter(Boolean).length
      return { r, prefix: '/' + path.split('/').filter(Boolean).slice(0, n).join('/') }
    }).filter(({r, prefix}) => !/^\/projects\/[^/]+\/releases$/.test(prefix) && (prefix === '/' || path.startsWith(prefix + '/')) && matchPath({path:r,end:true}, prefix))
      .map(({prefix}) => prefix)
    if (/^\/(targets|releases|backups)(\/|$)/.test(path)) expected.push('/deployments')
    // A tab of a page has the page's own parent, not the page's first tab.
    if ((TAB_ROUTES as readonly string[]).includes(route)) expected.splice(expected.indexOf(path.split('/').slice(0, -1).join('/')), 1)
    const trail = routeAncestors(path)
    expect(trail.map(c => c.to), route).toEqual([...new Set(expected)].sort((a,b) => a.split('/').filter(Boolean).length-b.split('/').filter(Boolean).length || (a === '/deployments' ? -1 : b === '/deployments' ? 1 : 0)))
  }
})

it('offers the parent page first in the folded menu, and nothing where the parent is home and no project is in scope', () => {
  const routes = [...app.matchAll(/<Route path="([^"]+)"/g)].map(m => m[1]).filter(p => !p.includes('*') && !p.startsWith('/janitor'))
  for (const route of routes) {
    const path = route.replace(/:([a-zA-Z]+)/g, 'example-$1')
    const parent = routeAncestors(path).filter(c => c.to !== '/').at(-1)
    const client = newQueryClient(), doc = document.createElement('div'), root = createRoot(doc)
    try {
      act(() => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><ParentLink /></MemoryRouter></QueryClientProvider>))
      const links = [...doc.querySelectorAll('a')]
      expect(links.map(a => a.getAttribute('href')), route).toEqual(parent ? [parent.to] : [])
      if (parent) expect(links[0].textContent, route).toBe('‹' + parent.label)
    } finally { act(() => root.unmount()); client.clear() }
  }
})

// f503: an operation's address names no parent, but the page is inside its
// project, so a phone leads back there rather than to the logo.
it('leads a scoped page whose parent is home back to its project', () => {
  for (const path of ['/operations/x', '/changes/x']) {
    const client = newQueryClient(), doc = document.createElement('div'), root = createRoot(doc)
    try {
      function Scope() { useSetProjectScope('club'); return null }
      act(() => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><Scope /><ParentLink /></MemoryRouter></QueryClientProvider>))
      const links = [...doc.querySelectorAll('a')]
      expect(links.map(a => [a.getAttribute('href'), a.textContent]), path).toEqual([path.startsWith('/changes') ? ['/projects/club/changes', '‹Changes'] : ['/projects/club', '‹Club Newsletter']])
    } finally { act(() => root.unmount()); client.clear() }
  }
})

it('uses supplied dynamic names without letting a page rewrite the ancestors', () => {
  expect(routeAncestors('/integrations/profiles/standard/projects', {'/integrations/profiles/standard':'Careful work', '/integrations':'Other area'}).map(c => c.label)).toEqual(['AppTown','Integrations','Model profiles','Careful work'])
})

it('skips release-history groupings that have no collection page', () => {
  expect(routeAncestors('/projects/club/releases/12/log').map(c=>c.to)).toEqual(['/', '/projects', '/projects/club', '/projects/club/releases/12'])
})

it('retains the work-session identity in its parent link', () => {
  const client = newQueryClient(), doc = document.createElement('div'), root = createRoot(doc)
  try {
    act(() => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/changes/c/work/s/records/a?run=r&attempt=s-1&theme=dark']}><ParentLink /></MemoryRouter></QueryClientProvider>))
    expect(doc.querySelector('a[href^="/changes/c/work/s"]')?.getAttribute('href')).toBe('/changes/c/work/s?run=r&attempt=s-1')
  } finally { act(() => root.unmount()); client.clear() }
})

it('gives every tab of one page the same parent', () => {
  expect(routeAncestors('/complaints/answered')).toEqual(routeAncestors('/complaints'))
  expect(routeAncestors('/complaints/archive')).toEqual(routeAncestors('/complaints'))
  for (const tab of ['resources', 'capacity', 'history']) {
    expect(routeAncestors(`/targets/a/${tab}`).map(c => c.to), tab).toEqual(routeAncestors('/targets/a').map(c => c.to))
    expect(routeAncestors(`/targets/a/${tab}`).at(-1)?.to, tab).toBe('/targets')
  }
  // A record under a tab keeps the tab as its parent.
  expect(routeAncestors('/targets/a/resources/r1').at(-1)?.to).toBe('/targets/a/resources')
})

it('links Standards review records back through Maintenance', () => {
  expect(routeAncestors('/maintenance/standards/reviews/r1').slice(1)).toEqual([
    {to:'/maintenance',label:'Maintenance'}, {to:'/maintenance/standards',label:'Standards'}, {to:'/maintenance/standards/reviews',label:'Reviews'},
  ])
})
