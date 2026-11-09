// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from './data'
import { deploymentStatus } from './deploymentStatus'
import { ApplicationOverview } from './pages/project/ApplicationOverview'
import { DeploymentReleaseCell } from './pages/project/settings/Deployments'
import { DeploymentTable } from './pages/DeploymentTable'
import { DeploymentTitle } from './pages/project/DeploymentDeploys'
import type { DeploymentView, ProjectDetail } from './wire.gen'

import { ProjectListRow } from './ProjectListRow'
import type { ProjectRow } from './wire.gen'

// One deployment as the project's Deployments page lists it: a table row
// with its release facts.
function DeploymentRow({ slug, env }: { slug: string; env: DeploymentView; current?: boolean }) {
  return <DeploymentTable release={() => <DeploymentReleaseCell slug={slug} env={env} />} rows={[{ ...env, slug, app: slug, deployment: env.name, label: env.displayLabel || env.name, static: env.static, url: env.url, hostOnly: env.hostOnly }]} />
}


// The server answer is authoritative even when the raw public-part reading
// says running and diagnostics have not been fetched by this page.
it.each(['working', 'unknown', 'failed', 'stopped', 'deploying'] as const)('renders the server answer %s across project and deployment views', word => {
  const client = newQueryClient()
  const reason = 'storage (s3) did not answer in time'
  const status = { word, reason, checkedAt: '2026-11-01T00:00:00Z', stale: word === 'unknown' }
  const env = { id: 'deployment', name: 'main', observation: 'running', generation: 1, kind: 'standing', feed: 'main', observationStale: false, observedAt: '2026-11-01T00:00:00Z', status } as DeploymentView
  const values = {
    '/projects/lunch/deployments': [env],
    '/projects/lunch/overview': { active: [], open: [], history: [] },
    '/projects/lunch/releases': [],
    '/projects/lunch/deployments/main/resources': { parts: [], limits: [], limitsKnown: true },
    '/projects/lunch/deployments/main/backups': { backups: [], plan: [] },
  }
  for (const [path, value] of Object.entries(values)) client.setQueryData(getKey(path), value)
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter>
    <div id="projects"><ProjectListRow project={{ ...env, slug: 'lunch', name: 'Lunch' } as unknown as ProjectRow} /></div>
    <div id="overview"><ApplicationOverview project={{ slug: 'lunch', name: 'Lunch' } as ProjectDetail} /></div>
    <div id="list"><DeploymentRow slug="lunch" env={env} /></div>
    <div id="page"><DeploymentTitle name="main" env={env} /></div>
  </MemoryRouter></QueryClientProvider>)
  for (const selector of ['#projects .project-state', '#overview .phone-state', '#list .phone-state', '#page h1']) {
    const state = el.querySelector(selector)
    expect(state?.querySelector('.chip')?.textContent?.replace(/^●\s*/, '').trim(), selector).toBe(word)
    // Every view shows the word; why and when checked are its details, on
    // hover or tap.
    const details = state?.querySelector('[data-help]')?.getAttribute('data-help') ?? state?.querySelector('.chip[title]')?.getAttribute('title')
    expect(details, selector).toContain(reason)
    expect(details, selector).toContain('Checked ')
    expect(state?.textContent, selector).not.toContain(reason)
  }
  expect(deploymentStatus(env).word).toBe(word)
  client.clear()
})

// An activity read that fails says so in the Activity section, rather than
// leaving it blank as if nothing had happened.
it('says when the project activity could not be read', async () => {
  const { createRoot } = await import('react-dom/client'), { act } = await import('react'), { vi } = await import('vitest')
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  vi.stubGlobal('fetch', vi.fn(async (url: string) => url.endsWith('/overview')
    ? new Response('{"error":"broken"}', { status: 404 })
    : new Response(JSON.stringify(url.endsWith('/deployments') || url.endsWith('/releases') ? [] : url.endsWith('/schedules') ? { schedules: [] } : {}))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><ApplicationOverview project={{ slug: 'lunch', name: 'Lunch' } as ProjectDetail} /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelector('[data-help*="Activity could not be read"], [title*="Activity could not be read"]')).not.toBeNull())
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
