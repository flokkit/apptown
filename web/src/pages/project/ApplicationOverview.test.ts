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
  expect(entries.map(e => e.word)).toEqual(['unreachable', 'failed', 'unknown'])
  expect(entries[0].help).toContain('Club host')
  expect(entries[1].to).toBe('/changes/change')
})

// Routine copies remain on Backups; Activity keeps failures, stops and manual copies.
it('shows only failed, stopped or manually started backups beside changes and releases', () => {
  const backups = ['pre-release', 'release', 'scheduled', 'manual'].flatMap((trigger, i) =>
    (['finished', 'failed', 'stopped', 'unknown'] as const).map((statusLabel, j) => ({
      id: `${i}-${j}`, trigger, statusLabel, status: { finished: 'ok', failed: 'partial', stopped: 'pruned', unknown: 'ok' }[statusLabel], deployment: 'main', takenAt: '2026-10-31',
    } as Backup)))
  const entries = overviewActivity({ history: [] } as unknown as OverviewData, [], backups, [], 'club')
  expect(entries.map(e => e.id)).toEqual([
    'backup-0-1', 'backup-0-2', 'backup-1-1', 'backup-1-2', 'backup-2-1', 'backup-2-2',
    'backup-3-0', 'backup-3-1', 'backup-3-2', 'backup-3-3',
  ])
})

it.each(['partial', 'failed', 'pruned'] as const)('retains a stale %s backup event with an unknown light', status => {
  const b = { id: 'copy', trigger: 'scheduled', status, statusLabel: 'unknown', takenAt: '2026-10-31', deployment: 'main' } as Backup
  const entries = overviewActivity({ history: [] } as unknown as OverviewData, [], [b], [], 'club')
  expect(entries).toHaveLength(1)
  expect(entries[0].word).toBe('unknown')
})
