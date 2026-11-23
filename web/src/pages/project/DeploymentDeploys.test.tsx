// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { getKey, newQueryClient, useGet } from '../../data'
import { DeploymentDeploys as PageDeploymentDeploys } from './DeploymentDeploys'
import type { ComponentProps } from 'react'
import type { DeploymentView, ReleaseListed } from '../../wire.gen'
function runtimeClient() {
  const client = newQueryClient()
  for (const name of ['main', 'production']) {
    client.setQueryData(getKey(`/projects/lunch/deployments/${name}/diagnostics`), { parts: [], reading: { unknown: false, stale: false } })
    client.setQueryData(getKey(`/projects/lunch/deployments/${name}/resources`), { parts: [], limits: [], limitsKnown: true, reading: { unknown: false, stale: false } })
  }
  return client
}
// The page reads the project's releases and hands them to the section.
function ActualDeploymentDeploys(props: Omit<ComponentProps<typeof PageDeploymentDeploys>, 'releases'>) {
  const releases = useGet<ReleaseListed[]>(`/projects/${props.slug}/releases`, { poll: 3000 })
  return <PageDeploymentDeploys {...props} releases={releases} />
}
function DeploymentDeploys(props: Omit<ComponentProps<typeof PageDeploymentDeploys>, 'env' | 'current' | 'reload' | 'releases'>) {
  const env = { status: { word: 'working', reason: '', stale: false }, id: 'deployment', name: props.name, generation: 1, kind: 'standing', routineUpdates: { mode: 'automatic' }, routineUpdatesRevision: 1, backups: true, runtimeState: 'running', runtimeActor: 'Priya', runtimeControls: { deploy: '', start: '', stop: '', restart: '' }, runtimeActions: ['start', 'stop', 'restart'], observation: 'running', servingRelease: null, promotionBundleId: null, promotionRelease: 0, promotionSource: '', approvalRole: '', feedRevision: 1, feedConfirmed: true, readinessLabel: 'deployed', restoreLabel: 'unknown', targetId: null, displayLabel: 'Lunch Orders', url: '', running: true, releaseRun: null, updatedAt: '', targetName: '', observationStale: false, protected: false } as DeploymentView
  // The status tab, then the history tab: the page shows them one at a time.
  return <><ActualDeploymentDeploys {...props} env={env} current reload={async () => {}} tab="status" /><ActualDeploymentDeploys {...props} env={env} current reload={async () => {}} tab="history" /></>
}



// The Status tab's rows, in order; and the History tab.
const rows = (el: Element) => [...el.querySelectorAll('.deployment-status-rows > .list-row')]
const history = (el: Element) => el.querySelector('section[aria-label="Deploy history"]')!

it.each(['releasing', 'released', 'failed'])('shows automatic and manual releases through the same history for %s', state => {
  const client = runtimeClient()
  client.setQueryData(getKey('/projects/lunch/releases'), [
    { number: 9, deployment: 'main', runId: 'landing', state, createdAt: '2026-11-05T01:01:00Z', failure: state === 'failed' ? 'follows main is not confirmed for this deployment' : '', changes: [{ id: 'change', number: 12, title: 'Fix lunch' }] },
    { number: 8, deployment: 'main', state: 'released', createdAt: '2026-11-05T01:00:00Z', changes: [] },
    { number: 10, deployment: 'other', state: 'released', createdAt: '', changes: [] },
  ])
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" displayLabel="Lunch Orders" /></QueryClientProvider></MemoryRouter>)
  expect(el.querySelector('h1')?.textContent).toContain('Lunch Orders')
  expect(el.querySelector('.page-bar-title .chip')?.textContent).toContain('working')
  const h = history(el)
  expect(h.querySelectorAll('a[href="/projects/lunch/releases/9"]')).toHaveLength(1)
  expect(h.querySelectorAll('a[href="/projects/lunch/releases/8"]')).toHaveLength(1)
  expect(h.querySelector('a[href="/projects/lunch/releases/10"]')).toBeNull()
  expect(h.querySelector('a[href="/changes/change"]')).not.toBeNull()
  if (state === 'failed') expect(rows(el)[0].textContent).toContain('follows main is not confirmed')
  if (state === 'releasing') expect(rows(el)[0].textContent).toContain('Release 9')
  client.clear()
})

