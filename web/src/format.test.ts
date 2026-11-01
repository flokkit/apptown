import { describe, expect, it } from 'vitest'
import { fmtCost, fmtDuration, fmtLastRead, fmtTokens, fmtWhen, fmtStamp, lastSeen, relTime, stampDate } from './format'

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
})

describe('fmtTokens and fmtCost', () => {
  it('abbreviates tokens', () => {
    expect(fmtTokens(999)).toBe('999')
    expect(fmtTokens(1500)).toBe('1.5k')
    expect(fmtTokens(2_500_000)).toBe('2.5M')
    expect(fmtTokens(1_000_000_000)).toBe('1.0B')
  })
  it('shows cents for dollars and four places under one', () => {
    expect(fmtCost(1_500_000)).toBe('$1.50')
    expect(fmtCost(12_300)).toBe('$0.0123')
  })
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
