import { withFeatures } from '../../../features.fixture'
// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import appSource from '../../../App.tsx?raw'
import { getKey, newQueryClient } from '../../../data'
import { fmtStamp } from '../../../format'
import TopBar from '../../../TopBar'
import { DeploymentPage, DeploymentsTab, EnvCard } from './Deployments'

// The person may deploy and operate the project's deployments.
const HOLDS = { instance: {}, project: { 'deployment:deploy': true, 'deployment:operate': true } }
import type { DeploymentView } from '../../../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const base = '/projects/lunch/deployments'
const at = '2026-11-03T08:00:00Z'
const env = { name: 'main', displayLabel: 'Lunch Orders', generation: 1, kind: 'standing', feed: 'main', audience: 'team', data: 'production', status: { word: 'working', reason: '', stale: false, servingRelease: 'Running release 8', servingReleaseNumber: 8 }, observation: 'running', servingRelease: { number: 8, releasedAt: at }, observedAt: at, readiness: 'reconciled', url: 'https://lunch.example', target: { kind: 'docker-host', name: 'local' } } as DeploymentView
const reading = { waiting: false, stale: false, unknown: false, observedAt: at }
function fixtures({ backup = 'none', lowDisk = false, readiness = 'reconciled' } = {}): Record<string, unknown> {
  return {
    '/projects/lunch': { slug: 'lunch', name: 'Lunch' },
    [base]: [{ ...env, readiness }, { ...env, name: 'preview-1', displayLabel: 'Fix lunch', kind: 'preview', changeId: 'change-1', generation: 0, target: undefined }],
    '/projects/lunch/targets': { targets: [{ name: 'local', kind: 'docker-host', kindInfo: { title: 'Docker host' }, address: {}, capabilities: {} }] },
    '/changes/change-1': { change: { number: 12, title: 'Fix lunch' } },
    '/authz/roles': { roles: [{ name: 'release-manager', title: 'Release manager', scope: 'project' }], capabilities: {} },
    '/projects/lunch/secrets': { deployments: [], choosable: [] },
    '/projects/lunch/retained': { retained: [] },
    '/projects/lunch/releases': [{ deployment: 'main', number: 9, changes: [], serving: false, state: 'failed', createdAt: at }, { deployment: 'main', number: 8, changes: [], serving: true, releasedAt: at }],
    [`${base}/main/resources`]: { reading, parts: [], limits: [], limitsKnown: true, share: { elastic: true, lowDisk } },
    [`${base}/main/diagnostics`]: { reading, parts: [], events: [], eventsKnown: true },
    [`${base}/main/protection`]: { reading, items: [{ need: 'postgres', copy: backup, copyLabel: backup === 'none' ? 'missing' : 'fresh' }], recent: [], restoredCopies: [], coverage: { assessed: [], notAssessed: [] } },
  }
}
function list(options?: Parameters<typeof fixtures>[0]) {
  const client = newQueryClient()
  for (const [path, value] of Object.entries(fixtures(options))) client.setQueryData(getKey(path), value)
  client.setQueryData(getKey('/authz/me?project=lunch'), HOLDS)
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter><DeploymentsTab slug="lunch" /></MemoryRouter></QueryClientProvider>)
  client.clear()
  return el
}

// The list keeps the operational summary and links, with full controls on the page.
it('summarises each deployment, including previews, and opens its page', () => {
  const el = list()
  const rows = el.querySelectorAll('tbody tr')
  expect(rows).toHaveLength(2)
  expect(rows[0].textContent).toContain('Lunch Orders')
  expect(rows[0].querySelector('.phone-name')?.textContent).not.toContain('Running release 8')
  expect(rows[0].querySelector('.deployment-release-cell')?.textContent).toContain('Running release 8')
  expect(rows[0].textContent).toContain('working')
  expect(rows[0].textContent).toContain('no backup yet')
  expect(rows[0].textContent).toContain(`Running release 8 · ${fmtStamp(at)}`)
  expect(rows[0].textContent).not.toContain('Running release 9')
  expect(rows[0].querySelector('a[href="https://lunch.example"]')).not.toBeNull()
  expect([...rows[0].querySelectorAll('button')].map(b => b.getAttribute('aria-label') ?? b.textContent)).toEqual([expect.stringContaining('More actions')])
  expect(rows[0].textContent).toContain('Automatic deploys are paused until a person saves the Release feed setting.')
  expect(rows[1].querySelector('a[href="/changes/change-1"]')?.textContent).toBe('Preview of change #12: Fix lunch')
  expect(el.textContent).toContain('New deployment')
  expect(el.querySelector('.deploy-entry')).toBeNull()
  expect(el.textContent).not.toContain('Edit display name')
})

