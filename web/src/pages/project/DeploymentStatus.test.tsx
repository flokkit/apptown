// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it } from 'vitest'
import { getKey, newQueryClient, type Read } from '../../data'
import { enabledFeatures } from '../../features.fixture'
import { DeploymentStatus } from './DeploymentStatus'
// @ts-expect-error -- Node is used only to read the shared stylesheet.
import { readFileSync } from 'node:fs'
import type { DeploymentView, ReleaseListed } from '../../wire.gen'

// The Status tab of one deployment, with the reads it makes answered.
const base = '/projects/lunch/deployments/main'
const at = '2026-11-20T08:00:00Z'
const reading = { observedAt: at, attemptedAt: at, waiting: false, stale: false, unknown: false }
const fig = (value: number) => ({ known: true, value })
const env = (more: Partial<DeploymentView> = {}) => ({
  id: 'main', name: 'main', displayLabel: '', kind: 'standing', generation: 4, backups: true, feed: 'main', feedConfirmed: true, readiness: 'reconciled', readinessLabel: 'deployed',
  url: 'https://lunch.example', runtimeState: 'running', runtimeActions: ['start', 'stop', 'restart'], runtimeControls: { deploy: '', start: '', stop: '', restart: '' },
  status: { word: 'working', reason: '', stale: false, servingRelease: 'Running release 7', servingReleaseNumber: 7 }, observation: 'running', running: true,
  servingRelease: { number: 7, releasedAt: at }, target: { kind: 'docker-host', name: 'docker-1', displayLabel: 'docker-1' }, ...more,
}) as unknown as DeploymentView
const part = (name: string, presence = 'running', more = {}) => ({ part: name, public: name === 'web', presence, stale: false, facts: { id: name, status: presence, running: presence === 'running', oomKilled: false, restarts: 0, health: 'not configured' }, ...more })
const share = (more = {}) => ({ readAt: at, target: 'docker-1', elastic: false, hostMemory: fig(8 * 2 ** 30), limits: fig(2 ** 30), declared: fig(2 ** 30), used: fig(2 ** 28), overcommitted: false, lowDisk: false, diskRefuses: false, ...more })
const copy = { backup: 'b', takenAt: at, bytes: 4096, sha256: '' }

function render(e: DeploymentView, { releases = [], parts = [part('web'), part('postgres')], diagnostics = {}, hostShare = share(), protection = {}, refreshFailed = false, failedReads = [], firstFailedReads = [], releasesFailure = false }: { releasesFailure?: boolean; firstFailedReads?: string[]; failedReads?: string[]; releases?: Partial<ReleaseListed>[]; parts?: object[]; diagnostics?: object; hostShare?: object; protection?: object; refreshFailed?: boolean } = {}) {
  const client = newQueryClient()
  client.setQueryData(getKey('/features'), enabledFeatures)
  client.setQueryData(getKey(`${base}/resources`), { reading, recorded: true, parts: (parts as { part: string }[]).map(p => ({ part: p.part, public: p.part === 'web', memory: '256Mi', cpu: 'no limit', overrideApplied: false, overrideLabel: 'unknown', observed: { part: p.part, usedBytes: 2 ** 26, limitBytes: 2 ** 28, cpuCores: 0, cpuLimit: 0, known: true } })), limits: [], limitsKnown: true, limitsSource: '', share: hostShare })
  client.setQueryData(getKey(`${base}/diagnostics`), { reading, generation: 4, containers: true, parts, events: [], eventsKnown: true, ...diagnostics })
  client.setQueryData(getKey(`${base}/protection`), { reading, items: [{ need: 'postgres', where: '', copy: 'fresh', copyLabel: 'fresh', lastCopy: copy }], recent: [], restoredCopies: [], coverage: { assessed: [], notAssessed: [] }, ...protection })
  for (const suffix of failedReads) client.getQueryCache().find({ queryKey: getKey(`${base}/${suffix}`) })!.setState({ status: 'error', error: new Error(`${suffix} offline`), fetchStatus: 'idle' })
  for (const suffix of firstFailedReads) {
    client.setDefaultOptions({ queries: { ...client.getDefaultOptions().queries, retryOnMount: false } })
    client.getQueryCache().find({ queryKey: getKey(`${base}/${suffix}`) })!.setState({ data: undefined, status: 'error', error: new Error(`${suffix} offline`), fetchStatus: 'idle' })
  }
  const read = { data: releasesFailure ? null : releases, err: releasesFailure ? 'releases offline' : '', reason: releasesFailure ? 'releases offline' : '', current: !releasesFailure, loading: false, refreshFailed: false } as unknown as Read<ReleaseListed[]>
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter><DeploymentStatus slug="lunch" env={e} refreshFailed={refreshFailed} releases={read} current reload={async () => {}} onChoose={() => {}} /></MemoryRouter></QueryClientProvider>)
  client.clear()
  return el
}
const rows = (el: Element) => [...el.querySelectorAll('.deployment-status-rows > .list-row')].map(r => r.textContent ?? '')
const facts = (el: Element) => Object.fromEntries([...el.querySelectorAll('.deployment-status-facts .deploy-line')].map(l => [l.children[0].textContent, l.children[1].textContent]))
const chip = (el: Element, n: number) => el.querySelectorAll('.deployment-status-rows > .list-row')[n].querySelector('.chip')?.textContent

