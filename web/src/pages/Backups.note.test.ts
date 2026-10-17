import { describe, expect, it } from 'vitest'
import { restoreDone, restoreNote } from './Backups'
import type { RestoreResult } from '../wire.gen'

const base: RestoreResult = {
  backup: 'b3', from: 'main', safetyBackup: 'b4', stopped: true, status: 'restored',
  items: [{ kind: 'volume', name: 'data', outcome: 'verified' }],
} as RestoreResult

describe('restoreNote', () => {
  it('says the app is not running again, naming the part, apart from the data', () => {
    const note = restoreNote('main', '2026-10-01T09:00:00Z', { ...base, started: false, notStarted: ['forge-env-x-main-worker: refused'] })
    expect(note).toContain('restored from the set taken')
    expect(note).toContain('The app is not running again: forge-env-x-main-worker: refused')
  })
  it('says an app that started has not answered yet', () => {
    expect(restoreNote('main', '2026-10-01T09:00:00Z', { ...base, started: true, ready: false })).toContain('The app has not answered yet')
  })
  it('says nothing about the app when it came back', () => {
    const note = restoreNote('main', '2026-10-01T09:00:00Z', { ...base, started: true, ready: true })
    expect(note).not.toContain('The app')
    expect(note).not.toContain('not checked')
  })
  it('does not present an unchecked app as answering', () => {
    expect(restoreNote('main', '2026-10-01T09:00:00Z', { ...base, started: true })).toContain('Whether the app answers was not checked')
  })
  it('says when nothing is known about the app, and is not done', () => {
    expect(restoreNote('main', '2026-10-01T09:00:00Z', { ...base, stopped: false })).toContain('Whether the app came back is not known')
    expect(restoreDone({ ...base, stopped: false })).toBe(false)
  })
  it('names a part that is not there', () => {
    expect(restoreNote('main', '2026-10-01T09:00:00Z', { ...base, started: false, notStarted: ['forge-svc-env-x-main-worker: not there, so not started'] }))
      .toContain('forge-svc-env-x-main-worker: not there')
  })
  it('marks a restore done only when the data is back and the app is known to have come back', () => {
    expect(restoreDone({ ...base, started: true, ready: true })).toBe(true)
    expect(restoreDone({ ...base, started: false, notStarted: ['x'] })).toBe(false)
    expect(restoreDone({ ...base, started: true, ready: false })).toBe(false)
    expect(restoreDone({ ...base, status: 'partly restored' })).toBe(false)
  })
})
