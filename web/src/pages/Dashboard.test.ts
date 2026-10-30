import { describe, expect, it } from 'vitest'
import { homeFinished, homeSections, homeStatus, type Activity } from './Dashboard'

it.each([true, false])('summarizes counted host issues once when coverage is known: %s', known => {
  expect(homeStatus({ operations: { count: 8, known }, needsYou: 3, running: 0, deployments: [] }))
    .toBe('3 needs you · 8 host issues')
})

type Dep = Activity['deployments'][number]
const dep = (project: string, over: Partial<Dep> = {}): Dep => ({
  project, name: 'main', url: 'http://x', running: true,
  observation: 'running', targetName: 'Club host', observedAt: '2026-10-03T00:00:00Z', observationStale: false, ...over,
})

describe('the home status sentence', () => {
  it('says all quiet only when needs-you and running were read and are empty', () => {
    expect(homeStatus({ operations: { count: 0, known: true }, needsYou: 0, running: 0, deployments: [] })).toMatch(/^All quiet/)
    expect(homeStatus({ operations: { count: 0, known: true }, needsYou: null, running: 0, deployments: [] })).not.toMatch(/quiet/i)
    expect(homeStatus({ operations: { count: 0, known: true }, needsYou: 0, running: 1, deployments: [] })).not.toMatch(/quiet/i)
  })

  it('never turns an unread needs-you count into zero', () => {
    const s = homeStatus({ operations: { count: 0, known: true }, needsYou: null, running: 2, deployments: [] })
    expect(s).not.toMatch(/\b0\b/)
    expect(s).toMatch(/needs you/)
  })

  it('counts an app live once, and only on a current running observation', () => {
    const s = homeStatus({ operations: { count: 0, known: true }, needsYou: 2, running: 0, deployments: [
      dep('a'), dep('a', { name: 'staging' }), dep('b'),
      dep('c', { observationStale: true }), dep('d', { running: null, observation: 'unknown' }),
      dep('e', { running: false, observation: 'stopped' }),
    ] })
    expect(s).toMatch(/^2 needs you/)
    expect(s).toMatch(/\b2 apps live/)
    // c is stale and d unknown: neither live nor stopped.
    expect(s).toMatch(/\b2 apps status unknown/)
  })
})

describe('the home sections', () => {
  // Record ids are UUIDv7 strings, made here from a small number.
  const id = (n: number) => `00000000-${String(n).padStart(4, '0')}-7000-8000-000000000000`
  const run = (runId: number, changeId: number, state: string) =>
    ({ runId: id(runId), changeId: id(changeId), state, workflow: 'w', title: 't', status: 'running', stage: 's', project: 'p', durationS: 1 }) as unknown as Parameters<typeof homeSections>[0][number]

  it('lists a blocked run under needs you, not running', () => {
    const s = homeSections([run(1, 10, 'blocked'), run(2, 11, 'agent working')], [{ changeId: id(12) }])
    expect(s.blocked.map((r) => r.runId)).toEqual([id(1)])
    expect(s.happening.map((r) => r.runId)).toEqual([id(2)])
    expect(s.needsYouCount).toBe(2)
  })

  it('counts a change once however many things on it need you', () => {
    const s = homeSections([run(1, 10, 'blocked')], [{ changeId: id(10) }, { changeId: id(10) }, { changeId: id(12) }, { changeId: id(12) }])
    expect(s.needsYouCount).toBe(2)
    // The blocked change shows as its blocked run alone.
    expect(s.waiting.map((a) => a.changeId)).toEqual([id(12), id(12)])
  })

  // The sentence above "Needs you" counts what the list holds: a failed
  // operation waiting on the person is not "All quiet".
  it('counts an operation that waits on the person, in the list and the sentence', () => {
    const s = homeSections([], [], [{ id: id(20), state: 'failed' }])
    expect(s.needsYouCount).toBe(1)
    const sentence = homeStatus({ operations: { count: 0, known: true }, needsYou: s.needsYouCount, running: 0, deployments: [dep('a')] })
    expect(sentence).toBe('1 needs you · 1 app live')
  })

  it('does not ask for an approval on a change whose run is working again', () => {
    const s = homeSections([run(1, 10, 'working')], [{ changeId: id(10) }])
    expect(s.waiting).toEqual([])
    expect(s.needsYouCount).toBe(0)
  })
})

it('keeps recently finished to landed and failed when the stop actor is unavailable', () => {
  const history = ['landed', 'failed', 'stopped', 'finished', 'unknown'].map(statusLabel => ({
    statusLabel, title: statusLabel === 'stopped' ? 'Release check: a chat answers' : statusLabel,
  })) as Activity['history']
  expect(homeFinished(history).map(c => c.statusLabel)).toEqual(['landed', 'failed'])
})