it('omits healthy backups and keeps disk and readiness warnings short', () => {
  const healthy = list({ backup: 'fresh' })
  expect(healthy.querySelector('.phone-state')?.textContent).toContain('working')
  expect(healthy.querySelector('.deployment-release-cell .error, .deployment-release-cell .deploy-warning')).toBeNull()
  expect(list({ lowDisk: true }).querySelector('.deploy-warning')?.textContent).toBe('low disk')
  expect(list({ readiness: 'uncertain' }).querySelector('.deploy-warning')?.textContent).toBe('uncertain')
})

it('routes to the full card with footer controls and the TopBar breadcrumb', async () => {
  const data = fixtures()
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string) => new Response(JSON.stringify(String(url).includes('/authz/me') ? HOLDS : data[String(url).replace(/^\/api\/v1/, '')] ?? (String(url).endsWith('/deploys') ? [] : {}))))))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`${base}/main`]}><TopBar me={null} /><Routes><Route path="/projects/:slug/deployments/:name" element={<DeploymentPage />} /></Routes></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelector('.deploy-entry')).not.toBeNull())
    await vi.waitFor(() => expect(el.querySelector('.topbar')?.textContent).toContain('AppTown›Projects›Lunch›Deployments'))
    expect(el.querySelector('.topbar a[href="/projects/lunch/deployments"]')).not.toBeNull()
    expect(el.querySelector('.topbar a[href="/projects"]')?.textContent).toBe('Projects')
    expect(el.querySelector('.deployment-page-heading')?.textContent).toContain('Lunch Orders')
    expect(el.querySelectorAll('h1')).toHaveLength(1)
    expect(el.querySelector('.deployment-address a')?.getAttribute('href')).toBe('https://lunch.example')
    // Status, History and Settings are tabs of the page; Status shows first.
    expect([...el.querySelectorAll('.deployment-tabs a')].map(a => [a.textContent, a.getAttribute('href'), a.getAttribute('aria-current')])).toEqual([['Status', '/projects/lunch/deployments/main', 'page'], ['History', '/projects/lunch/deployments/main?tab=history', null], ['Settings', '/projects/lunch/deployments/main?tab=settings', null]])
    expect(el.textContent).not.toContain('Edit display name')
    expect(el.textContent).toContain('resources →')
    // The application's route must take precedence over the generic project tab.
    expect(appSource).toContain('path="/projects/:slug/deployments/:name" element={<CoreArea area="projects"><DeploymentPage /></CoreArea>}')
  } finally {
    act(() => root.unmount()); client.clear(); vi.unstubAllGlobals()
  }
})

// The card keeps its own acts after the list becomes a summary.
it.each(['reconciled', 'uncertain'])('keeps %s deployment actions in the card footer', readiness => {
  const client = newQueryClient()
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter><EnvCard slug="lunch" env={{ ...env, name: 'staging', generation: 0, feed: 'promotion', readiness, target: { ...env.target!, kind: 's3-bucket' } }} onChanged={() => {}} /></MemoryRouter></QueryClientProvider>)
  const footer = el.querySelector('.deploy-more')!
  expect(footer.textContent).not.toContain('Check again')
  expect(footer.textContent).toContain('Promote')
  // Take down is in the deployment's ⋯ menu, not the card.
  expect(footer.textContent).not.toContain('Tear down')
  // The name and the history are the page's Settings and History tabs.
  expect(footer.textContent).not.toContain('Edit display name')
  expect(footer.querySelector('a')).toBeNull()
  client.clear()
})

