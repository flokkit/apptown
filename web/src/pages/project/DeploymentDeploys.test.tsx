// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { getKey, newQueryClient } from '../../data'
import { DeploymentDeploys } from './DeploymentDeploys'
import css from './deployment-deploys.css?inline'
import type { LandingDeployAttempt } from '../../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const attempt = (status: string): LandingDeployAttempt => ({
  outcomeKnown: true, attempt: 1, changeId: 'change', changeNumber: 12, changeTitle: 'Fix lunch', runStartedAt: '2026-11-05T01:00:00Z',
  step: { id: 'deploy', runId: 'run', type: 'deploy', status, config: { deployment: 'main' },
  evidence: { failedStage: 'starting', reason: 'The container exited', url: 'https://lunch.example', backup: { outcome: 'no-data', reason: 'nothing to copy' } },
  startedAt: '2026-11-05T01:01:00Z', finishedAt: status === 'running' ? '' : '2026-11-05T01:01:12Z' },
})
function clientFor(status?: string) {
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/lunch/deployments/main/deploys'), status ? [attempt(status)] : [])
  client.setQueryData(getKey('/projects/lunch/releases'), status ? [{ number: 8, deployment: 'main', runId: 'run', state: 'released' }] : [])
  return client
}
it.each([undefined, 'running', 'pass', 'fail'])('reserves one card with a fixed scene and compact stages for %s', status => {
  const client = clientFor(status)
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>)
  expect(el.querySelectorAll('.deployment-deploy-card')).toHaveLength(1)
  expect(css).toMatch(/\.deployment-deploy-card\s*\{[^}]*height: 660px;[^}]*overflow: auto;/)
  expect(css).toContain('grid-template-columns: max-content minmax(0, 1fr)')
  expect(css).toMatch(/\.deploy-scene-panel\s*\{[^}]*height: 150px;/)
  expect(css).toMatch(/\.deploy-stages-compact\s*\{[^}]*display: flex;/)
  expect(el.querySelector('.deployment-deploy-card')?.textContent).toContain(status === 'pass' ? "It's live! Release 8" : status === 'running' ? 'Deploying' : status === 'fail' ? 'Deploy stopped' : 'No deploys yet.')
  if (status !== 'pass') expect(el.querySelector('.deployment-deploy-card')?.textContent).not.toContain('Release 8')
  expect(el.querySelector('h1')?.textContent).toContain('main')
  expect(el.querySelector('h1 .chip')?.textContent).toContain(status === 'pass' ? 'live' : status === 'running' ? 'deploying' : status === 'fail' ? 'stopped' : 'not deployed')
  expect(el.querySelector('.deployment-deploy-card details')).toBeNull()
  expect([...el.querySelectorAll('.deploy-stages li > span:nth-child(2)')].map(s => s.textContent)).toEqual(['Code', 'Backup', 'Build', 'Start', 'Check', 'Live'])
  if (status === 'pass') {
    expect(el.querySelector('.ch-deploy-heading strong')?.textContent).toBe("It's live!")
    expect(el.querySelector('.ch-deploy-heading')?.textContent).toContain('Release 8')
    expect(el.querySelector('.ch-deploy-heading .ch-deploy-times')).toBeNull()
    expect(el.querySelector('.ch-deploy-result-facts')?.textContent).toContain('Landing took 1m 12s')
  }
  expect(el.querySelector('.celebrate')).toBeNull()
  client.clear()
})
it('lists unsuccessful invocations without release numbers and links only the successful invocation', () => {
  const client = clientFor('pass')
  client.setQueryData(getKey('/projects/lunch/deployments/main/deploys'), [attempt('pass'), { ...attempt('fail'), attempt: 0 }])
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>)
  const history = el.querySelector('section:last-child')!
  expect(history.querySelectorAll('a[href="/projects/lunch/releases/8"]')).toHaveLength(1)
  expect(history.querySelectorAll('a[href="/changes/change"]')).toHaveLength(2)
  expect(history.textContent).toContain('No data to back up')
  client.clear()
})
it('keeps the watched scene mounted and celebrates only once when its attempt succeeds', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(String(url).includes('/live')
    ? { running: true, stage: 'building', elapsedMs: 8000, lines: [], total: 0 }
    : String(url).endsWith('/deploys') ? [attempt('running')] : [{ number: 8, deployment: 'main', runId: 'run', state: 'released' }]))))
  const client = clientFor('running'), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>))
    await vi.waitFor(() => expect(el.querySelector('.deploy-scene.n2')).not.toBeNull())
    const scene=el.querySelector('.deploy-scene')
    await act(async () => { client.setQueryData(getKey('/projects/lunch/deployments/main/deploys'), [attempt('pass')]) })
    expect(el.querySelector('.deploy-scene')).toBe(scene)
    await vi.waitFor(() => expect(scene?.classList.contains('celebrate')).toBe(true))
    await act(async () => { const e = new Event('webkitAnimationEnd', { bubbles: true }); Object.defineProperty(e, 'animationName', { value: 'deploy-burst' }); scene!.dispatchEvent(e) })
    expect(scene?.classList.contains('celebrate')).toBe(false)
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})


it.each(['releasing', 'released', 'failed'])('shows the current manual release state for %s', state => {
  const client = clientFor()
  client.setQueryData(getKey('/projects/lunch/releases'), [{ number: 9, deployment: 'main', state, createdAt: '2026-11-05T01:00:00Z', changes: [] }])
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" displayLabel="Lunch Orders" /></QueryClientProvider></MemoryRouter>)
  expect(el.querySelector('h1')?.textContent).toContain('Lunch Orders')
  expect(el.querySelector('h1 .chip')?.textContent).toContain(state === 'released' ? 'live' : state === 'releasing' ? 'deploying' : 'stopped')
  expect(el.querySelector('.deployment-deploy-card details')).toBeNull()
  expect(el.querySelectorAll('.deploy-stages li')).toHaveLength(6)
  if (state === 'released') {
    expect(el.querySelector('.ch-deploy-heading strong')?.textContent).toBe("It's live!")
    expect(el.querySelector('.ch-deploy-heading')?.textContent).toContain('Release 9')
  }
  client.clear()
})

it('keeps an unrecorded outcome unknown in the page heading', () => {
  const client = clientFor('pass')
  client.setQueryData(getKey('/projects/lunch/deployments/main/deploys'), [{ ...attempt('pass'), outcomeKnown: false }])
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>)
  expect(el.querySelector('h1 .chip')?.textContent).toContain('unknown')
  expect(el.querySelector('.ch-deploy-heading')?.textContent).toBe('Deploy status unknown')
  client.clear()
})
