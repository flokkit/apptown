import { describe, expect, it } from 'vitest'
import { fmtCores, fmtCost, fmtPrice, fmtDuration, fmtLastRead, fmtTokens, fmtWhen, fmtStamp, lastSeen, relTime, stampDate } from './format'

const NOW = Date.parse('2026-09-03T10:00:00Z')

describe('stampDate', () => {
  it('reads a stamp in its own zone', () => {
    expect(stampDate('2026-09-03T09:00:00Z').toISOString()).toBe('2026-09-03T09:00:00.000Z')
    expect(stampDate('2026-09-03T19:00:00+10:00').toISOString()).toBe('2026-09-03T09:00:00.000Z')
    expect(stampDate('2026-09-03T09:00:00.123456Z').toISOString()).toBe('2026-09-03T09:00:00.123Z')
  })
})

describe('relTime', () => {
  it('says how long ago', () => {
    expect(relTime('2026-09-03T09:59:30Z', NOW)).toBe('now')
    expect(relTime('2026-09-03T09:55:00Z', NOW)).toBe('5m ago')
    expect(relTime('2026-09-03T07:00:00Z', NOW)).toBe('3h ago')
    expect(relTime('2026-09-01T10:00:00Z', NOW)).toBe('2d ago')
  })
  it('is empty for nothing, garbage, or the future', () => {
    expect(relTime('', NOW)).toBe('')
    expect(relTime('not a date', NOW)).toBe('')
    expect(relTime('2026-09-04T10:00:00Z', NOW)).toBe('')
  })
})

describe('fmtWhen', () => {
  it('uses the same reader-local event format for recent and old events', () => {
    for (const iso of ['2026-09-03T09:50:00Z', '2026-09-01T10:00:00Z', '2026-10-30T06:26:00Z']) {
      expect(fmtWhen(iso, NOW)).toBe(fmtStamp(iso))
    }
    const local = new Date(2026, 9, 30, 17, 26).toISOString()
    expect(fmtWhen(local)).toBe('30 Oct, 17:26')
    expect(fmtWhen('bad')).toBe('')
  })
})

describe('fmtDuration', () => {
  it('picks the unit that fits', () => {
    expect(fmtDuration(45)).toBe('45s')
    expect(fmtDuration(300)).toBe('5m')
    expect(fmtDuration(7200)).toBe('2.0h')
  })
  // A deploy that took 74.664 seconds reads 75s, not the milliseconds.
  it('shows whole seconds', () => {
    expect(fmtDuration(74.664)).toBe('75s')
    expect(fmtDuration(0.4)).toBe('0s')
    expect(fmtDuration(89.6)).toBe('1m')
  })
})

describe('fmtTokens and fmtCost', () => {
  it('abbreviates tokens', () => {
    expect(fmtTokens(999)).toBe('999')
    expect(fmtTokens(1500)).toBe('1.5k')
    expect(fmtTokens(2_500_000)).toBe('2.5M')
    expect(fmtTokens(1_000_000_000)).toBe('1.0B')
  })
  it('shows whole cents, rounded down, and under a cent in words', () => {
    expect(fmtCost(1_500_000)).toBe('$1.50')
    expect(fmtCost(12_300)).toBe('$0.01')
    expect(fmtCost(19_900)).toBe('$0.01')
    expect(fmtCost(3_000)).toBe('under $0.01')
    expect(fmtCost(0)).toBe('$0.00')
  })
  // A total and its parts use one rule, rounding down: a total never reads
  // below one of its parts, nor below the sum of the parts shown, and a
  // lower bound never claims more than recorded.
  it('prices a total the way it prices its parts', () => {
    expect(fmtPrice(34_100, 0)).toBe('$0.03')
    expect(fmtPrice(34_100, 2)).toBe('at least $0.03')
    expect(fmtPrice(35_100, 2)).toBe('at least $0.03')
    // The usage page: the total is all Chat, partly unpriced elsewhere.
    expect(fmtPrice(106_600, 2)).toBe('at least $0.10')
    expect(fmtPrice(106_600, 0)).toBe('$0.10')
    expect(fmtPrice(3_000, 1)).toBe('partly priced')
    expect(fmtPrice(0, 1)).toBe('not priced')
  })
  it('never shows a total below the sum of its shown parts', () => {
    const shown = (s: string) => s.startsWith('$') ? Number(s.slice(1)) : 0
    const sets = [[5_000, 5_000], [106_600, 4_000], [15_000, 15_000, 15_000], [9_999, 9_999, 1], [1_234_567, 7_654_321, 5_555]]
    for (const parts of sets) {
      const total = parts.reduce((a, b) => a + b, 0)
      const sum = parts.reduce((a, p) => a + shown(fmtCost(p)), 0)
      expect(shown(fmtCost(total)) + 1e-9, JSON.stringify(parts)).toBeGreaterThanOrEqual(sum)
    }
  })
})

// Kills: real use too small for two places shown as idle.
it('says tiny real CPU use is under 0.01, not 0', () => {
  expect([0, 0.003, 0.005, 0.25, 2].map(fmtCores)).toEqual(['0', '<0.01', '0.01', '0.25', '2'])
})

describe('fmtLastRead', () => {
  // Local times, since the mark is read in the reader's own zone.
  const now = new Date(2026, 9, 11, 15, 30).getTime()
  it('gives the time alone for a read earlier today', () => {
    expect(fmtLastRead(new Date(2026, 9, 11, 9, 41).getTime(), now)).toBe('09:41')
  })
  it('adds the date for a read before today', () => {
    expect(fmtLastRead(new Date(2026, 9, 10, 23, 5).getTime(), now)).toBe('10 Oct, 23:05')
  })
})

it('tolerates a server clock up to five minutes ahead', () => {
 const now = Date.parse('2026-11-01T00:00:00Z')
 expect(relTime('2026-11-01T00:03:00Z',now)).toBe('just now')
 expect(lastSeen('2026-11-01T00:03:00Z',now)).toBe('last seen just now')
})
