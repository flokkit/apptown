import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it } from 'vitest'
import { getKey, newQueryClient } from '../../data'
import { DeploymentContainerPage } from './DeploymentContainers'
import type { DeploymentDiagnostics, DeploymentResources, DeploymentView, PartMemory } from '../../wire.gen'

const base = '/projects/shop/deployments/main'
const part = (part: string, observed: PartMemory) => ({ part, public: part === 'web', overrideLabel: 'unknown', memory: '', cpu: '', overrideApplied: false, observed })
const diag = (part: string) => ({ part, presence: 'running', stale: false, facts: { id: part, status: 'running', running: true, oomKilled: false, restarts: 0, health: '', startedAt: '2026-11-08T01:00:00Z' } })
const env = (runtimeState = 'running') => ({ name: 'main', displayLabel: '', runtimeState, status: { word: 'working' } }) as unknown as DeploymentView

// One container's page, with the reads it makes already answered (or not).
function page(at: string, { resources, diagnostics, stopped = false, diagnosticsError = false }: { diagnosticsError?: boolean; resources?: unknown; diagnostics?: unknown; stopped?: boolean }) {
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/shop/deployments'), [env(stopped ? 'stopped' : 'running')])
  if (resources) client.setQueryData(getKey(`${base}/resources`), resources)
  if (diagnostics) client.setQueryData(getKey(`${base}/diagnostics`), diagnostics)
  if (diagnosticsError) client.setDefaultOptions({ queries: { ...client.getDefaultOptions().queries, retryOnMount: false } })
  if (diagnosticsError) client.getQueryCache().build(client, { queryKey: getKey(`${base}/diagnostics`) }).setState({ status: 'error', error: new Error('diagnostics offline'), fetchStatus: 'idle' })
  const html = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`${base}/containers/${at}`]}><Routes><Route path="/projects/:slug/deployments/:name/containers/:part" element={<DeploymentContainerPage />} /></Routes></MemoryRouter></QueryClientProvider>)
  client.clear()
  return html
}
function render(parts: ReturnType<typeof part>[], at: string, more: Partial<DeploymentResources> = {}, opts: { stopped?: boolean; presence?: string } = {}) {
  return page(at, {
    stopped: opts.stopped,
    resources: { reading: { stale: false }, parts, limits: [], limitsKnown: true, limitsSource: 'the Docker daemon', ...more },
    diagnostics: { reading: { stale: false }, containers: true, parts: parts.map(p => ({ ...diag(p.part), ...(opts.presence ? { presence: opts.presence, facts: { ...diag(p.part).facts, running: false, exitCode: 0 } } : {}) })) },
  })
}

// A container's page shows the image it runs and what it uses against its
// limits; a part with no CPU limit says so rather than a bar against nothing.
it('shows a container with its image and its use against its limits', () => {
  const parts = [
    part('web', { part: 'web', usedBytes: 64 << 20, limitBytes: 128 << 20, cpuCores: 0.25, cpuLimit: 1, image: 'registry.local/shop:4', known: true }),
    part('worker', { part: 'worker', usedBytes: 32 << 20, limitBytes: 128 << 20, cpuCores: 0.5, cpuLimit: 0, image: 'sha256:0123456789abcdef0123', known: true }),
  ]
  const web = render(parts, 'web')
  expect(web).toContain('registry.local/shop:4')
  // A quarter of a core used out of one: the meter reads 0.25 of 1.
  expect(web).toMatch(/<meter[^>]*aria-label="web CPU use"[^>]*max="1"[^>]*value="0.25"/)
  const worker = render(parts, 'worker')
  expect(worker).toContain('>0123456789ab<')
  expect(worker).toMatch(/>0\.5 cores(<!-- -->)?<span class="muted"> · no limit/)
})

// Fly sizes Machines in shared CPUs and says so; a Docker container's CPUs
// carry no kind. Tiny real use reads as under 0.01, not idle.
it('labels shared CPUs where the provider names them', () => {
  const fly = render([part('web', { part: 'web', usedBytes: 18 << 20, limitBytes: 256 << 20, cpuCores: 0.002, cpuLimit: 1, cpuKind: 'shared', known: true })], 'web')
  expect(fly).toMatch(/>&lt;0\.01(<!-- -->)?<span class="muted"> \/ 1(<!-- -->)? shared CPUs/)
  const docker = render([part('web', { part: 'web', usedBytes: 18 << 20, limitBytes: 64 << 20, cpuCores: 0.002, cpuLimit: 1, known: true })], 'web')
  expect(docker).not.toContain('shared')
  expect(docker).toMatch(/&lt;0\.01 cores(<!-- -->)?<span class="muted"> \/ 1 cores/)
})

// A use the host did not report is unknown, with why, never a zero that
// would read as idle.
it('keeps an unreported use unknown, never zero', () => {
  const html = render([part('postgres', { part: 'postgres', usedBytes: 0, limitBytes: 0, cpuCores: 0, cpuLimit: 0, known: false, reason: 'the daemon did not report its container' })], 'postgres')
  expect(html).toContain('the daemon did not report its container')
  expect(html).not.toContain('0 cores')
  expect(html).toContain('>unknown<')
})