it('leads with what runs when nothing is wrong, and says it once', () => {
  const el = render(env())
  expect(rows(el)).toHaveLength(1)
  expect(rows(el)[0]).toContain('Release 7')
  expect(rows(el)[0]).toContain('since')
  // The lead row said what runs, so the facts do not say it again.
  expect(Object.keys(facts(el))).toEqual(['Address', 'Containers', 'Host', 'Backups'])
})

// A failure leads; what runs then moves to the facts, as the owner states
// it. Host and backups problems replace their facts.
it('puts each problem in a row, a failure first, and moves what runs to the facts', () => {
  const el = render(env({ status: { word: 'working', reason: '', stale: false, servingRelease: 'Running release 7', servingReleaseNumber: 7 } }), {
    releases: [{ number: 8, deployment: 'main', state: 'failed', failedStage: 'building', failure: 'npm ci failed', createdAt: at, changes: [] }],
    hostShare: share({ overcommitted: true, limits: fig(10 * 2 ** 30) }),
    protection: { items: [{ need: 'postgres', where: '', copy: 'stale', copyLabel: 'overdue', lastCopy: copy }] },
  })
  expect(chip(el, 0)).toContain('failed at Build')
  expect(rows(el)[0]).toContain('npm ci failed')
  expect(rows(el)[0]).toContain('Running release 7')
  expect(rows(el).some(r => r.includes('Backups') && r.includes('overdue'))).toBe(true)
  expect(rows(el).some(r => r.includes('docker-1') && r.includes('over-committed') && r.includes('Memory limits 10 GB, host 8 GB.'))).toBe(true)
  const f = facts(el)
  // What runs is said once, as the release and when it went live.
  expect(f.Runs).toMatch(/^release 7 · /)
  expect(el.querySelector('.deployment-status-facts a[href="/projects/lunch/releases/7"]')?.textContent).toBe('release 7')
  expect(f.Host).toBeUndefined()
  expect(f.Backups).toBeUndefined()
})

// What runs is the owner's answer, never inferred from release history:
// unknown stays unknown while a deploy is on its way and after one failed,
// and a stopped deployment keeps its recorded release.
it.each(['releasing', 'failed'] as const)('keeps an unknown serving release unknown while the latest release is %s', state => {
  const el = render(env({ status: { word: 'unknown', reason: 'host did not answer', stale: false, servingRelease: 'Serving release unknown' }, servingRelease: { number: 7, releasedAt: at } as DeploymentView['servingRelease'] }), {
    releases: [{ number: 8, deployment: 'main', state, stage: 'building', failedStage: state === 'failed' ? 'building' : undefined, failure: state === 'failed' ? 'npm ci failed' : '', createdAt: at, changes: [] }, { number: 7, deployment: 'main', state: 'released', createdAt: at, changes: [] }],
  })
  const text = el.textContent ?? ''
  expect(text).not.toMatch(/Running release 7|still runs|Nothing runs/)
  expect(text).toContain('Serving release unknown')
})
it('keeps a stopped deployment\'s recorded release and its link, even beyond the history read', () => {
  const el = render(env({ runtimeState: 'stopped', runtimeActor: 'sam', running: false, observation: 'stopped', status: { word: 'stopped', reason: 'stopped by sam', stale: false, servingRelease: 'Recorded release 3', servingReleaseNumber: 3 } }), { parts: [part('web', 'stopped'), part('postgres', 'stopped')] })
  expect(el.querySelector('a[href="/projects/lunch/releases/3"]')?.textContent).toBe('Release 3')
  expect(rows(el)[0]).toContain('stopped by sam')
  // A stop a person asked for is not a failure.
  expect(rows(el)).toHaveLength(1)
  expect(el.innerHTML).not.toContain('chip fail')
})

