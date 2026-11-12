import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it } from 'vitest'
import { getKey, newQueryClient } from '../../data'
import { DeploymentContainers } from './DeploymentContainers'
import type { DeploymentDiagnostics, DeploymentResources, PartMemory } from '../../wire.gen'

const base = '/projects/shop/deployments/main'
const part = (part: string, observed: PartMemory) => ({ part, public: part === 'web', overrideLabel: 'unknown', memory: '', cpu: '', overrideApplied: false, observed })
const diag = (part: string) => ({ part, presence: 'running', stale: false, facts: { id: part, status: 'running', running: true, oomKilled: false, restarts: 0, health: '', startedAt: '2026-11-08T01:00:00Z' } })

function render(parts: ReturnType<typeof part>[], more: Partial<DeploymentResources> = {}, opts: { stopped?: boolean; presence?: string } = {}) {
  const client = newQueryClient()
  client.setQueryData(getKey(`${base}/resources`), { reading: { stale: false }, parts, limits: [], limitsKnown: true, limitsSource: 'the Docker daemon', ...more } as unknown as DeploymentResources)
  client.setQueryData(getKey(`${base}/diagnostics`), { reading: { stale: false }, containers: true, parts: parts.map(p => ({ ...diag(p.part), ...(opts.presence ? { presence: opts.presence, facts: { ...diag(p.part).facts, running: false, exitCode: 0 } } : {}) })) } as unknown as DeploymentDiagnostics)
  const html = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter><DeploymentContainers slug="shop" name="main" stopped={opts.stopped} /></MemoryRouter></QueryClientProvider>)
  client.clear()
  return html
}

// Each container shows its image and what it uses against its limits; the
// totals add the parts up, and a part with no CPU limit says so rather
// than a bar against nothing.
it('shows each container with its use and the deployment totals', () => {
  const html = render([
    part('web', { part: 'web', usedBytes: 64 << 20, limitBytes: 128 << 20, cpuCores: 0.25, cpuLimit: 1, image: 'registry.local/shop:4', known: true }),
    part('worker', { part: 'worker', usedBytes: 32 << 20, limitBytes: 128 << 20, cpuCores: 0.5, cpuLimit: 0, image: 'sha256:0123456789abcdef0123', known: true }),
  ])
  expect(html).toContain('registry.local/shop:4')
  expect(html).toContain('>0123456789ab<')
  expect(html).toContain('0.75 cores · no limit')
  expect(html).toMatch(/>0\.5(<!-- -->)?<span class="muted"> · no limit/)
  // A quarter of a core used out of one: the meter reads 0.25 of 1.
  expect(html).toMatch(/<meter[^>]*aria-label="web CPU use"[^>]*max="1"[^>]*value="0.25"/)
})

// Fly sizes Machines in shared CPUs and says so; a Docker container's CPUs
// carry no kind. Tiny real use reads as under 0.01, not idle.
// Kills: the kind dropped, or the kind shown where the provider gave none.
it('labels shared CPUs where the provider names them', () => {
  const fly = render([
    part('web', { part: 'web', usedBytes: 18 << 20, limitBytes: 256 << 20, cpuCores: 0.002, cpuLimit: 1, cpuKind: 'shared', known: true }),
    part('api', { part: 'api', usedBytes: 64 << 20, limitBytes: 256 << 20, cpuCores: 0.01, cpuLimit: 1, cpuKind: 'shared', known: true }),
  ])
  expect(fly).toMatch(/>&lt;0\.01(<!-- -->)?<span class="muted"> \/ 1(<!-- -->)? shared/)
  expect(fly).toContain('0.01 / 2 shared CPUs')
  const docker = render([part('web', { part: 'web', usedBytes: 18 << 20, limitBytes: 64 << 20, cpuCores: 0.002, cpuLimit: 1, known: true })])
  expect(docker).not.toContain('shared')
  expect(docker).toContain('&lt;0.01 cores / 1 cores')
})

// A use the host did not report is unknown, in the row and in the totals,
// never a zero that would read as idle.
it('keeps an unreported use unknown, never zero', () => {
  const html = render([
    part('web', { part: 'web', usedBytes: 64 << 20, limitBytes: 128 << 20, cpuCores: 0.25, cpuLimit: 1, known: true }),
    part('postgres', { part: 'postgres', usedBytes: 0, limitBytes: 0, cpuCores: 0, cpuLimit: 0, known: false, reason: 'the daemon did not report its container' }),
  ])
  expect(html).toContain('the daemon did not report its container')
  expect(html).not.toContain('0 cores')
  expect(html.match(/>unknown</g)?.length).toBeGreaterThanOrEqual(4)
})

