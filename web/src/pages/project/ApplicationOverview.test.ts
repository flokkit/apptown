import { expect, it } from 'vitest'
import { overviewActivity } from './ApplicationOverview'
import type { OverviewData } from '../../parts/runActivity'
import type { Backup, DeploymentView } from '../../wire.gen'

it('keeps unknown, unreachable and failed owner labels separate in the activity record', () => {
  const data = { history: [{ changeId: 'change', changeNumber: 4, title: 'Fix photos', when: '2026-10-30', statusLabel: 'failed' }] } as OverviewData
  const deployments = [
    { id: 'preview', name: 'preview', observation: 'unreachable', targetName: 'Club host', observedAt: '2026-10-31', observationIssue: 'Credential unavailable' },
    { id: 'main', name: 'main', observation: 'unknown', targetName: 'Main host', observedAt: '2026-10-29' },
  ] as DeploymentView[]
  const entries = overviewActivity(data, deployments, [], [], 'club')
  expect(entries.map(e => e.word)).toEqual(['failed'])
  expect(entries[0].to).toBe('/changes/change')
})

// Routine copies remain on Backups; Activity keeps failures, stops and manual copies.
it('keeps automated and manual backups on their own page', () => {
  const backups = ['pre-release', 'release', 'scheduled', 'manual'].flatMap((trigger, i) =>
    (['finished', 'failed', 'stopped', 'unknown'] as const).map((statusLabel, j) => ({
      id: `${i}-${j}`, trigger, statusLabel, status: { finished: 'ok', failed: 'partial', stopped: 'pruned', unknown: 'ok' }[statusLabel], deployment: 'main', takenAt: '2026-10-31',
    } as Backup)))
  const entries = overviewActivity({ history: [] } as unknown as OverviewData, [], backups, [], 'club')
  expect(entries).toEqual([])
})

it.each(['partial', 'failed', 'pruned'] as const)('keeps a stale %s backup on its own page', status => {
  const b = { id: 'copy', trigger: 'scheduled', status, statusLabel: 'unknown', takenAt: '2026-10-31', deployment: 'main' } as Backup
  const entries = overviewActivity({ history: [] } as unknown as OverviewData, [], [b], [], 'club')
  expect(entries).toEqual([])
})

it('labels a release with its linked change number even outside the recent activity window', () => {
  const releases = [{ number: 6, changes: [{ id: 'older-change', number: 4, title: 'Keep photos' }], statusLabel: 'deployed', deployment: 'main', releasedAt: '2026-10-30' }] as unknown as import('../../api').Release[]
  const entries = overviewActivity({ history: [] } as unknown as OverviewData, [], [], releases, 'club')
  expect(entries[0].title).toBe('Change 4 → Release 6')
})
