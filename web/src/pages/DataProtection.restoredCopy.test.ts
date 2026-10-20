import { describe, expect, it } from 'vitest'
import { copyHead, restoreTestLine } from './DataProtection'
import type { RestoredCopy } from '../wire.gen'

// Record ids are UUIDv7 strings.
const COPY = '00000000-0004-7000-8000-000000000000'
const SET = '00000000-0041-7000-8000-000000000000'
const OPERATION = '00000000-0009-7000-8000-000000000000'

const base: RestoredCopy = {
  id: COPY, number: 4, source: 'prod', deployment: 'copy-4', backup: SET, release: 7, target: 'do-syd', test: false, askedBy: 'bls',
  startedAt: '2026-10-08T05:00:00Z', checkedAt: '2026-10-08T05:12:00Z', expiresAt: '2026-10-08T09:00:00Z', open: true, outcome: 'passed',
  facts: { checks: [], withheld: [], notStarted: [], images: {}, egress: 'outbound network not blocked' },
  entries: [{ person: 'bls', at: '2026-10-08T05:20:00Z' }, { person: 'ana', at: '2026-10-08T05:30:00Z' }, { person: 'bls', at: '2026-10-08T06:30:00Z' }],
  operation: OPERATION, warning: 'w', boundary: 'b',
}

// The last restore test reads apart from the rehearsal: passed with its
// set, release and platform; failed with the step; ended before it was
// checked; or never tested on its platform.
describe('the restore test line', () => {
  it('says each outcome and when there was none', () => {
    expect(restoreTestLine({ ...base, test: true })).toContain('last restore test passed')
    expect(restoreTestLine({ ...base, test: true })).toContain('from a backup set, release 7, on do-syd')
    expect(restoreTestLine({ ...base, outcome: 'failed', failedStep: 'asking the app through the gate' })).toContain('failed')
    expect(restoreTestLine({ ...base, outcome: 'failed', failedStep: 'asking the app through the gate' })).toContain('while asking the app through the gate')
    expect(restoreTestLine({ ...base, outcome: '', endDetail: 'nothing was made' })).toContain('nothing was made')
    expect(restoreTestLine(null, 'never tested on its platform')).toBe('last restore test: never tested on its platform')
  })
  it("says AppTown's check of the gate, and each check that failed", () => {
    const gate = [
      { name: 'refuses a request with no token', path: '/', status: 404, passed: true },
      { name: 'refuses that ticket a second time', path: '/_apptown/enter', status: 303, passed: false },
    ]
    const passed = { ...base, test: true, facts: { ...base.facts, gate: gate.map((k) => ({ ...k, passed: true })) } }
    expect(restoreTestLine(passed)).toContain('· gate: 2 of 2 checks passed')
    const failed = { ...base, test: true, outcome: 'failed', failedStep: 'checking the gate', facts: { ...base.facts, gate } }
    expect(restoreTestLine(failed)).toContain('while checking the gate · gate: 1 of 2 checks passed; failed: refuses that ticket a second time (/_apptown/enter 303)')
    expect(restoreTestLine({ ...base, test: true })).not.toContain('gate:')
  })
})

// A standing copy says what it is a copy of, until when it is open, and
// each person let in once.
describe('a restored copy on the card', () => {
  it('says until when and who opened it', () => {
    const line = copyHead(base, 'prod')
    expect(line).toContain('restored copy of prod from a backup set')
    expect(line).toContain('open until')
    expect(line).toContain('opened by bls, ana')
  })
  it('says a test and a copy nobody opened', () => {
    expect(copyHead({ ...base, test: true, entries: [] }, 'prod')).toContain('a restore test, taken down once checked')
    expect(copyHead({ ...base, entries: [] }, 'prod')).toContain('not opened yet')
    expect(copyHead({ ...base, checkedAt: undefined }, 'prod')).toContain('starting')
  })
})
