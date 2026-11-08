import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../../data'
import type { DeploymentResources, ProjectSecrets } from '../../../wire.gen'
import { ResourceLines, ShareLine } from './Deployments'

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
// What the host says about the deployment's share: the Status tab shows it
// under the containers, from the same resources read.
const share = (s: NonNullable<DeploymentResources['share']>) => renderToStaticMarkup(<QueryClientProvider client={newQueryClient()}><MemoryRouter><ShareLine share={s} /></MemoryRouter></QueryClientProvider>)

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
  it('says what it holds of a fixed host, and when the host is overcommitted or short of disk', () => {
    const fig = (value: number) => ({ known: true, value })
    const html = share({ target: 'lab', elastic: false, hostMemory: fig(8 * 2 ** 30), requested: fig(10 * 2 ** 30), declared: fig(1.5 * 2 ** 30), used: fig(640 * 2 ** 20), overcommitted: true, lowDisk: true, diskRefuses: false })
    expect(html).not.toContain('(19%) declared')
    expect(html).toContain('href="/targets/lab"')
    expect(html).toContain('10 GB requested; host total 8 GB')
    expect(html).toContain('Server is low on disk space.')
    const refused = share({ target: 'lab', elastic: false, hostMemory: fig(8 * 2 ** 30), declared: fig(2 ** 30), used: fig(2 ** 20), overcommitted: false, lowDisk: true, diskRefuses: true })
    expect(refused).toContain('New deployments are blocked.')
    expect(refused).not.toContain('Server is low on disk space.')
  })
  it('never makes up a share it cannot know, and says an elastic target grows', () => {
    const unknownShare = share({ target: 'lab', elastic: false, hostMemory: { known: true, value: 2 ** 30 }, declared: { known: false, value: 0, reason: 'a part or need has no limit' }, used: { known: false, value: 0 }, overcommitted: false, lowDisk: false, diskRefuses: false })
    expect(unknownShare).not.toContain('an unknown share')
    expect(unknownShare).not.toContain('(0%)')
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

it('keeps a host pending until its first reading and reports later staleness', () => {
  const host = { target: 'lab', elastic: false, hostMemory: { known: false, value: 0 }, declared: { known: false, value: 0 }, used: { known: false, value: 0 }, overcommitted: false, lowDisk: false, diskRefuses: false, stale: true }
  const pending = share(host)
  expect(pending).not.toContain('The host reading is out of date.')
  expect(pending).toContain('Waiting for the first host check.')
  expect(share({ ...host, readAt: '2026-10-10T01:00:00Z' })).toContain('The host reading is out of date.')
  expect(share({ ...host, error: 'Host unavailable' })).toContain('Host unavailable')
})
