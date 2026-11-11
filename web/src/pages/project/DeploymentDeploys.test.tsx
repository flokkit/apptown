// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { deployWords } from '../change/DeployScene'
import { getKey, newQueryClient } from '../../data'
import { DeploymentDeploys as ActualDeploymentDeploys } from './DeploymentDeploys'
import type { ComponentProps } from 'react'
import type { DeploymentView } from '../../wire.gen'
import css from './deployment-deploys.css?inline'
function runtimeClient() {
  const client = newQueryClient()
  for (const name of ['main', 'production']) client.setQueryData(getKey(`/projects/lunch/deployments/${name}/diagnostics`), { parts: [], reading: { unknown: false, stale: false } })
  return client
}
function DeploymentDeploys(props: Omit<ComponentProps<typeof ActualDeploymentDeploys>, 'env' | 'current' | 'reload'>) {
  const env = { status: { word: 'working', reason: '', stale: false }, id: 'deployment', name: props.name, generation: 1, kind: 'standing', backups: true, runtimeState: 'running', runtimeActor: 'Priya', runtimeControls: { deploy: '', start: '', stop: '', restart: '' }, runtimeActions: ['start', 'stop', 'restart'], observation: 'running', servingRelease: null, promotionBundleId: null, promotionRelease: 0, promotionSource: '', approvalRole: '', feedRevision: 1, feedConfirmed: true, readinessLabel: 'deployed', restoreLabel: 'unknown', targetId: null, displayLabel: 'Lunch Orders', url: '', running: true, releaseRun: null, updatedAt: '', targetName: '', observationStale: false, protected: false } as DeploymentView
  // The status tab, then the history tab: the page shows them one at a time.
  return <><ActualDeploymentDeploys {...props} env={env} current reload={async () => {}} tab="status" /><ActualDeploymentDeploys {...props} env={env} current reload={async () => {}} tab="history" /></>
}


it.each(['releasing', 'released', 'failed'])('shows automatic and manual releases through the same history for %s', state => {
  const client = runtimeClient()
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
  expect(el.querySelector('h1 .chip')?.textContent).toContain('working')
  const history = el.querySelector('section:last-child')!
  expect(history.querySelectorAll('a[href="/projects/lunch/releases/9"]')).toHaveLength(1)
  expect(history.querySelectorAll('a[href="/projects/lunch/releases/8"]')).toHaveLength(1)
  expect(history.querySelector('a[href="/projects/lunch/releases/10"]')).toBeNull()
  expect(history.querySelector('a[href="/changes/change"]')).not.toBeNull()
  if (state === 'failed') expect(el.querySelector('.deployment-deploy-card')?.textContent).toContain('follows main is not confirmed')
  client.clear()
})

it('shows an empty release history without manufacturing a deploy', () => {
  const client = runtimeClient()
  client.setQueryData(getKey('/projects/lunch/releases'), [])
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>)
  expect(html).toContain('No deploys yet.')
  expect(html).not.toContain('Release details')
  client.clear()
})

it.each([true, false])('shows waiting release doors only when offered to this viewer: %s', eligible => {
  const client = runtimeClient()
  client.setQueryData(getKey('/projects/lunch/releases'), [{ number: 12, deployment: 'production', state: 'waiting for approval', approvalRole: 'release-manager', createdAt: '2026-11-05T01:00:00Z', changes: [], decision: { meaning: 'I accept release 12 running on production', canApprove: eligible, canCancel: eligible } }])
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="production" /></QueryClientProvider></MemoryRouter>)
  expect(el.textContent).toContain('Waiting for approval')
  expect(el.textContent).not.toContain('Deploy stopped')
  const history = el.querySelector('section:last-child')!
  expect(history.textContent).toContain('Waiting for approval from release-manager.')
  expect(history.querySelectorAll('button')).toHaveLength(0)
  expect(el.textContent).not.toContain('See the release for its backup outcome.')
  const buttons = [...el.querySelectorAll('button')]
  expect(buttons.filter(b => b.textContent === 'Approve')).toHaveLength(eligible ? 1 : 0)
  expect(buttons.filter(b => b.textContent === 'Cancel')).toHaveLength(eligible ? 1 : 0)
  expect(el.textContent?.includes('I accept release 12 running on production')).toBe(eligible)
  client.clear()
})

it('retains human assent on the deployment card and its history', () => {
  const client = runtimeClient()
  client.setQueryData(getKey('/projects/lunch/releases'), [{ number: 12, deployment: 'production', state: 'released', createdAt: '2026-11-05T01:00:00Z', changes: [], approval: { actor: { name: 'Priya', via: 'token-cli:laptop' }, at: '2026-11-05T01:02:00Z', meaning: 'I accept release 12 running on production' } }])
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="production" /></QueryClientProvider></MemoryRouter>)
  expect(el.textContent?.match(/Approved by Priya via token-cli:laptop/g)).toHaveLength(2)
  expect(el.textContent?.match(/I accept release 12 running on production/g)).toHaveLength(2)
  client.clear()
})

