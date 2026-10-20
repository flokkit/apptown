import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../../data'
import type { DeploymentResources, ProjectSecrets } from '../../../wire.gen'
import { ResourceLines } from './Deployments'

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

describe("a deployment's resources", () => {
  it('shows App Platform declarations with the size they run on, never as no limit', () => {
    const html = render({ parts: [{ part: 'web', public: true, memory: '768M declared', cpu: 'not declared', runsOn: 'apps-s-1vcpu-1gb-fixed (1G, 1 CPU)', overrideApplied: false, observed: { part: 'web', usedBytes: 0, limitBytes: 0, known: false, reason: 'its target does not report memory use to AppTown' } }] })
    expect(html).toContain('768M declared')
    expect(html).toContain('apps-s-1vcpu-1gb-fixed (1G, 1 CPU)')
    expect(html).not.toContain('no limit')
    expect(html).toContain('unknown')
    expect(html).not.toMatch(/>0 ?B</)
  })
  it('says no limit where a Docker host part declares nothing, and what it uses', () => {
    const html = render({ parts: [{ part: 'worker', public: false, memory: 'no limit', cpu: 'no limit', overrideApplied: false, observed: { part: 'worker', usedBytes: 200 * 1024 * 1024, limitBytes: 0, known: true } }] })
    expect(html).toContain('no limit')
    expect(html).toContain('200')
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
    const html = render({ parts: [{ part: 'postgres', public: false, need: true, size: 'small', memory: '256M', cpu: 'no limit', overrideApplied: false, observed: { part: 'postgres', need: true, usedBytes: 31 * 1024 * 1024, limitBytes: 256 * 1024 * 1024, known: true } }] })
    expect(html).toContain('postgres')
    expect(html).toContain('(service)')
    expect(html).toContain('size small')
    expect(html).toContain('256M')
  })
  it('says what it holds of a fixed host, and when the host is overcommitted or short of disk', () => {
    const fig = (value: number) => ({ known: true, value })
    const html = render({ share: { target: 'lab', elastic: false, hostMemory: fig(8 * 2 ** 30), declared: fig(1.5 * 2 ** 30), used: fig(640 * 2 ** 20), overcommitted: true, lowDisk: true, diskRefuses: false } })
    expect(html).toContain('(19%) declared')
    expect(html).toContain("lab&#x27;s")
    expect(html).toContain('host overcommitted')
    expect(html).toContain('host disk low')
    const refused = render({ share: { target: 'lab', elastic: false, hostMemory: fig(8 * 2 ** 30), declared: fig(2 ** 30), used: fig(2 ** 20), overcommitted: false, lowDisk: true, diskRefuses: true } })
    expect(refused).toContain('host short of disk: deploys refused')
    expect(refused).not.toContain('host disk low')
  })
  it('never makes up a share it cannot know, and says an elastic target grows', () => {
    const unknownShare = render({ share: { target: 'lab', elastic: false, hostMemory: { known: true, value: 2 ** 30 }, declared: { known: false, value: 0, reason: 'a part or need has no limit' }, used: { known: false, value: 0 }, overcommitted: false, lowDisk: false, diskRefuses: false } })
    expect(unknownShare).toContain('an unknown share')
    expect(unknownShare).not.toContain('(0%)')
    expect(render({ share: { target: 'do-syd', elastic: true, hostMemory: { known: false, value: 0 }, declared: { known: false, value: 0 }, used: { known: false, value: 0 }, overcommitted: false, lowDisk: false, diskRefuses: false } })).toContain('grows on demand')
  })
  it('says what happened at the limits, and never reads an unreported target as nothing happened', () => {
    const html = render({ limits: [{ part: 'web', kind: 'out-of-memory', count: 3, last: '2026-10-13T02:00:00Z' }, { part: 'worker', kind: 'exited', count: 1, last: '2026-10-13T01:00:00Z' }] })
    expect(html).toContain('killed: out of memory, 3 times in the last day')
    expect(html).toContain('exited, once in the last day')
    expect(html).not.toContain('restarted')
    expect(render({})).toContain('no out-of-memory kills or exits recorded in the last day')
    const unreported = render({ limitsKnown: false, limitsSource: 'App Platform does not report them to AppTown' })
    expect(unreported).toContain('App Platform does not report them to AppTown')
    expect(unreported).not.toContain('no out-of-memory kills')
  })
  it('shows the recorded default source and explicit unlimited act separately', () => {
    const html = render({ parts: [
      { part: 'web', public: true, memory: '160M', cpu: 'no limit', memoryDefault: { kind: 'pack', name: 'django-htmx', version: 3 }, overrideApplied: false, observed: { part: 'web', usedBytes: 0, limitBytes: 0, known: false, reason: 'not observed' } },
      { part: 'worker', public: false, memory: 'no limit', cpu: 'no limit', memoryUnlimited: true, overrideApplied: true, override: { part: 'worker', resources: {}, memoryUnlimited: true, reason: 'measured allowance', setBy: 'alice', setAt: '2026-10-14T00:00:00Z' }, observed: { part: 'worker', usedBytes: 0, limitBytes: 0, known: false, reason: 'not observed' } },
    ] })
    expect(html).toContain('pack django-htmx v3')
    expect(html).toContain('default')
    expect(html).toContain('explicit no limit')
    expect(html).toContain('measured allowance')
    expect(html).toContain('alice')
    expect(html).toContain('unknown')
  })

})

// AP-C01: the stored kind stays forge; the UI names its source AppTown.
it('names the product that supplied the memory default', () => {
  const html = render({ parts: [{ part: 'web', public: true, memory: '160M', cpu: 'no limit', memoryDefault: { kind: 'forge' }, overrideApplied: false, observed: { part: 'web', usedBytes: 0, limitBytes: 0, known: false, reason: 'not observed' } }] })
  expect(html).toContain('AppTown default')
})
