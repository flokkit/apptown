// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import type { ChangeView, Step } from '../../api'
import { getKey, newQueryClient } from '../../data'
import { DeployResult, previousLanding, landingDuration } from './DeployResult'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const view = { projectSlug: 'club', change: { id: 'change' }, detail: { run: { createdAt: '2026-11-05T01:00:00Z' } }, evidence: { releases: [{ number: 7 }, { number: 6 }] } } as ChangeView
const step = { id: 'deploy', runId: 'landing', type: 'deploy', config: { deployment: 'main' }, status: 'pass', startedAt: '2026-11-05T01:01:00Z', finishedAt: '2026-11-05T01:01:12Z', evidence: { url: 'https://club.example.test' } } as unknown as Extract<Step, { type: 'deploy' }>
function draw(s = step, v = view, showRunning = false, releases: import('../../wire.gen').ReleaseListed[] = []) {
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/club/releases'), releases)
  client.setQueryData(getKey('/runs/old/trace'), { run: { createdAt: '2026-11-04T01:00:00Z' }, steps: [{ type: 'deploy', status: 'pass', finishedAt: '2026-11-04T01:01:12Z' }] })
  return renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><DeployResult view={v} step={s} showRunning={showRunning} /></QueryClientProvider></MemoryRouter>)
}
it('ends a completed landing with the known release, address, duration and app link, without replay', () => {
  const html = draw()
  for (const text of ['Release 7 is live', 'https://club.example.test', 'Landing took 1m 12s', 'Open the app']) expect(html).toContain(text)
  expect(html).not.toContain('celebrate')
  expect(draw(step, { ...view, evidence: { ...view.evidence, releases: [] } })).toContain("It&#x27;s live!")
})
it('names the recorded failed stage and reason, and says the app is down only once it was replaced', () => {
  const html = draw({ ...step, status: 'fail', evidence: { failedStage: 'starting', reason: 'The app container exited.' } })
  for (const text of ['Stopped while starting the app', 'The app container exited.']) expect(html).toContain(text)
  expect(html).not.toContain('not being served')
  expect(draw({ ...step, status: 'fail', evidence: { failedStage: 'checking', reason: 'No answer.' } })).toContain('The app is not being served until the next deploy.')
  expect(html).not.toContain('still serving')
  expect(html).not.toContain('Open the app')
})

it('orders the finished scene, live heading, host, facts and primary action', () => {
  const el = document.createElement('div'); el.innerHTML = draw()
  const result = el.querySelector('.ch-deploy-result')!
  expect(result.querySelectorAll('.deploy-stages')).toHaveLength(0)
  expect(result.querySelector('h3')?.classList.contains('ok')).toBe(true)
  expect([...result.querySelectorAll('a')].map(a => a.textContent)).toEqual(['club.example.test', 'Open the app'])
  expect(result.querySelector('a.app-address')?.getAttribute('href')).toBe('https://club.example.test')
  expect(result.querySelector('a.app-address')?.getAttribute('rel')).toContain('noopener')
  expect(result.querySelector('a.app-address')?.getAttribute('target')).toBe('_blank')
  expect(result.querySelector('a.primary')).not.toBeNull()
  const order = ['.deploy-scene', 'h3', 'p a', '.small', 'a.primary'].map(s => result.querySelector(s)!)
  for (let i = 1; i < order.length; i++) expect(order[i - 1].compareDocumentPosition(order[i]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
})

it('omits the comparison when no previous successful duration is recorded', () => {
  expect(draw({ ...step, status: 'running' }, view, true)).not.toContain('Last deploy took')
  expect(previousLanding([], 'main', view.detail!.run.createdAt)).toBeUndefined()
  expect(landingDuration(undefined)).toBeUndefined()
})
it('chooses the latest successful landing before this deployment on the same deployment', () => {
  const previous = { number: 7, deployment: 'main', state: 'released', releasedAt: '2026-11-04T01:01:12Z' } as import('../../wire.gen').ReleaseListed
  const releases = [previous, { ...previous, number: 6 }, { ...previous, number: 8, state: 'failed' }, { ...previous, number: 9, deployment: 'test' }, { ...previous, number: 10, releasedAt: '2026-11-06T01:00:00Z' } ] as typeof previous[]
  expect(previousLanding(releases, 'main', step.startedAt)).toBe(previous)
  expect(landingDuration({ run: { createdAt: '2026-11-04T01:00:00Z' }, steps: [{ type: 'deploy', status: 'pass', finishedAt: '' }] } as import('../../wire.gen').RunTrace)).toBeUndefined()
})

it('includes a previous landing that finished after this run began but before deployment started', () => {
  const current = { number: 8, runId: 'landing', deployment: 'main', state: 'releasing' } as import('../../wire.gen').ReleaseListed
  const previous = { ...current, number: 7, runId: 'old', state: 'released', createdAt: '2026-11-05T01:00:30Z', releasedAt: '2026-11-05T01:00:30Z' } as typeof current
  const html = draw({ ...step, status: 'running' }, { ...view, evidence: { ...view.evidence, releases: [{ number: 8 }] } } as ChangeView, true, [current, previous])
  expect(html).toContain('Last deploy took 1m 12s')
  expect(html).toContain('Release 8')
  expect(html).not.toContain('Deploying release')
})

it('counts elapsed seconds between server progress reads', async () => {
  vi.useFakeTimers()
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(String(url).includes('/live')
    ? { running: true, stage: 'building', elapsedMs: 8000, lines: [], total: 0 } : []))))
  const el = document.createElement('div'), root = createRoot(el), client = newQueryClient()
  try {
    await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><DeployResult view={view} step={{ ...step, status: 'running' }} showRunning /></QueryClientProvider></MemoryRouter>))
    await act(async () => { await vi.advanceTimersByTimeAsync(100) })
    expect(el.querySelector('.ch-deploy-times')?.textContent).toBe('8s')
    await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
    expect(el.querySelector('.ch-deploy-times')?.textContent).toBe('9s')
    expect(vi.mocked(fetch).mock.calls.filter(([url]) => String(url).includes('/live'))).toHaveLength(1)
  } finally { act(() => root.unmount()); client.clear(); vi.useRealTimers(); vi.unstubAllGlobals() }
})

