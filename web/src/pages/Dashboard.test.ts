import { describe, expect, it } from 'vitest'
import { homeFinished, homeStatus, type Activity } from './Dashboard'

it.each([true, false])('summarizes counted to fix once when coverage is known: %s', known => {
  expect(homeStatus({ operations: { count: 8, known }, needsYou: 3, running: 0, deployments: [] }))
    .toBe('3 waiting on you · 8 to fix')
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
    expect(s).toMatch(/waiting on you/)
  })

  it('counts a running project once, and only on a current running observation', () => {
    const s = homeStatus({ operations: { count: 0, known: true }, needsYou: 2, running: 0, deployments: [
      dep('a'), dep('a', { name: 'staging' }), dep('b'),
      dep('c', { observationStale: true }), dep('d', { running: null, observation: 'unknown' }),
      dep('e', { running: false, observation: 'stopped' }),
    ] })
    expect(s).toMatch(/^2 waiting on you/)
    expect(s).toMatch(/\b2 running/)
    // c is stale and d unknown: neither live nor stopped.
    expect(s).toMatch(/\b2 running status unknown/)
  })
})

// The feed is what changed: landed only; a failed change that needs
// something is already waiting on someone.
it('keeps the activity feed to landed changes', () => {
  const history = ['landed', 'failed', 'stopped', 'finished', 'unknown'].map(statusLabel => ({
    statusLabel, title: statusLabel === 'stopped' ? 'Release check: a chat answers' : statusLabel,
  })) as Activity['history']
  expect(homeFinished(history).map(c => c.statusLabel)).toEqual(['landed'])
})
