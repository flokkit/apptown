// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from './data'
import { FleetDeploymentStatus, deploymentStatus } from './deploymentStatus'
import { ApplicationOverview } from './pages/project/ApplicationOverview'
import { DeploymentRow, EnvCard } from './pages/project/settings/Deployments'
import type { DeploymentView, ProjectDetail } from './wire.gen'

import { ProjectListRow } from './ProjectListRow'
import type { ProjectRow } from './wire.gen'

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
    <div id="page"><EnvCard slug="lunch" env={env} onChanged={() => {}} /></div>
    <div id="fleet"><FleetDeploymentStatus slug="lunch" id={env.id} /></div>
  </MemoryRouter></QueryClientProvider>)
  for (const selector of ['#projects .project-state', '#overview .row-status', '#list .list-state', '#page .deploy-head', '#fleet']) {
    const state = el.querySelector(selector)
    expect(state?.querySelector('.chip')?.textContent?.replace(/^●\s*/, '').trim(), selector).toBe(word)
    expect(state?.textContent, selector).toContain(reason)
    expect(state?.textContent, selector).toContain('checked ')
    expect(state?.querySelector('.status-help-stack > .small.muted'), selector).not.toBeNull()
  }
  expect(deploymentStatus(env).word).toBe(word)
  client.clear()
})