it('creates a follows-main deployment on the project Deployments page', async () => {
  const { ProjectDeployments } = await import('../ProjectDeployments')
  const client = newQueryClient()
  for (const [path, value] of Object.entries(fixtures())) client.setQueryData(getKey(path), value)
  client.setQueryData(getKey('/projects/lunch/targets'), { targets: [{ name: 'local', displayLabel: 'Local host', kind: 'docker-host', kindInfo: { title: 'Docker host' }, address: {}, capabilities: {} }] })
  client.setQueryData(getKey('/projects/lunch/final-copies'), { copies: [] })
  client.setQueryData(getKey('/projects/lunch/release-preference'), {})
  const requests: unknown[] = []
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string, init?: RequestInit) => {
    if (String(url).includes('/authz/me')) return new Response(JSON.stringify(HOLDS))
    if (init?.method === 'POST' && url.endsWith('/deployments')) { requests.push(JSON.parse(String(init.body))); return new Response('{}') }
    return new Response(JSON.stringify(fixtures()[url.replace('/api/v1', '')] || {}))
  })))
  const el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><ProjectDeployments slug="lunch" /></MemoryRouter></QueryClientProvider>))
    await act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === 'New deployment')!.click())
    const input = el.querySelector<HTMLInputElement>('[aria-label="Deployment name"]')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'office')
      input.dispatchEvent(new Event('input', { bubbles: true }))
      const feed = el.querySelector<HTMLSelectElement>('[aria-label="Release feed"]')!
      feed.value = 'main'; feed.dispatchEvent(new Event('change', { bubbles: true }))
    })
    await act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Create')!.click())
    expect(requests).toEqual([expect.objectContaining({ name: 'office', feed: 'main', target: 'local' })])
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

it.each(['uncertain', 'reconciled'])('refreshes status on the deployment page in state %s', readiness => {
  const client = newQueryClient()
  for (const [path, value] of Object.entries(fixtures({ readiness }))) client.setQueryData(getKey(path), value)
  const html = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`${base}/main`]}><Routes><Route path="/projects/:slug/deployments/:name" element={<DeploymentPage />} /></Routes></MemoryRouter></QueryClientProvider>)
  expect(html).toContain('aria-label="More actions for')
  expect(html).not.toContain('>Check again<')
  client.clear()
})

// Based on the settled-refresh reproduction: click the real control and wait
// for the refreshed deployment query, rather than only checking its markup.
it.each(['reconciled', 'uncertain'])('reloads the resulting state after refreshing a %s deployment', async readiness => {
  let refreshed = false
  let readsAfterRefresh = 0
  const data = fixtures({ readiness })
  const posts: string[] = []
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string, init?: RequestInit) => {
    if (String(url).includes('/authz/me')) return new Response(JSON.stringify(HOLDS))
    if (init?.method === 'POST' && url.endsWith('/check')) {
      posts.push(url)
      refreshed = true
      return new Response(JSON.stringify({ deployment: 'main', checked: [], status: { word: 'stopped', reason: '', stale: false }, observation: 'stopped' }))
    }
    const path = url.replace(/^\/api\/v1/, '')
    if (path === base && refreshed) {
      readsAfterRefresh++
      return new Response(JSON.stringify([{ ...env, readiness: 'reconciled', status: { word: 'stopped', reason: '', stale: false }, observation: 'stopped', observedAt: '2026-11-07T10:00:00Z' }]))
    }
    return new Response(JSON.stringify(data[path] ?? (url.endsWith('/deploys') ? [] : {})))
  })))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`${base}/main`]}><Routes><Route path="/projects/:slug/deployments/:name" element={<DeploymentPage />} /></Routes></MemoryRouter></QueryClientProvider>))
    // Refresh status is in the deployment's ⋯ menu, which opens in a popover.
    const refresh = () => [...document.body.querySelectorAll('.more-menu-list button')].find(b => b.firstChild?.textContent === 'Refresh status') as HTMLButtonElement | undefined
    await vi.waitFor(() => expect(el.querySelector('button[aria-label^="More actions"]')).not.toBeNull())
    await act(async () => { el.querySelector<HTMLButtonElement>('button[aria-label^="More actions"]')!.click() })
    await act(async () => {
      refresh()!.click()
      await new Promise(resolve => setTimeout(resolve, 30))
    })
    await vi.waitFor(() => {
      expect(readsAfterRefresh).toBeGreaterThan(0)
      expect(el.querySelector('.deployment-page-heading')?.textContent).toContain('stopped')
      expect(el.textContent).not.toContain('nothing about this deployment is in doubt')
    })
    expect(posts).toEqual([`/api/v1${base}/main/check`])
    expect(client.getQueryData<DeploymentView[]>(getKey(base))?.[0]).toMatchObject({ readiness: 'reconciled', status: { word: 'stopped', reason: '', stale: false }, observation: 'stopped', observedAt: '2026-11-07T10:00:00Z' })
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
