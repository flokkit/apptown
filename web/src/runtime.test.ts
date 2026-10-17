import { describe, expect, it } from 'vitest'
import { runtimeChip, runtimeDescription } from './runtime'

describe('runtime status', () => {
  const unobserved = { observation: 'unknown' as const, observationStale: true }
  it('says a project with no deployment is not deployed yet, not unknown', () => {
    expect(runtimeDescription({ ...unobserved, deployed: false })).toBe('not deployed yet')
    expect(runtimeChip({ ...unobserved, deployed: false })).toBe('')
  })
  it('keeps unknown for a deployment nobody has observed', () => {
    expect(runtimeDescription({ ...unobserved, deployed: true })).toMatch(/unknown/)
    expect(runtimeChip({ ...unobserved, deployed: true })).toBe('unknown')
    expect(runtimeChip(unobserved)).toBe('unknown')
  })
})
