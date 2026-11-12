import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../../data'
import type { DeploymentResources, ProjectSecrets } from '../../../wire.gen'
import { ResourceLines, HostLine } from './Deployments'

// A deployment's card says what each part may use as the target holds it,
// what it uses now, and which profile it runs and will run: no limit only
// where nothing is declared and the target holds declarations exactly,
// unknown never as zero, and the choice offered among both files' names.
const render = (res: Partial<DeploymentResources>, choice?: Partial<ProjectSecrets>) => {
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/game/deployments/cloud/resources'), { recorded: true, profile: 'default', how: 'default', parts: [], limits: [], limitsKnown: true, limitsSource: "recorded from the Docker host's events", ...res })
  client.setQueryData(getKey('/projects/game/secrets'), { declared: false, spec: 'secretspec.toml', stores: [], deployments: [], choosable: ['default'], ...choice })
  return renderToStaticMarkup(
    <QueryClientProvider client={client}><MemoryRouter><ResourceLines slug="game" name="cloud" /></MemoryRouter></QueryClientProvider>)
}
// The deployment's Host line: always present, so the page keeps its shape.
const share = (s?: NonNullable<DeploymentResources['share']>) => renderToStaticMarkup(<QueryClientProvider client={newQueryClient()}><MemoryRouter><HostLine share={s} target="lab" /></MemoryRouter></QueryClientProvider>)

describe("a deployment's resources", () => {
  it('shows App Platform declarations with the size they run on, never as no limit', () => {
    const html = render({ parts: [{ part: 'web', public: true, memory: '768M declared', cpu: 'not declared', runsOn: 'apps-s-1vcpu-1gb-fixed (1G, 1 CPU)', overrideApplied: false, overrideLabel: 'unknown', observed: { part: 'web', usedBytes: 0, limitBytes: 0, cpuCores: 0, cpuLimit: 0, known: false, reason: 'its target does not report memory use to AppTown' } }] })
    expect(html).toContain('768M declared')
    expect(html).toContain('apps-s-1vcpu-1gb-fixed (1G, 1 CPU)')
    expect(html).not.toContain('no limit')
    expect(html).not.toMatch(/>0 ?B</)
  })
  it('says no limit where a Docker host part declares nothing', () => {
    const html = render({ parts: [{ part: 'worker', public: false, memory: 'no limit', cpu: 'no limit', overrideApplied: false, overrideLabel: 'unknown', observed: { part: 'worker', usedBytes: 200 * 1024 * 1024, limitBytes: 0, cpuCores: 0, cpuLimit: 0, known: true } }] })
    expect(html).toContain('no limit')
  })
  it('offers every profile either file declares, and says when a new choice waits for the next deploy', () => {
    const html = render({ profile: 'default' }, { choosable: ['default', 'production'], deployments: [{ name: 'cloud', chosen: 'production', profile: 'default', secrets: [], missing: [] }] })
    expect(html).toContain('<option value="production" selected="">production</option>')
    expect(html).toContain('next: production')
  })
  it('says not recorded for a generation from before the record was kept', () => {
    expect(render({ recorded: false, profile: '' })).toContain('not recorded')
  })
  it("shows a need's service as a row of its own", () => {
    const html = render({ parts: [{ part: 'postgres', public: false, need: true, size: 'small', memory: '256M', cpu: 'no limit', overrideApplied: false, overrideLabel: 'unknown', observed: { part: 'postgres', need: true, usedBytes: 31 * 1024 * 1024, limitBytes: 256 * 1024 * 1024, cpuCores: 0, cpuLimit: 0, known: true } }] })
    expect(html).toContain('postgres')
    expect(html).toMatch(/postgres<\/strong><span class="muted small"> service/)
    expect(html).toContain('size small')
    expect(html).toContain('256M')
  })
  it('says whether the host takes new deploys and what its memory limits add up to against its memory', () => {
    const fig = (value: number) => ({ known: true, value })
    const read = { readAt: '2026-11-09T01:00:00Z', target: 'lab', elastic: false, declared: fig(2 ** 30), used: fig(2 ** 20) }
    const html = share({ ...read, hostMemory: fig(8 * 2 ** 30), limits: fig(10 * 2 ** 30), overcommitted: true, lowDisk: true, diskRefuses: false })
    expect(html).toContain('href="/targets/lab"')
    expect(html).toContain('ready for deploys · low on disk')
    expect(html).toContain('memory limits add up to 10 GB of 8 GB')
    const refused = share({ ...read, hostMemory: fig(8 * 2 ** 30), overcommitted: false, lowDisk: true, diskRefuses: true })
    expect(refused).toContain('new deploys blocked: low on disk')
    expect(share({ ...read, hostMemory: fig(8 * 2 ** 30), overcommitted: false, lowDisk: false, diskRefuses: false })).toContain('lab · ready for deploys')
    expect(share({ target: 'do-syd', elastic: true, hostMemory: { known: false, value: 0 }, declared: { known: false, value: 0 }, used: { known: false, value: 0 }, overcommitted: false, lowDisk: false, diskRefuses: false })).toContain('grows on demand')
  })
  it('shows the recorded default source and explicit unlimited act separately', () => {
    const html = render({ parts: [
      { part: 'web', public: true, memory: '512Mi', cpu: 'no limit', memoryDefault: { kind: 'apptown' }, overrideApplied: false, overrideLabel: 'unknown', observed: { part: 'web', usedBytes: 0, limitBytes: 0, cpuCores: 0, cpuLimit: 0, known: false, reason: 'not observed' } },
      { part: 'worker', public: false, memory: 'no limit', cpu: 'no limit', memoryUnlimited: true, overrideApplied: true, overrideLabel: 'excepted', override: { part: 'worker', resources: {}, memoryUnlimited: true, reason: 'measured allowance', setBy: 'alice', setAt: '2026-10-14T00:00:00Z' }, observed: { part: 'worker', usedBytes: 0, limitBytes: 0, cpuCores: 0, cpuLimit: 0, known: false, reason: 'not observed' } },
    ] })
    expect(html).toContain('AppTown default')
    expect(html).toContain('default')
    expect(html).toContain('explicit no limit')
    expect(html).toContain('measured allowance')
    expect(html).toContain('alice')
  })

})