it.each(['preparing', 'backing-up', 'building', 'starting', 'checking', 'live'] as const)('renders live progress and failure at %s', stage => {
  const client = runtimeClient()
  const draw = (state: string) => {
    client.setQueryData(getKey('/projects/lunch/releases'), [{ number: 9, deployment: 'main', state, stage, failedStage: state === 'failed' ? stage : undefined, stageElapsedMs: 15042, createdAt: '', changes: [] }])
    const el = document.createElement('div')
    el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>)
    return el.querySelector('.deployment-deploy-card')!
  }
  const running = draw('releasing')
  expect(running.querySelector('.deploy-scene.paused')).toBeNull()
  expect(running.querySelector('li[aria-current="step"] .busy')).not.toBeNull()
  // While the code is prepared the picture would be an empty outline, so
  // there is none; the heading and the stage list carry the progress.
  expect(running.querySelector('svg')?.getAttribute('aria-label')).toBe(stage === 'preparing' ? undefined : stage === 'live' ? 'The app is live' : `Deploying: ${deployWords[stage]}`)
  expect(running.querySelector('.ch-deploy-progress')?.textContent).toBe(`${deployWords[stage]} · 15s`)
  const failed = draw('failed')
  expect(failed.querySelector('.deploy-scene.fail li[aria-current="step"] .bad')).not.toBeNull()
  expect(failed.querySelector('svg')?.getAttribute('aria-label')).toBe(`Stopped while ${deployWords[stage]}`)
  client.clear()
})

it('polls and updates elapsed time without changing stages', async () => {
  vi.useFakeTimers()
  const client = runtimeClient()
  let elapsed = 15000
  const releases = () => [{ number: 9, deployment: 'main', state: 'releasing', stage: 'checking', stageElapsedMs: elapsed, createdAt: '', changes: [] }]
  client.setQueryData(getKey('/projects/lunch/releases'), releases())
  const fetcher = vi.fn(async (path: string) => new Response(JSON.stringify(path.endsWith('/releases') ? releases() : path.endsWith('/diagnostics') ? { parts: [], reading: { unknown: false, stale: false } } : []), { headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetcher)
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const el = document.createElement('div')
  const root = createRoot(el)
  try {
    await act(async () => { root.render(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>) })
    expect(el.querySelector('.ch-deploy-progress')?.textContent).toContain('15s')
    elapsed = 18000
    await act(async () => { await vi.advanceTimersByTimeAsync(3100) })
    expect(el.querySelector('.ch-deploy-progress')?.textContent).toContain('18s')
    expect(fetcher.mock.calls.filter(([path]) => path.endsWith('/releases')).length).toBeGreaterThanOrEqual(2)
  } finally {
    await act(async () => root.unmount())
    client.clear()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  }
})

it.each(['releasing', 'released', 'failed'])('freezes stale deployment progress and shows a fresh %s read', async state => {
  const client = runtimeClient()
  const releases = [{ number: 9, deployment: 'main', state: 'releasing', stage: 'building', stageElapsedMs: 15000, createdAt: '', changes: [] }]
  let fail = false
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    if (path.endsWith('/releases') && fail) return new Response('unavailable', { status: 503 })
    return new Response(JSON.stringify(path.endsWith('/releases') ? releases : path.endsWith('/diagnostics') ? { parts: [], reading: { unknown: false, stale: false } } : []), { headers: { 'Content-Type': 'application/json' } })
  }))
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const el = document.createElement('div')
  const root = createRoot(el)
  try {
    await act(async () => { root.render(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>) })
    await vi.waitFor(() => expect(el.querySelector('.deployment-deploy-card .busy')).not.toBeNull())
    fail = true
    await act(async () => { await client.refetchQueries({ queryKey: getKey('/projects/lunch/releases'), exact: true }) })
    await vi.waitFor(() => expect(el.querySelector('.deployment-deploy-card .stale-note')).not.toBeNull())
    expect(el.querySelector('.deployment-deploy-card .busy')).toBeNull()
    expect(el.querySelector('.deployment-deploy-card .deploy-scene.paused')).not.toBeNull()
    expect(el.querySelector('.deployment-deploy-card .deploy-stages')?.textContent).not.toContain('now')
    expect(el.querySelector('.ch-deploy-progress')?.textContent).toContain('Last seen: building')
    expect(el.querySelector('.ch-deploy-progress')?.textContent).toContain('Current progress is unknown')
    expect(el.querySelector('.ch-deploy-progress')?.textContent).not.toContain('15s')
    fail = false
    Object.assign(releases[0], { state, failedStage: state === 'failed' ? 'building' : undefined })
    await act(async () => { await client.refetchQueries({ queryKey: getKey('/projects/lunch/releases'), exact: true }) })
    await vi.waitFor(() => expect(el.querySelector('.deployment-deploy-card .stale-note')).toBeNull())
    expect(el.querySelector('.deployment-deploy-card .deploy-scene.paused')).toBeNull()
    expect(el.querySelector('.deployment-deploy-card .busy') !== null).toBe(state === 'releasing')
    if (state === 'released') expect(el.querySelector('.deployment-deploy-card')?.textContent).toContain("It's live!")
    if (state === 'failed') expect(el.querySelector('.deployment-deploy-card .bad')).not.toBeNull()
  } finally {
    await act(async () => root.unmount())
    client.clear()
    vi.unstubAllGlobals()
  }
})

