// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../../data'
import { settled } from '../../settled'
import { StaleScope } from '../../ReadGate'
import JanitorStatisticsPage, { fmtSpan } from './Statistics'
import type { JanitorCounts, JanitorStatistics } from '../../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const zero: JanitorCounts = { runs: 0, shipped: 0, nothingToChange: 0, waiting: 0, failed: 0, closed: 0, running: 0 }
const day = 86400
function fixture(): JanitorStatistics {
  return {
    days: 7, since: '2026-11-11T09:00:00Z',
    kinds: [
      { kind: 'packs', counts: { ...zero, runs: 10, shipped: 8, failed: 2 } },
      { kind: 'updates', counts: { ...zero, runs: 4, shipped: 1, waiting: 1, nothingToChange: 2 } },
      { kind: 'security', counts: { ...zero, runs: 3, failed: 3 } },
    ],
    reasons: [
      { kind: 'packs', column: 'failed', step: 'tests', count: 2, latest: 'tests: the container could not be cleaned up', examples: [
        { changeId: 'kept-change', project: 'Shop', slug: 'shop', at: '2026-11-17T10:00:00Z' },
        { project: 'Shop', slug: 'shop', at: '2026-11-16T10:00:00Z' },
      ] },
      { kind: 'updates', column: 'waiting', step: 'Waiting for acceptance', count: 1, latest: 'Waiting for acceptance', examples: [{ changeId: 'decide-me', project: 'Garden', slug: 'garden', at: '2026-11-18T06:00:00Z' }] },
    ],
    patch: { patched: 3, median: 2 * day, slowest: 5 * day, open: 1, oldestOpen: 9 * day },
    projects: [
      { projectId: 'p1', slug: 'shop', name: 'Shop', counts: { ...zero, runs: 12, shipped: 8, failed: 4 }, patch: { patched: 3, median: 2 * day, slowest: 5 * day, open: 0, oldestOpen: 0 } },
      { projectId: 'p2', slug: 'garden', name: 'Garden', counts: { ...zero, runs: 5, shipped: 1, waiting: 1, nothingToChange: 2, failed: 1 }, patch: { patched: 0, median: 0, slowest: 0, open: 1, oldestOpen: 9 * day } },
    ],
  }
}
const cleanup: (() => void)[] = []
afterEach(() => { cleanup.splice(0).forEach(f => f()); vi.unstubAllGlobals() })
async function mount(fetcher: (url: string) => Promise<Response>, path = '/maintenance/statistics') {
  vi.stubGlobal('fetch', vi.fn(fetcher))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  client.setDefaultOptions({ queries: { retry: false } })
  cleanup.push(() => { act(() => root.unmount()); client.clear() })
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><StaleScope><JanitorStatisticsPage /></StaleScope></MemoryRouter></QueryClientProvider>))
  await settled(client)
  return el
}
const table = (el: HTMLElement, label: string) => el.querySelector(`table[aria-label="${label}"]`)!
const rows = (el: HTMLElement, label: string) => [...table(el, label).querySelectorAll('tbody tr')].map(r => [...r.querySelectorAll('td')].map(c => c.textContent))

it('shows counts by kind and project, reasons with their examples, and the patch clock', async () => {
  const urls: string[] = []
  const el = await mount(async url => { urls.push(url); return new Response(JSON.stringify(fixture())) })
  expect(urls).toEqual(['/api/v1/janitor/statistics?days=7'])
  expect(rows(el, 'Runs by kind')).toEqual([
    ['Packs', '10', '8', '0', '0', '2', '0', '0'],
    ['Dependency and image updates', '4', '1', '2', '1', '0', '0', '0'],
    ['Security fixes', '3', '0', '0', '0', '3', '0', '0'],
  ])
  const examples = [...table(el, 'Why not shipped').querySelectorAll('a')].map(a => a.getAttribute('href'))
  expect(examples).toEqual(['/changes/kept-change', '/changes/decide-me'])
  expect(table(el, 'Why not shipped').textContent).toContain('Shop, ')
  expect(rows(el, 'Why not shipped')[0].slice(2, 5)).toEqual(['tests', '2', 'the container could not be cleaned up'])
  expect(rows(el, 'Why not shipped')[1][4]).toBe('')
  expect(rows(el, 'Time to patch')).toEqual([['All projects', '3', '2 days', '5 days', '1', '9 days']])
  expect(el.textContent).toContain('Still open (seen in period)')
  const projects = table(el, 'Projects')
  expect([...projects.querySelectorAll('tbody a')].map(a => a.getAttribute('href'))).toEqual(['/projects/shop/settings/general#janitor', '/projects/garden/settings/general#janitor'])
  expect(rows(el, 'Projects')[1].slice(-4)).toEqual(['0', '—', '—', '1'])
})

it('reads the 30-day period from the address', async () => {
  const urls: string[] = []
  const el = await mount(async url => { urls.push(url); return new Response(JSON.stringify({ ...fixture(), days: 30, reasons: [] })) }, '/maintenance/statistics?days=30')
  expect(urls).toEqual(['/api/v1/janitor/statistics?days=30'])
  expect(el.querySelector('[aria-current="page"]')?.textContent).toBe('30 days')
  expect(el.textContent).toContain('None in the last 30 days.')
})

it('says the read failed rather than showing zeros', async () => {
  const el = await mount(async () => new Response('{"error":"the period is 7 or 30 days"}', { status: 400, headers: { 'Content-Type': 'application/json' } }))
  expect(el.querySelector('table')).toBeNull()
  expect(el.textContent).toContain('unknown')
})

it('writes patch times in the unit that fits', () => {
  expect([fmtSpan(30), fmtSpan(3 * 3600), fmtSpan(3 * day)]).toEqual(['1 min', '3 h', '3 days'])
})