it('shows an empty release history without manufacturing a deploy', () => {
  const client = runtimeClient()
  client.setQueryData(getKey('/projects/lunch/releases'), [])
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>)
  expect(html).toContain('No deploys yet.')
  expect(html).not.toContain('deploy-stages')
  client.clear()
})

it.each([true, false])('shows waiting release doors only when offered to this viewer: %s', eligible => {
  const client = runtimeClient()
  client.setQueryData(getKey('/projects/lunch/releases'), [{ number: 12, deployment: 'production', state: 'waiting for approval', approvalRole: 'release-manager', createdAt: '2026-11-05T01:00:00Z', changes: [], decision: { meaning: 'I accept release 12 running on production', canApprove: eligible, canCancel: eligible } }])
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="production" /></QueryClientProvider></MemoryRouter>)
  expect(rows(el)[0].textContent).toContain('Release 12')
  expect(rows(el)[0].textContent).toContain('waiting for approval')
  expect(history(el).textContent).toContain('Waiting for approval from release-manager.')
  expect(history(el).querySelectorAll('button')).toHaveLength(0)
  const buttons = [...el.querySelectorAll('button')]
  expect(buttons.filter(b => b.textContent === 'Approve')).toHaveLength(eligible ? 1 : 0)
  expect(buttons.filter(b => b.textContent === 'Cancel')).toHaveLength(eligible ? 1 : 0)
  expect(el.textContent?.includes('I accept release 12 running on production')).toBe(eligible)
  client.clear()
})

// Who approved a release, and what they said it means, is kept with it in
// History; the Status tab says what runs.
it('retains human assent in the history', () => {
  const client = runtimeClient()
  client.setQueryData(getKey('/projects/lunch/releases'), [{ number: 12, deployment: 'production', state: 'released', createdAt: '2026-11-05T01:00:00Z', changes: [], approval: { actor: { name: 'Priya', via: 'token-cli:laptop' }, at: '2026-11-05T01:02:00Z', meaning: 'I accept release 12 running on production' } }])
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="production" /></QueryClientProvider></MemoryRouter>)
  expect(history(el).textContent).toContain('Approved by Priya via token-cli:laptop')
  expect(history(el).textContent).toContain('I accept release 12 running on production')
  client.clear()
})

// A deploy on its way leads the Status tab with its stages; a failed one
// leads with the stage it failed at and why.
it.each(['preparing', 'backing-up', 'building', 'starting', 'checking', 'live'] as const)('renders live progress and failure at %s', stage => {
  const client = runtimeClient()
  const short = { preparing: 'Code', 'backing-up': 'Backup', building: 'Build', starting: 'Start', checking: 'Check', live: 'Live' }[stage]
  const draw = (state: string) => {
    client.setQueryData(getKey('/projects/lunch/releases'), [{ number: 9, deployment: 'main', state, stage, failedStage: state === 'failed' ? stage : undefined, failure: state === 'failed' ? 'it stopped' : '', stageElapsedMs: 15042, createdAt: '', changes: [] }])
    const el = document.createElement('div')
    el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>)
    return rows(el)[0]
  }
  const running = draw('releasing')
  expect(running.querySelector('li[aria-current="step"] .busy')).not.toBeNull()
  const failed = draw('failed')
  expect(failed.textContent).toContain(`failed at ${short}`)
  expect(failed.textContent).toContain('it stopped')
  client.clear()
})

