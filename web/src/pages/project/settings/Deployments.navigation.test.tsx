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
import type { DeploymentView } from '../../../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const base = '/projects/lunch/deployments'
const at = '2026-11-03T08:00:00Z'
const env = { name: 'main', displayLabel: 'Lunch Orders', generation: 1, kind: 'standing', feed: 'main', audience: 'team', data: 'production', observation: 'running', observedAt: at, readiness: 'reconciled', url: 'https://lunch.example', target: { kind: 'docker-host', name: 'local' } } as DeploymentView
const reading = { waiting: false, stale: false, unknown: false, observedAt: at }
function fixtures({ backup = 'none', lowDisk = false, readiness = 'reconciled' } = {}): Record<string, unknown> {
  return {
    '/projects/lunch': { slug: 'lunch', name: 'Lunch' },
    [base]: [{ ...env, readiness }, { ...env, name: 'preview-1', displayLabel: 'Fix lunch', kind: 'preview', changeId: 'change-1', generation: 0, target: undefined }],
    '/projects/lunch/targets': { targets: [{ name: 'local', kind: 'docker-host' }] },
    '/changes/change-1': { change: { number: 12, title: 'Fix lunch' } },
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
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter><DeploymentsTab slug="lunch" /></MemoryRouter></QueryClientProvider>)
  client.clear()
  return el
}

// The list keeps the operational summary and links, with full controls on the page.
it('summarises each deployment, including previews, and opens its page', () => {
  const el = list()
  const rows = el.querySelectorAll('.list-row')
  expect(rows).toHaveLength(2)
  expect(rows[0].textContent).toContain('Lunch Orders')
  expect(rows[0].classList.contains('deployment-row')).toBe(true)
  expect(rows[0].querySelector('.list-identity')?.textContent).not.toContain('Release #8')
  expect(rows[0].querySelector('.list-detail')?.textContent).toContain('Release #8')
  expect(rows[0].textContent).toContain('working')
  expect(rows[0].textContent).toContain('no backup yet')
  expect(rows[0].textContent).toContain(`Release #8 · ${fmtStamp(at)}`)
  expect(rows[0].textContent).not.toContain('Release #9')
  expect(rows[0].querySelector('a[href="https://lunch.example"]')).not.toBeNull()
  expect(rows[0].querySelector('.list-action a')?.getAttribute('href')).toBe(`${base}/main`)
  expect(rows[1].querySelector('a[href="/changes/change-1"]')?.textContent).toBe('Preview of change #12: Fix lunch')
  expect(el.textContent).toContain('New deployment')
  expect(el.querySelector('.deploy-entry')).toBeNull()
  expect(el.textContent).not.toContain('Edit display name')
})

it('omits healthy backups and keeps disk and readiness warnings short', () => {
  const healthy = list({ backup: 'fresh' })
  expect(healthy.querySelector('.list-state')?.textContent).toContain('working')
  expect(healthy.querySelector('.list-detail .error, .list-detail .deploy-warning')).toBeNull()
  expect(list({ lowDisk: true }).querySelector('.deploy-warning')?.textContent).toBe(' low disk')
  expect(list({ readiness: 'uncertain' }).querySelector('.deploy-warning')?.textContent).toBe(' uncertain')
})

it('routes to the full card with footer controls and the TopBar breadcrumb', async () => {
  const data = fixtures()
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(data[String(url).replace(/^\/api\/v1/, '')] ?? (String(url).endsWith('/deploys') ? [] : {})))))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`${base}/main`]}><TopBar me={null} /><Routes><Route path="/projects/:slug/deployments/:name" element={<DeploymentPage />} /></Routes></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelector('.deploy-entry')).not.toBeNull())
    await vi.waitFor(() => expect(el.querySelector('.topbar')?.textContent).toContain('AppTown›Projects›Lunch›Deployments›Lunch Orders'))
    expect(el.querySelector('.topbar a[href="/projects/lunch/deployments"]')).not.toBeNull()
    expect(el.querySelector('.topbar a[href="/projects"]')?.textContent).toBe('Projects')
    expect(el.querySelector('.deployment-page-heading')?.textContent).toContain('Lunch Orders')
    expect(el.querySelectorAll('h1')).toHaveLength(1)
    expect(el.querySelector('.deployment-address a')?.getAttribute('href')).toBe('https://lunch.example')
    expect(el.querySelector('.deploy-head .deploy-trailer')).toBeNull()
    expect(el.querySelector('.deploy-more')?.textContent).toContain('Edit display name')
    expect(el.querySelector('.deploy-more a')?.getAttribute('href')).toBe('/projects/lunch/releases')
    expect(el.textContent).toContain('Resources')
    // The application's route must take precedence over the generic project tab.
    expect(appSource).toContain('path="/projects/:slug/deployments/:name" element={<DeploymentPage />}')
  } finally {
    act(() => root.unmount()); client.clear(); vi.unstubAllGlobals()
  }
})

// All operational acts stay with the full card after the list becomes a summary.
it.each(['reconciled', 'uncertain'])('keeps %s deployment actions in the card footer', readiness => {
  const client = newQueryClient()
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter><EnvCard slug="lunch" env={{ ...env, name: 'staging', generation: 0, feed: 'promotion', readiness, target: { ...env.target!, kind: 's3-bucket' } }} onChanged={() => {}} /></MemoryRouter></QueryClientProvider>)
  const footer = el.querySelector('.deploy-more')!
  expect(footer.textContent).toContain(readiness === 'uncertain' ? 'Check again' : 'Deploy latest')
  expect(footer.textContent).toContain('Tear down')
  expect(footer.textContent).toContain('Edit display name')
  expect(footer.querySelector('a')?.getAttribute('href')).toBe('/projects/lunch/releases')
  client.clear()
})