it('names the configured deployment or its main default before a release number is recorded', () => {
  const v = { ...view, evidence: { ...view.evidence, releases: [] } }
  for (const [config, name] of [[{}, 'main'], [{ deployment: 'staff' }, 'staff']] as const) {
    const el = document.createElement('div')
    el.innerHTML = draw({ ...step, config, status: 'running' }, v, true)
    expect(el.querySelector('.ch-deploy-heading strong')?.textContent).toBe(name)
  }
})

it('claims a backup only for a complete set, linking to that recorded copy', () => {
  const backup = { outcome: 'ok', reason: 'complete', set: { id: 'copy-7', takenAt: '2026-11-05T01:01:00Z', bytes: 2048, status: 'ok' as const } }
  const el = document.createElement('div'); el.innerHTML = draw({ ...step, evidence: { ...step.evidence, backup } })
  const line = el.querySelector('.ch-deploy-backup')!
  expect(line.textContent).toBe('Backed up before this deploy · 2 KB →')
  expect(line.querySelector('a')?.getAttribute('href')).toBe('/deployments/backups#backup-copy-7')
  expect(el.querySelector('h3')!.compareDocumentPosition(line) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
})
it.each([
  ['partial', 'partial', 'Backup incomplete'], ['ok', 'partial', 'Backup incomplete'],
  ['partial', 'ok', 'Backup incomplete'], ['unavailable', undefined, 'No backup'],
] as const)('keeps %s / %s distinct from a complete backup and names who accepted', (outcome, status, words) => {
  const backup = { outcome, reason: 'Database copy failed', ...(status ? { set: { id: 'copy', takenAt: '', bytes: 0, status } } : {}), accepted: { by: 'Priya', at: '2026-11-05T01:00:00Z', attempt: 2 } }
  const el = document.createElement('div'); el.innerHTML = draw({ ...step, evidence: { ...step.evidence, backup } })
  expect(el.querySelector('.ch-deploy-backup')?.textContent).toBe(`${words}: Database copy failed · went ahead, accepted by Priya`)
  expect(el.querySelector('.ch-deploy-backup')?.textContent).not.toMatch(/✓|Backed up/)
})
it('states no data separately from missing evidence and an unaccepted incomplete copy', () => {
  const el = document.createElement('div')
  el.innerHTML = draw({ ...step, evidence: { ...step.evidence, backup: { outcome: 'no-data', reason: 'nothing to copy' } } })
  expect(el.querySelector('.ch-deploy-backup')?.textContent).toBe('No data to back up')
  el.innerHTML = draw()
  expect(el.querySelector('.ch-deploy-backup')).toBeNull()
  el.innerHTML = draw({ ...step, status: 'fail', evidence: { backup: { outcome: 'partial', set: { id: 'copy', takenAt: '', bytes: 0, status: 'partial' }, reason: 'Volume unreadable' } } })
  expect(el.querySelector('.ch-deploy-backup')?.textContent).toBe('Backup incomplete: Volume unreadable')
})