// The Status tab shows a deploy's stages, never how long the current one has
// taken: that figure is only as true as the stage's progress, and a release
// that never moves on would read hours "getting the code ready". Kills: the
// elapsed stage time printed beside the stage.
it('shows a deploy in progress by its stages, with no stage duration', () => {
  const client = runtimeClient()
  client.setQueryData(getKey('/projects/lunch/releases'), [{ number: 9, deployment: 'main', state: 'releasing', stage: 'preparing', stageElapsedMs: 68_040_000, createdAt: '', changes: [] }])
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>)
  const lead = rows(el)[0]
  expect(lead.querySelector('li[aria-current="step"]')?.textContent).toContain('Code')
  expect(el.textContent).not.toMatch(/18\.9h|getting the code ready ·/)
  client.clear()
})

it.each(['releasing', 'released', 'failed'])('freezes stale deployment progress and shows a fresh %s read', async state => {
  const client = runtimeClient()
  const releases = [{ number: 9, deployment: 'main', state: 'releasing', stage: 'building', stageElapsedMs: 15000, createdAt: '', changes: [], failure: '' }]
  let fail = false
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    if (path.endsWith('/releases') && fail) return new Response('unavailable', { status: 503 })
    return new Response(JSON.stringify(path.endsWith('/releases') ? releases : path.endsWith('/diagnostics') ? { parts: [], reading: { unknown: false, stale: false } } : path.endsWith('/resources') ? { parts: [], limits: [], limitsKnown: true, reading: {} } : []), { headers: { 'Content-Type': 'application/json' } })
  }))
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const el = document.createElement('div')
  const root = createRoot(el)
  try {
    await act(async () => { root.render(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>) })
    await vi.waitFor(() => expect(rows(el)[0]?.querySelector('.busy')).not.toBeNull())
    fail = true
    await act(async () => { await client.refetchQueries({ queryKey: getKey('/projects/lunch/releases'), exact: true }) })
    await vi.waitFor(() => expect(rows(el)[0].textContent).toContain('Current progress is unknown'))
    expect(rows(el)[0].querySelector('.busy')).toBeNull()
    expect(rows(el)[0].textContent).toContain('Last seen: building')
    fail = false
    Object.assign(releases[0], { state, failedStage: state === 'failed' ? 'building' : undefined, failure: state === 'failed' ? 'the build failed' : '' })
    await act(async () => { await client.refetchQueries({ queryKey: getKey('/projects/lunch/releases'), exact: true }) })
    await vi.waitFor(() => expect(el.textContent).not.toContain('Current progress is unknown'))
    expect(rows(el)[0].querySelector('.busy') !== null).toBe(state === 'releasing')
    if (state === 'failed') expect(rows(el)[0].textContent).toContain('failed at Build')
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
  const lead = rows(el)[0]
  expect(lead.querySelector('.busy')).toBeNull()
  expect(lead.querySelector('.deploy-stages')?.textContent).not.toContain('now')
  expect(lead.textContent).toContain('Last seen: starting the app · Interrupted by a restart — checking.')
  client.clear()
})

// How long a deploy took starts at its approval when it waited for one: a
// release cut fifteen hours before a person approved it still deployed in
// 75 seconds. Kills: the duration measured from when the release was cut.
it('counts a deploy from its approval, not the wait for it', () => {
  const client = runtimeClient()
  const live = Date.now() - 60 * 60 * 1000
  const iso = (ms: number) => new Date(ms).toISOString()
  client.setQueryData(getKey('/projects/lunch/releases'), [{ number: 8, deployment: 'main', state: 'released', statusLabel: 'deployed', createdAt: iso(live - 15 * 3600 * 1000), releasedAt: iso(live), changes: [], approval: { actor: { name: 'Priya', via: 'web' }, at: iso(live - 75 * 1000), meaning: 'I accept release 8 running on main' } }])
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>)
  expect(history(el).textContent).toContain('took 75s')
  expect(history(el).textContent).not.toContain('15.0h')
  client.clear()
})