it('shows an interrupted stage as history without active time or animation', () => {
  const client = runtimeClient()
  client.setQueryData(getKey('/projects/lunch/releases'), [{ number: 9, deployment: 'main', state: 'releasing', lastStage: 'starting', progressUncertain: true, interruptedByRestart: true, createdAt: '', changes: [] }])
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>)
  const card = el.querySelector('.deployment-deploy-card')!
  expect(card.querySelector('.deploy-scene.paused')).not.toBeNull()
  expect(card.querySelector('.busy')).toBeNull()
  expect(card.querySelector('.deploy-stages')?.textContent).not.toContain('now')
  expect(card.querySelector('.ch-deploy-progress')?.textContent).toBe('Last seen: starting the app · Interrupted by a restart — checking.')
  expect(card.querySelector('.ch-deploy-heading')?.textContent).toBe('Deployment progress uncertain')
  client.clear()
})

// A deploy that ended a while ago is one line, keeping how it ended (the
// stage it stopped at, its failure, who approved it); a recent one keeps
// the scene so its outcome is seen.
it.each([[true, 'failed'], [true, 'released'], [false, 'released']] as const)('shows a deploy that ended long ago (%s) as one line: %s', async (old, state) => {
  const client = runtimeClient()
  const at = new Date(Date.now() - (old ? 60 : 1) * 60 * 1000).toISOString()
  const releases = [{ number: 9, deployment: 'main', state, statusLabel: state === 'failed' ? 'failed' : 'deployed', failedStage: state === 'failed' ? 'building' : undefined, failure: state === 'failed' ? 'the build ran out of disk' : '', createdAt: at, releasedAt: state === 'released' ? at : undefined, changes: [], approval: { actor: { name: 'Priya', via: 'web' }, at, meaning: 'I accept release 9 running on main' } }]
  vi.stubGlobal('fetch', vi.fn(async (path: string) => new Response(JSON.stringify(path.endsWith('/releases') ? releases : []), { headers: { 'Content-Type': 'application/json' } })))
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const el = document.createElement('div')
  const root = createRoot(el)
  try {
    await act(async () => { root.render(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>) })
    await vi.waitFor(() => expect(el.querySelector('[aria-label="Latest deploy"]')?.textContent).toContain('Approved by Priya'))
    const latest = el.querySelector('[aria-label="Latest deploy"]')!
    expect(latest.classList.contains('deployment-latest-line')).toBe(old)
    expect(latest.querySelector('.deploy-scene') === null).toBe(old)
    expect(latest.textContent).toContain('Approved by Priya')
    if (state === 'failed') {
      expect(latest.textContent).toContain(`while ${deployWords.building}`)
      expect(latest.textContent).toContain('the build ran out of disk')
    }
  } finally {
    await act(async () => root.unmount())
    client.clear()
    vi.unstubAllGlobals()
  }
})

// A deployment nothing has run on says so and how it will go live, with no
// empty deploy scene and no start, stop or restart.
it.each([['main', true, 'It deploys when a change lands on main.'], ['manual', false, 'Deploy a release to put it live.']] as const)('says a never-deployed %s deployment is not deployed yet', (feed, confirmed, how) => {
  const client = runtimeClient()
  client.setQueryData(getKey('/projects/lunch/releases'), [])
  const env = { status: { word: 'stopped', reason: '', stale: false }, id: 'd', name: 'main', generation: 0, kind: 'standing', feed, feedConfirmed: confirmed, runtimeState: 'stopped', runtimeControls: {}, observation: 'stopped' } as unknown as DeploymentView
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><ActualDeploymentDeploys slug="lunch" name="main" env={env} current reload={async () => {}} /></QueryClientProvider></MemoryRouter>)
  expect(el.textContent).toContain(`Not deployed yet. ${how}`)
  expect(el.querySelector('.deploy-scene')).toBeNull()
  expect(el.textContent).not.toMatch(/Stop|Restart/)
  expect([...el.querySelectorAll('button')].some(b => b.textContent === 'Deploy a release…')).toBe(feed !== 'main')
  client.clear()
})


it('calls a failed deploy failed, not stopped', () => {
  const client = runtimeClient()
  const at = new Date().toISOString()
  client.setQueryData(getKey('/projects/lunch/releases'), [{ number: 3, deployment: 'main', state: 'failed', failedStage: 'starting', lastStage: 'starting', failure: 'The app container exited during startup.', createdAt: at, stageEnteredAt: at, changes: [] }])
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>)
  expect(el.querySelector('.ch-deploy-heading')?.textContent).toBe('Deploy failed')
  expect(el.textContent).not.toContain('Deploy stopped')
  client.clear()
})