// AP-C01: the stored kind stays forge; the UI names its source AppTown.
it('names the product that supplied the memory default', () => {
  const html = render({ parts: [{ part: 'web', public: true, memory: '160M', cpu: 'no limit', memoryDefault: { kind: 'forge' }, overrideApplied: false, overrideLabel: 'unknown', observed: { part: 'web', usedBytes: 0, limitBytes: 0, cpuCores: 0, cpuLimit: 0, known: false, reason: 'not observed' } }] })
  expect(html).toContain('AppTown default')
})

// A host check that failed, went stale or has not run yet is the host's own
// problem, shown on its page: the line keeps its place and says unknown,
// never ready.
it('says unknown, not ready and not the host check error, when the host reading is missing, stale or failed', () => {
  const host = { target: 'lab', elastic: false, hostMemory: { known: true, value: 2 ** 30 }, requested: { known: true, value: 2 ** 29 }, declared: { known: false, value: 0 }, used: { known: false, value: 0 }, overcommitted: false, lowDisk: false, diskRefuses: false }
  for (const html of [share(undefined), share(host), share({ ...host, readAt: '2026-10-10T01:00:00Z', stale: true }), share({ ...host, readAt: '2026-10-10T01:00:00Z', error: 'The host capacity check failed.' })]) {
    expect(html).toContain('Host')
    expect(html).toContain('unknown')
    expect(html).not.toContain('ready for deploys')
    expect(html).not.toContain('capacity check failed')
    expect(html).not.toContain('requested of')
  }
})
