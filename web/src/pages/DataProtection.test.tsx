import { describe, expect, it } from 'vitest'
import { copyLine, currentStep, drillLine, itemLabel, nativeLine, operationTone, stateWords } from './DataProtection'
import type { ItemProtection, Operation } from '../wire.gen'

// Record ids are UUIDv7 strings.
const SET = '00000000-0003-7000-8000-000000000000'
const OPERATION = '00000000-0001-7000-8000-000000000000'
const PROJECT = '00000000-0001-7000-8000-000000000000'

const item = (over: Partial<ItemProtection>): ItemProtection => ({ need: 'postgres', where: 'database app on forge-ledger-prod', copy: 'none', ...over })

describe('how a cloud deployment\'s data is protected, in words', () => {
  it('tells fresh, stale, unknown and none apart, with the newest copy where there is one', () => {
    const last = { backup: SET, takenAt: '2026-10-08T06:10:00Z', bytes: 3 * 1024 * 1024, sha256: 'ab' }
    expect(copyLine(item({ copy: 'fresh', lastCopy: last }))).toMatch(/^forge copies: newest copy .*, 3\.0 MB$/)
    expect(copyLine(item({ copy: 'stale', copyNote: 'the last copy failed: could not connect', lastCopy: last }))).toContain('forge copies: the last copy failed: could not connect; newest copy')
    expect(copyLine(item({ copy: 'unknown', copyNote: 'the last copy was interrupted' }))).toBe('forge copies: the last copy was interrupted')
    expect(copyLine(item({ copy: 'none' }))).toBe('no copy')
    expect(copyLine(item({ copy: 'disposable' }))).toBe('disposable: starts empty')
  })
  it('tells two volumes of one need apart by their volume, and names a lone one by its need', () => {
    const old = item({ volume: 'forge-need-old-postgres' }), now = item({ volume: 'forge-need-env-app-main-postgres' })
    expect(itemLabel(old, [old, now])).toBe('postgres · volume forge-need-old-postgres')
    expect(itemLabel(now, [now])).toBe('postgres')
  })
  it('says the provider\'s backups were not read rather than none', () => {
    expect(nativeLine(item({}))).toBe('')
    expect(nativeLine(item({ native: { mechanism: 'do-database-backups', count: 0, unknown: 'DigitalOcean answered 500' } }))).toBe('DigitalOcean backups: unknown — DigitalOcean answered 500')
    expect(nativeLine(item({ native: { mechanism: 'do-database-backups', count: 0 } }))).toBe('DigitalOcean backups: none yet')
    expect(nativeLine(item({ native: { mechanism: 'do-database-backups', count: 2, oldest: '2026-10-01T06:10:00Z', newest: '2026-10-07T06:10:00Z' } }))).toMatch(/^DigitalOcean backups: oldest .+, newest .+$/)
  })
  it('says whether a copy came back, from which set, where and as whom', () => {
    expect(drillLine(item({}))).toBe('never checked to come back')
    expect(drillLine(item({ drill: { at: '2026-10-07T06:10:00Z', backup: SET, target: 'do-syd', place: "a new database on the deployment's cluster", user: 'app', passed: true } }))).toMatch(/^came back on .+ from a backup set into a new database on the deployment's cluster as app$/)
    // A bucket's check ran on the copy runner, as no app user.
    expect(drillLine(item({ drill: { at: '2026-10-07T06:10:00Z', backup: SET, target: 'do-syd', place: 'a new s3 store on demo-docker', user: '', passed: true } }))).toMatch(/^came back on .+ from a backup set into a new s3 store on demo-docker$/)
    expect(drillLine(item({ drill: { at: '2026-10-07T06:10:00Z', backup: SET, target: 'do-syd', place: 'app_check_9', user: 'app', passed: false, detail: 'missing after import' } }))).toMatch(/^did not come back .+ from a backup set: missing after import$/)
  })
  it('names the step a running operation is in and how long it has run', () => {
    const op: Operation = { id: OPERATION, number: 1, projectId: PROJECT, subject: 'project:1/deployment:2', holds: [], input: { deployment: 'prod' }, kind: 'deployment.take-down', askedBy: 'alice', state: 'running', result: {},
      cancellable: true, createdAt: '2026-10-08T00:00:00Z', touchedAt: '2026-10-08T00:00:00Z', startedAt: '2026-10-08T00:00:00Z',
      steps: [{ name: 'deleting the app, so nothing writes', started: '2026-10-08T00:00:00Z', ended: '2026-10-08T00:00:10Z' }, { name: 'copying postgres', started: '2026-10-08T00:00:10Z' }] }
    expect(currentStep(op, Date.parse('2026-10-08T00:02:05Z'))).toBe('Take-down running for 2m 5s: copying postgres')
    expect(currentStep({ ...op, steps: op.steps.slice(0, 1) }, Date.parse('2026-10-08T00:00:30Z'))).toBe('Take-down running for 30s, between steps')
  })
  it('keeps success and verification apart, and says a queued operation has not started', () => {
    const op: Operation = { id: OPERATION, number: 1, projectId: PROJECT, holds: [], input: { deployment: 'prod' }, kind: 'deployment.copy', askedBy: 'alice', state: 'succeeded', result: {},
      cancellable: false, createdAt: '2026-10-08T00:00:00Z', touchedAt: '2026-10-08T00:00:00Z', steps: [] }
    expect(stateWords({ ...op, verification: 'verified' })).toBe('done, and the copy came back')
    expect(stateWords({ ...op, verification: 'not-verified' })).toBe('done; nothing has checked the copy comes back')
    expect(operationTone({ ...op, verification: 'not-verified' })).toBe('pending')
    expect(stateWords({ ...op, kind: 'deployment.rehearsal', verification: 'verified' })).toBe('passed: the app answered on the copy')
    expect(stateWords({ ...op, state: 'unknown' })).toBe('unknown: the forge stopped while it ran')
    expect(operationTone({ ...op, state: 'unknown' })).toBe('fail')
    expect(currentStep({ ...op, state: 'queued' }, Date.parse('2026-10-08T00:00:40Z'))).toBe('Copy waiting to start (asked 40s ago)')
  })
})