// A deployment nothing has run on says so and how it will go live, with no
// stages and no start, stop or restart.
it.each([['main', true, 'It deploys when a change lands on main.'], ['manual', false, 'Deploy a release to put it live.']] as const)('says a never-deployed %s deployment is not deployed yet', (feed, confirmed, how) => {
  const client = runtimeClient()
  client.setQueryData(getKey('/projects/lunch/releases'), [])
  const env = { status: { word: 'not deployed', reason: '', stale: false }, id: 'd', name: 'main', generation: 0, kind: 'standing', routineUpdates: { mode: 'automatic' }, routineUpdatesRevision: 1, feed, feedConfirmed: confirmed, runtimeState: 'stopped', runtimeControls: {}, runtimeActions: [], observation: 'stopped' } as unknown as DeploymentView
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><ActualDeploymentDeploys slug="lunch" name="main" env={env} current reload={async () => {}} /></QueryClientProvider></MemoryRouter>)
  expect(rows(el)[0].textContent).toContain(`Not deployed yet${how}`)
  expect(el.querySelector('.deploy-stages')).toBeNull()
  expect(el.textContent).not.toMatch(/Stop|Restart/)
  expect([...el.querySelectorAll('button')].some(b => b.textContent === 'Deploy a release…')).toBe(feed !== 'main')
  client.clear()
})

// A failed latest deploy leads the rows as failed, whatever the runtime says.
it('calls a failed deploy failed, not stopped', () => {
  const client = runtimeClient()
  const at = new Date().toISOString()
  client.setQueryData(getKey('/projects/lunch/releases'), [{ number: 3, deployment: 'main', state: 'failed', failedStage: 'starting', lastStage: 'starting', failure: 'The app container exited during startup.', createdAt: at, stageEnteredAt: at, changes: [] }])
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="main" /></QueryClientProvider></MemoryRouter>)
  expect(rows(el)[0].querySelector('.chip')?.textContent).toContain('failed at Start')
  expect(rows(el)[0].textContent).toContain('The app container exited during startup.')
  client.clear()
})

it.each(['waiting for a person to release this update', 'waiting for a person: the latest release failed'])('shows a personal wait in the deployment and history: %s', waitReason => {
  const client = runtimeClient()
  client.setQueryData(getKey('/projects/lunch/releases'), [{ number: 12, deployment: 'production', state: 'waiting for approval', statusLabel: 'waiting for approval', approvalRole: '', createdAt: '2026-11-05T01:00:00Z', changes: [], decision: { waitReason, meaning: 'I accept release 12 running on production', canApprove: true, canCancel: true } }])
  try {
    const el = document.createElement('div')
    el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="production" /></QueryClientProvider></MemoryRouter>)
    expect(el.textContent?.split(waitReason)).toHaveLength(3)
    expect(el.textContent?.toLowerCase()).not.toContain('waiting for approval')
    expect([...el.querySelectorAll('button')].filter(b => b.textContent === 'Release')).toHaveLength(1)
  } finally { client.clear() }
})

it.each(['release-manager', ''])('keeps an older waiting decision available after a newer release fails: %s', approvalRole => {
  const client = runtimeClient()
  const waitReason = approvalRole ? undefined : 'waiting for a person: the latest release failed'
  client.setQueryData(getKey('/projects/lunch/releases'), [
    { number: 13, deployment: 'production', state: 'failed', createdAt: '2026-11-05T02:00:00Z', changes: [], failure: 'The app exited during startup.' },
    { number: 12, deployment: 'production', state: 'waiting for approval', approvalRole, createdAt: '2026-11-05T01:00:00Z', changes: [], decision: { waitReason, meaning: 'I accept release 12 running on production', canApprove: true, canCancel: true } },
  ])
  try {
    const el = document.createElement('div')
    el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeploymentDeploys slug="lunch" name="production" /></QueryClientProvider></MemoryRouter>)
    const [failed, pending] = rows(el)
    expect(failed.textContent).toContain('The app exited during startup.')
    expect(pending.textContent).toContain(approvalRole ? 'Waiting for approval from release-manager.' : waitReason)
    expect(pending.textContent).toContain('I accept release 12 running on production')
    expect([...pending.querySelectorAll('button')].map(b => b.textContent)).toEqual([approvalRole ? 'Approve' : 'Release', 'Cancel'])
  } finally { client.clear() }
})