// What happened at its limits over the last day is on its page; a target
// that does not report it is never read as nothing happened.
it('says what happened at the limits, and never reads an unreported target as nothing happened', () => {
  const web = part('web', { part: 'web', usedBytes: 1, limitBytes: 2, cpuCores: 0, cpuLimit: 0, known: true })
  const html = render([web], 'web', { limits: [{ part: 'web', kind: 'out-of-memory', count: 3, last: '2026-10-13T02:00:00Z' }, { part: 'worker', kind: 'exited', count: 1, last: '2026-10-13T01:00:00Z' }] })
  expect(html).toContain('killed: out of memory, 3 times in the last day')
  expect(html).not.toContain('exited, once')
  expect(render([web], 'web')).toContain('no out-of-memory kills or exits recorded in the last day')
  const unreported = render([web], 'web', { limitsKnown: false, limitsSource: 'App Platform does not report them to AppTown' })
  expect(unreported).toContain('Limit events were not reported')
  expect(unreported).not.toContain('no out-of-memory kills')
})

// A part stopped because a person stopped the deployment reads stopped; the
// same part stopped on its own is a failure with what to do about it.
it.each([true, false])('reads a stopped part as stopped only when a person stopped the deployment: %s', stopped => {
  const web = part('web', { part: 'web', usedBytes: 0, limitBytes: 0, cpuCores: 0, cpuLimit: 0, known: false, reason: 'not running' })
  const html = render([web], 'web', {}, { stopped, presence: 'stopped' })
  expect(html.replace(/<[^>]+>/g, '').includes('Read its log to see why')).toBe(!stopped)
  expect(html.includes('class="chip fail')).toBe(!stopped)
})

// The app's own container has no part name (''); its page is "app" and
// offers its log before any log is read.
it('opens the unnamed app container as "app" and offers its log', () => {
  const html = page('app', { diagnostics: { reading: {}, containers: true, parts: [{ part: '', presence: 'running', facts: { running: true, restarts: 0 } }] }, resources: { reading: {}, parts: [], limits: [], limitsKnown: true } })
  expect(html).toContain('<h1>app')
  expect(html).toContain('>Log</button>')
})

// A bucket website runs no containers: CPU, memory, image and start time
// are not applicable, so they are left out rather than shown unknown, and
// its part says what its address was seen serving.
it.each([['serving', 'working'], ['not serving', 'not serving'], ['unknown', 'unknown']])('shows a bucket website part %s as %s, with no container facts', (presence, word) => {
  const html = page('catalog', {
    resources: { reading: { stale: false }, parts: [part('catalog', { part: 'catalog', usedBytes: 0, limitBytes: 0, cpuCores: 0, cpuLimit: 0, known: false, reason: 'its target does not report memory use to AppTown' })], limits: [], limitsKnown: false, limitsSource: 'this target runs no containers' },
    diagnostics: { reading: {}, containers: false, noContainers: true, parts: [{ part: 'catalog', public: true, presence, stale: false, reason: presence === 'serving' ? undefined : 'its address answers, but not with this release' }] } as unknown as DeploymentDiagnostics,
  })
  const text = html.replace(/<[^>]+>/g, ' ')
  expect(text).toContain('catalog')
  expect(text).toContain(word)
  for (const absent of ['CPU', 'Memory', 'Image', 'Started', 'does not report', 'Limit events']) expect(text).not.toContain(absent)
})

// The page waits for both first reads: until then it says it is loading,
// rather than calling the missing half unknown.
it.each(['resources', 'diagnostics'])('says loading, not unknown, while %s is still on its way', waiting => {
  const html = page('web', {
    resources: waiting === 'resources' ? undefined : { reading: { stale: false }, parts: [part('web', { part: 'web', usedBytes: 1, limitBytes: 2, cpuCores: 0, cpuLimit: 0, image: 'i', known: true })], limits: [], limitsKnown: true },
    diagnostics: waiting === 'diagnostics' ? undefined : { reading: { stale: false }, containers: true, parts: [diag('web')] },
  })
  expect(html).toContain('Loading…')
  expect(html).not.toContain('unknown')
})

it('does not call a container absent when the diagnostics first read failed', () => {
  const html = page('db', { resources: { reading: {}, parts: [{ part: 'db', suppliedService: true }], limits: [], limitsKnown: true }, diagnosticsError: true })
  expect(html).not.toContain('has no container called')
  expect(html).toContain('unknown')
})

// Cached absence must carry the same stale qualification as cached facts.
it('marks a cached container absence stale when diagnostics refresh fails', () => {
  const html = page('db', {
    resources: { reading: {}, parts: [{ part: 'db', suppliedService: true }], limits: [], limitsKnown: true },
    diagnostics: { reading: {}, containers: true, parts: [] },
    diagnosticsError: true,
  })
  expect(html).toContain('stale-note')
})
