import { describe, expect, it } from 'vitest'
import { runtimeChip, runtimeDescription, runtimeLabel } from './runtime'

describe('runtime status', () => {
  const unobserved = { observation: 'unknown' as const, observationStale: true }
  it('keeps unknown for a deployment nobody has observed', () => {
    expect(runtimeDescription(unobserved)).toMatch(/unknown/)
    expect(runtimeLabel(unobserved)).toBe('not checked yet')
    expect(runtimeChip(unobserved)).toBe('unknown')
  })

  const now = new Date().toISOString()
  it('says only the state on a current badge, with the check time in the title', () => {
    for (const [observation, label, chip] of [['running', 'running', 'pass'], ['stopped', 'stopped', ''], ['unknown', 'status unknown', 'unknown']]) {
      const o = { observation, observedAt: now, observationStale: false }
      expect(runtimeLabel(o)).toBe(label)
      expect(runtimeChip(o)).toBe(chip)
      expect(runtimeDescription(o)).toBe(`${label} · checked just now`)
    }
  })

  it('says how old an out-of-date reading is, and never draws it as current', () => {
    const old = new Date(Date.now() - 10 * 60 * 1000).toISOString()
    const o = { observation: 'running', observedAt: old, observationStale: true, observationIssue: 'refresh failed' }
    expect(runtimeLabel(o)).toBe('last known: running · 10m ago')
    expect(runtimeDescription(o)).toBe('Last known: running · checked 10m ago · refresh unavailable')
    expect(runtimeChip(o)).toBe('unknown')
  })
})