// Diagnostics that did not answer are unknown, never failed, and make no row.
it.each([['a part the host did not answer for', { parts: [part('web', 'unknown', { reason: 'it did not answer in time' })] }], ['a diagnostics read the owner calls unknown', { diagnostics: { reading: { ...reading, unknown: true } } }]])('shows %s as unknown, never failed', (_, opts) => {
  const el = render(env(), opts as Parameters<typeof render>[1])
  expect(el.querySelector('.deployment-containers-table')?.innerHTML).toMatch(/class="chip[^"]* unknown"/)
  expect(el.querySelector('.deployment-containers-table')?.innerHTML).not.toContain('chip fail')
  expect(rows(el)).toHaveLength(1)
})

// A container that failed by itself is a row, with what to do.
it('makes a row of a container that failed by itself', () => {
  const el = render(env(), { parts: [part('web', 'stopped', { facts: { id: 'w', status: 'exited', running: false, exitCode: 137, oomKilled: true, restarts: 2, health: '' } }), part('postgres')] })
  // The chip says what happened; the exit code is the container page's.
  expect(chip(el, 0)).toBe('● killed: out of memory')
  expect(rows(el)[0]).toContain('Raise its memory or read its log.')
  const row = el.querySelectorAll('.deployment-status-rows > .list-row')[0]
  expect([...row.querySelectorAll('.list-identity a')].map(a => [a.textContent, a.getAttribute('href')])).toEqual([['web', '/projects/lunch/deployments/main/containers/web'], ['memory', '/projects/lunch/deployments/main?tab=settings'], ['log', '/projects/lunch/deployments/main/containers/web']])
  expect(facts(el).Runs).toMatch(/^release 7 · /)
})

// A host that cannot be read is unknown, said as a row: never left out as if fine.
it('says an unread host is unknown', () => {
  const el = render(env(), { hostShare: share({ readAt: undefined }) })
  expect(rows(el).some(r => r.includes('docker-1') && r.includes('unknown') && r.includes('disk and memory could not be read'))).toBe(true)
  expect(facts(el).Host).toBeUndefined()
})

// A browser's failed refresh is the page's stale note, never a claim that
// the host did not answer.
it('does not turn a failed browser refresh into a host row', () => {
  const el = render(env({ status: { word: 'unknown', reason: 'The status could not be refreshed: offline', stale: true, servingRelease: 'Serving release unknown' } }), { refreshFailed: true })
  expect(rows(el)[0]).not.toContain('docker-1')
})

// The label-and-value line's trailer column is as wide as what it holds,
// and an empty one is not drawn: the rule is the shared grid's, not a page's.
it('shrinks an empty trailer in the shared label-and-value grid', () => {
  const lineCss = readFileSync('src/overview-line.css', 'utf8') as string
  expect(lineCss).toMatch(/\.deploy-entry \.deploy-line \{[^}]*grid-template-columns: 9rem minmax\(0, 1fr\) auto;/)
  expect(lineCss).toMatch(/\.deploy-entry \.deploy-line > \.deploy-trailer:empty \{ display: none; \}/)
})

// Match the phone CSS by excluding the card it hides. No prior answer
// exists, so no StaleNote can register a global stale notice.
it('shows a failed first diagnostics read on phones', () => {
  const el = render(env(), { firstFailedReads: ['diagnostics'] })
  el.querySelectorAll('.deployment-wide-only').forEach(node => node.remove())
  expect(el.textContent).toContain('unknown')
})
it('marks a failed backup refresh as a stale answer', () => {
  const el = render(env(), { failedReads: ['protection'] })
  expect(el.querySelector('.stale-note')).not.toBeNull()
})

it('says unknown when the first release read failed', () => {
  const el = render(env(), { releasesFailure: true })
  expect(el.textContent).toContain('unknown')
})
