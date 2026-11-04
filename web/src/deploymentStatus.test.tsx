// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from './data'
import { FleetDeploymentStatus } from './deploymentStatus'
import { ApplicationOverview } from './pages/project/ApplicationOverview'
import { DeploymentRow, EnvCard } from './pages/project/settings/Deployments'
import type { DeploymentView, ProjectDetail } from './wire.gen'

// Every deployment view must qualify the same runtime reading with the same
// diagnostics, including unknown parts and stale or missing readings.
it.each([
  ['running', 'ready', {}, 'working'],
  ['running', 'unknown', {}, 'unknown'],
  ['running', 'ready', { unknown: true }, 'unknown'],
  ['running', 'ready', { stale: true }, 'unknown'],
  ['running', 'missing', {}, 'unknown'],
  ['unknown', 'missing', {}, 'unknown'],
  ['stopped', 'unknown', {}, 'stopped'],
  ['unreachable', 'unknown', {}, 'unreachable'],
  ['unknown', 'missing', {}, 'deploying', 'releasing'],
  ['running', 'ready', { stale: true }, 'deploying', 'releasing'],
  ['unreachable', 'unknown', {}, 'deploying', 'releasing'],
  ['unknown', 'missing', {}, 'unknown', 'released'],
  ['running', 'ready', {}, 'working', 'released'],
  ['unknown', 'missing', {}, 'unknown', 'failed'],
] as const)('agrees across all deployment views for %s / %s / %j', (observation, presence, reading, word, releaseState = undefined) => {
  const client = newQueryClient()
  const env = { id: 'deployment', name: 'main', observation, generation: 1, kind: 'standing', feed: 'landing', observationIssue: 'Runtime status is unavailable', observationStale: word === 'deploying', observedAt: '2026-11-01T00:00:00Z' } as DeploymentView
  const values = {
    '/projects/lunch/deployments': [env],
    '/projects/lunch/overview': { active: [], open: [], history: [] },
    '/projects/lunch/releases': [
      { number: 8, state: 'releasing', statusLabel: 'deploying', deployment: 'other', createdAt: '2026-11-01T00:00:00Z' },
      ...(releaseState ? [{ number: 7, state: releaseState, statusLabel: releaseState === 'releasing' ? 'deploying' : releaseState === 'released' ? 'deployed' : 'failed', deployment: env.name, createdAt: '2026-11-01T00:00:00Z' }] : []),
    ],
    '/projects/lunch/deployments/main/resources': { parts: [], limits: [], limitsKnown: true },
    '/projects/lunch/deployments/main/backups': { backups: [], plan: [] },
    ...(presence === 'missing' ? {} : { '/projects/lunch/deployments/main/diagnostics': { reading, parts: [{ part: 'web', presence }], events: [], eventsKnown: true } }),
  }
  for (const [path, value] of Object.entries(values)) client.setQueryData(getKey(path), value)
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter>
    <div id="overview"><ApplicationOverview project={{ slug: 'lunch', name: 'Lunch' } as ProjectDetail} /></div>
    <div id="list"><DeploymentRow slug="lunch" env={env} /></div>
    <div id="page"><EnvCard slug="lunch" env={env} onChanged={() => {}} /></div>
    <div id="fleet"><FleetDeploymentStatus slug="lunch" id={env.id} /></div>
  </MemoryRouter></QueryClientProvider>)
  for (const selector of ['#overview .card.row > .row-status .chip', '#list .list-state .chip', '#page .deploy-head .chip', '#fleet .chip']) {
    const chip = el.querySelector(selector)
    expect(chip?.textContent?.replace(/^●\s*/, '').trim(), selector).toBe(word)
    if (word === 'deploying') {
      expect(chip?.classList.contains('running'), selector).toBe(true)
      expect(chip?.getAttribute('title'), selector).toBe('Release 7')
    } else if (observation === 'unknown') {
      expect(chip?.classList.contains('unknown'), selector).toBe(true)
      expect(chip?.getAttribute('title'), selector).toBe('Runtime status is unavailable')
    }
  }
  client.clear()
})

it('keeps observations without generation diagnostics and downgrades a failed diagnostics refresh', async () => {
  const { deploymentStatus } = await import('./deploymentStatus')
  const env = { observation: 'running', generation: 0 } as DeploymentView
  expect(deploymentStatus(env).word).toBe('running')
  expect(deploymentStatus({ ...env, generation: 1 }, { parts: [] } as never, true).word).toBe('unknown')
})
