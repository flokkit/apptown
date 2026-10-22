import { describe, expect, it } from 'vitest'
import { copyHead, restoreTestLine } from './DataProtection'
import type { RestoredCopy } from '../wire.gen'

// Record ids are UUIDv7 strings.
const COPY = '00000000-0004-7000-8000-000000000000'
const SET = '00000000-0041-7000-8000-000000000000'
const OPERATION = '00000000-0009-7000-8000-000000000000'

const base: RestoredCopy = {
  id: COPY, number: 4, source: 'prod', deployment: 'copy-4', backup: SET, release: 7, target: 'do-syd', askedBy: 'bls',
  startedAt: '2026-10-08T05:00:00Z', checkedAt: '2026-10-08T05:12:00Z', outcome: 'passed',
  facts: { checks: [], withheld: [], notStarted: [], images: {}, egress: 'outbound network not blocked' },
  operation: OPERATION, warning: 'w', boundary: 'b',
}

// The last restore test reads apart from the rehearsal: passed with its
// set, release and platform; failed with the step; ended before it was
// checked; or never tested on its platform.
describe('the restore test line', () => {
  it('says each outcome and when there was none', () => {
    expect(restoreTestLine(base)).toContain('last restore test passed')
    expect(restoreTestLine(base)).toContain('from a backup set, release 7, on do-syd')
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
    const passed = { ...base, facts: { ...base.facts, gate: gate.map((k) => ({ ...k, passed: true })) } }
    expect(restoreTestLine(passed)).toContain('· gate: 2 of 2 checks passed')
    const failed = { ...base, outcome: 'failed', failedStep: 'checking the gate', facts: { ...base.facts, gate } }
    expect(restoreTestLine(failed)).toContain('while checking the gate · gate: 1 of 2 checks passed; failed: refuses that ticket a second time (/_apptown/enter 303)')
    expect(restoreTestLine(base)).not.toContain('gate:')
  })
})

// A standing copy says what it is a copy of and how far its test got.
describe('a restored copy on the card', () => {
  it('says what it is a copy of and whether it was checked', () => {
    expect(copyHead(base, 'prod')).toBe('restored copy of prod from a backup set · checked, to be taken down')
    expect(copyHead({ ...base, checkedAt: undefined }, 'prod')).toBe('restored copy of prod from a backup set · being tested')
  })
})