// What happened at the limits over the last day is said under the
// containers; a target that does not report it is never read as nothing.
it('says what happened at the limits, and never reads an unreported target as nothing happened', () => {
  const web = part('web', { part: 'web', usedBytes: 1, limitBytes: 2, cpuCores: 0, cpuLimit: 0, known: true })
  const html = render([web], { limits: [{ part: 'web', kind: 'out-of-memory', count: 3, last: '2026-10-13T02:00:00Z' }, { part: 'worker', kind: 'exited', count: 1, last: '2026-10-13T01:00:00Z' }] })
  expect(html).toContain('killed: out of memory, 3 times in the last day')
  expect(html).toContain('exited, once in the last day')
  expect(render([web])).toContain('no out-of-memory kills or exits recorded in the last day')
  const unreported = render([web], { limitsKnown: false, limitsSource: 'App Platform does not report them to AppTown' })
  expect(unreported).toContain('Limit events were not reported')
  expect(unreported).not.toContain('no out-of-memory kills')
})

// A part stopped because a person stopped the deployment reads stopped; the
// same part stopped on its own is a failure with what to do about it.
it.each([true, false])('reads a stopped part as stopped only when a person stopped the deployment: %s', stopped => {
  const web = part('web', { part: 'web', usedBytes: 0, limitBytes: 0, cpuCores: 0, cpuLimit: 0, known: false, reason: 'not running' })
  const html = render([web], {}, { stopped, presence: 'stopped' })
  expect(html.includes('read its log to see why')).toBe(!stopped)
  expect(html.replace(/<[^>]+>/g, '')).toContain(stopped ? 'stopped' : 'failed')
  expect(html.includes('failed')).toBe(!stopped)
})

// The app's own container has no part name (''): before any log is read its
// row offers Logs, never "reading…" (review R2, 8 Nov).
it('offers logs for the unnamed app container before any log request', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/shop/deployments/main/diagnostics'), {
    reading: {}, containers: true, parts: [{ part: '', presence: 'running', facts: { running: true, restarts: 0 } }],
  })
  client.setQueryData(getKey('/projects/shop/deployments/main/resources'), { reading: {}, parts: [], limits: [], limitsKnown: true })
  try {
    const html = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter><DeploymentContainers slug="shop" name="main" /></MemoryRouter></QueryClientProvider>)
    expect(html).toContain('More actions for app')
    expect(html).not.toContain('reading…')
  } finally { client.clear() }
})

// A bucket website runs no containers: CPU, memory, image and start time
// are not applicable, so they are left out rather than shown unknown, and
// its part reads what its address was seen serving. An address answering
// another release is a failure; no current answer stays unknown.
it.each([['serving', 'working'], ['not serving', 'failed'], ['unknown', 'unknown']])('shows a bucket website part %s as %s, with no container facts', (presence, word) => {
  const client = newQueryClient()
  client.setQueryData(getKey(`${base}/resources`), { reading: { stale: false }, parts: [part('catalog', { part: 'catalog', usedBytes: 0, limitBytes: 0, cpuCores: 0, cpuLimit: 0, known: false, reason: 'its target does not report memory use to AppTown' })], limits: [], limitsKnown: false, limitsSource: 'this target runs no containers' } as unknown as DeploymentResources)
  client.setQueryData(getKey(`${base}/diagnostics`), { reading: {}, containers: false, noContainers: true, parts: [{ part: 'catalog', public: true, presence, stale: false, reason: presence === 'serving' ? undefined : 'its address answers, but not with this release' }] } as unknown as DeploymentDiagnostics)
  const html = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter><DeploymentContainers slug="shop" name="main" /></MemoryRouter></QueryClientProvider>)
  client.clear()
  const text = html.replace(/<[^>]+>/g, ' ')
  expect(text).toContain('catalog')
  expect(text).toContain(word)
  for (const absent of ['CPU', 'MEMORY', 'Memory', 'Image', 'Started', 'does not report', 'Limit events']) expect(text).not.toContain(absent)
  if (presence === 'serving') expect(text).not.toContain('unknown')
})
