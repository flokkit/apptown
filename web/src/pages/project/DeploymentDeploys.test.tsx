// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it } from 'vitest'
import { getKey, newQueryClient } from '../../data'
import { DeploymentDeploys } from './DeploymentDeploys'
import css from './deployment-deploys.css?inline'

it.each(['releasing', 'released', 'failed'])('shows automatic and manual releases through the same history for %s', state => {
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/lunch/releases'), [
    { number: 9, deployment: 'main', runId: 'landing', state, createdAt: '2026-11-05T01:01:00Z', failure: state === 'failed' ? 'follows main is not confirmed for this deployment' : '', changes: [{ id: 'change', number: 12, title: 'Fix lunch' }] },
    { number: 8, deployment: 'main', state: 'released', createdAt: '2026-11-05T01:00:00Z', changes: [] },
    { number: 10, deployment: 'other', state: 'released', createdAt: '', changes: [] },
  ])
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" displayLabel="Lunch Orders" /></QueryClientProvider></MemoryRouter>)
  expect(el.querySelectorAll('.deployment-deploy-card')).toHaveLength(1)
  expect(css).toMatch(/\.deployment-deploy-card\s*\{[^}]*height: 660px;[^}]*overflow: auto;/)
  expect(el.querySelector('h1')?.textContent).toContain('Lunch Orders')
  expect(el.querySelector('h1 .chip')?.textContent).toContain(state === 'released' ? 'live' : state === 'releasing' ? 'deploying' : 'stopped')
  const history = el.querySelector('section:last-child')!
  expect(history.querySelectorAll('a[href="/projects/lunch/releases/9"]')).toHaveLength(1)
  expect(history.querySelectorAll('a[href="/projects/lunch/releases/8"]')).toHaveLength(1)
  expect(history.querySelector('a[href="/projects/lunch/releases/10"]')).toBeNull()
  expect(history.querySelector('a[href="/changes/change"]')).not.toBeNull()
  if (state === 'failed') expect(el.querySelector('.deployment-deploy-card')?.textContent).toContain('follows main is not confirmed')
  client.clear()
})

it('shows an empty release history without manufacturing a deploy', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/lunch/releases'), [])
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>)
  expect(html).toContain('No deploys yet.')
  expect(html).not.toContain('Release details')
  client.clear()
})
