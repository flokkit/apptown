import { describe, expect, it } from 'vitest'
import { runtimeChip, runtimeDescription } from './runtime'

describe('runtime status', () => {
  const unobserved = { observation: 'unknown' as const, observationStale: true }
  it('keeps unknown for a deployment nobody has observed', () => {
    expect(runtimeDescription(unobserved)).toMatch(/unknown/)
    expect(runtimeChip(unobserved)).toBe('unknown')
  })
})
