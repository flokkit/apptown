import { describe, expect, it } from 'vitest'
import { heldBack, rehearsalLine } from './DataProtection'
import type { RehearsalRef } from '../wire.gen'

// Record ids are UUIDv7 strings.
const SET = '00000000-0004-7000-8000-000000000000'
const OPERATION = '00000000-0009-7000-8000-000000000000'

const base: RehearsalRef = {
  at: '2026-10-08T01:00:00Z', outcome: 'passed', passed: true, backup: SET, release: 7, host: 'rehearse-1', askedBy: 'alice', operation: OPERATION,
  facts: { checks: [], withheld: ['SMTP_PASSWORD'], notStarted: ['worker'], images: {}, egress: 'outbound network not blocked' },
}

// The last restore rehearsal reads as passed, passed without a data check,
// failed (with the step), or why there was none; what it ran without is
// said every time, the open outbound network first.
describe('the rehearsal line', () => {
  it('says each outcome and why there was none', () => {
    expect(rehearsalLine(base)).toContain('last restore rehearsal passed')
    expect(rehearsalLine(base)).toContain('from a backup set, release 7, on rehearse-1')
    expect(rehearsalLine({ ...base, outcome: 'passed: booted and ready; no data check declared' })).toContain('no data check declared')
    expect(rehearsalLine({ ...base, passed: false, outcome: 'failed', failedStep: 'asking the app' })).toContain('failed')
    expect(rehearsalLine({ ...base, passed: false, outcome: 'failed', failedStep: 'asking the app' })).toContain('while asking the app')
    expect(rehearsalLine({ ...base, passed: false, outcome: 'unknown: AppTown stopped while it ran', release: 0 })).toContain('unknown: AppTown stopped while it ran')
    expect(rehearsalLine({ ...base, host: 'server-1', facts: { ...base.facts, sharedHost: true } })).toContain('on server-1 (shared, not set aside for rehearsals)')
    expect(rehearsalLine(base)).not.toContain('not kept')
    expect(rehearsalLine(null, 'never rehearsed: no rehearsal place')).toBe('Backup never tested')
  })
  it('says what it held back', () => {
    expect(heldBack(base)).toBe('outbound network not blocked; withheld: SMTP_PASSWORD; not started: worker')
  })
})
