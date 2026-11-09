// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../data'
import { TargetResourcesPage } from './TargetResources'
import { TargetTabs } from './TargetTabs'

it('filters the resource table by deployment and opens full references on a separate page', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/infrastructure'), { targets: [{ target: 'host', live: true, resources: [
    { name: 'api', id: 'sha256:abc', kind: 'container', record: 'recorded', statusLabel: 'running', hosts: [{ project: 'shop', deployment: 'main', generation: 3 }] },
    { name: 'other', id: '456', kind: 'volume', record: 'unrecorded' },
  ] }] })
  const render = (path: string, detail = false) => renderToStaticMarkup(<MemoryRouter initialEntries={[path]}><QueryClientProvider client={client}><Routes><Route path={detail ? '/targets/:name/resources/:id' : '/targets/:name/resources'} element={<TargetResourcesPage detail={detail} />} /></Routes></QueryClientProvider></MemoryRouter>)
  const table = render('/targets/host/resources?deployment=shop%2Fmain')
  expect(table).toMatch(/<th[^>]*>Owned<\/th>/)
  expect(table).toContain('/targets/host/resources/container%3Asha256%3Aabc')
  expect(table).not.toContain('>other<')
  expect(table).not.toContain('View resource')
  const page = document.createElement('div'); page.innerHTML = table
  expect(page.querySelector('h1')?.nextElementSibling?.tagName).not.toBe('P')
  expect(render('/targets/host/resources')).toContain('>present</span>')
  expect(render('/targets/host/resources/volume%3A456', true)).toContain('>present</span>')
  const detail = render('/targets/host/resources/container%3Asha256%3Aabc', true)
  expect(detail).not.toContain('Resources on host')
  expect(detail).toContain('sha256:abc')
  expect(detail).toContain('shop/main: 3')
  // The title names what the resource is and what it belongs to; its
  // generated name is the full reference on a technical line.
  const detailPage = document.createElement('div'); detailPage.innerHTML = detail
  expect(detailPage.querySelector('h1')?.textContent).toBe('Container for shop/main')
  expect([...detailPage.querySelectorAll('.tech-line')].map(e => e.textContent)).toEqual(['api', 'sha256:abc'])
  expect(detail).toContain('AppTown records ownership.')
})

it('shows unavailable inventory as unknown with a muted reason', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/infrastructure'), { targets: [{ target: 'host', live: false, unknown: true, error: 'SSH did not answer', resources: [] }] })
  const html = renderToStaticMarkup(<MemoryRouter initialEntries={['/targets/host/resources']}><QueryClientProvider client={client}><Routes><Route path="/targets/:name/resources" element={<TargetResourcesPage />} /></Routes></QueryClientProvider></MemoryRouter>)
  const el = document.createElement('div'); el.innerHTML = html
  expect(el.textContent).toContain('Inventory is unknown: SSH did not answer')
  expect(el.querySelector('.error')).toBeNull()
})


it('keeps resource list states compact with their explanation on the resource page', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/infrastructure'), { targets: [{ target: 'host', live: false, invalidated: true, resources: [
    { name: 'api', id: 'abc', kind: 'container', record: 'recorded', statusLabel: 'running' },
  ] }] })
  const render = (detail: boolean) => renderToStaticMarkup(<MemoryRouter initialEntries={[detail ? '/targets/host/resources/container%3Aabc' : '/targets/host/resources']}><QueryClientProvider client={client}><Routes><Route path={detail ? '/targets/:name/resources/:id' : '/targets/:name/resources'} element={<TargetResourcesPage detail={detail} />} /></Routes></QueryClientProvider></MemoryRouter>)
  const table = document.createElement('div'); table.innerHTML = render(false)
  expect(table.querySelector('.phone-state')?.textContent).toBe('● unknown')
  expect(render(true)).toContain('Inventory predates the latest change.')
})

it('marks the current page in the target tab row, which the resource list carries too', () => {
  for (const current of ['overview', 'resources', 'capacity', 'history'] as const) {
    const el = document.createElement('div')
    el.innerHTML = renderToStaticMarkup(<MemoryRouter><TargetTabs name="host" current={current} /></MemoryRouter>)
    const links = [...el.querySelectorAll('nav[aria-label="Target pages"] a.tab')]
    expect(links.map(a => a.getAttribute('href'))).toEqual(['/targets/host', '/targets/host/resources', '/targets/host/capacity', '/targets/host/history'])
    expect(links.filter(a => a.getAttribute('aria-current') === 'page').map(a => a.textContent)).toEqual([current[0].toUpperCase() + current.slice(1)])
  }
  const client = newQueryClient()
  client.setQueryData(getKey('/infrastructure'), { targets: [{ target: 'host', live: true, resources: [] }] })
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter initialEntries={['/targets/host/resources']}><QueryClientProvider client={client}><Routes><Route path="/targets/:name/resources" element={<TargetResourcesPage />} /></Routes></QueryClientProvider></MemoryRouter>)
  expect(el.querySelector('nav[aria-label="Target pages"] a[aria-current="page"]')?.getAttribute('href')).toBe('/targets/host/resources')
})
