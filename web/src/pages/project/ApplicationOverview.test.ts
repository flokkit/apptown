import { expect, it } from 'vitest'
import { overviewActivity } from './ApplicationOverview'
import type { OverviewData } from '../../parts/runActivity'
import type { Backup, DeploymentView } from '../../wire.gen'

it('keeps unknown, unreachable and failed owner labels separate in the activity record', () => {
  const data = { active: [], open: [], history: [{ changeId: 'change', changeNumber: 4, title: 'Fix photos', when: '2026-10-30', waitingOn: { says: 'Tests failed', tone: 'bad' } }] } as unknown as OverviewData
  const deployments = [
    { id: 'preview', name: 'preview', observation: 'unreachable', targetName: 'Club host', observedAt: '2026-10-31', observationIssue: 'Credential unavailable' },
    { id: 'main', name: 'main', observation: 'unknown', targetName: 'Main host', observedAt: '2026-10-29' },
  ] as DeploymentView[]
  const entries = overviewActivity(data, deployments, [], [], 'club')
  expect(entries.map(e => 'waitingOn' in e ? e.waitingOn.says : undefined)).toEqual(['Tests failed'])
  expect(entries[0].to).toBe('/changes/change')
})

// Routine copies remain on Backups; Activity keeps failures, stops and manual copies.
it('keeps automated and manual backups on their own page', () => {
  const backups = ['pre-release', 'release', 'scheduled', 'manual'].flatMap((trigger, i) =>
    (['finished', 'failed', 'stopped', 'unknown'] as const).map((statusLabel, j) => ({
      id: `${i}-${j}`, trigger, statusLabel, status: { finished: 'ok', failed: 'partial', stopped: 'pruned', unknown: 'ok' }[statusLabel], deployment: 'main', takenAt: '2026-10-31',
    } as Backup)))
  const entries = overviewActivity({ active: [], open: [], history: [] } as unknown as unknown as OverviewData, [], backups, [], 'club')
  expect(entries).toEqual([])
})

it.each(['partial', 'failed', 'pruned'] as const)('keeps a stale %s backup on its own page', status => {
  const b = { id: 'copy', trigger: 'scheduled', status, statusLabel: 'unknown', takenAt: '2026-10-31', deployment: 'main' } as Backup
  const entries = overviewActivity({ active: [], open: [], history: [] } as unknown as unknown as OverviewData, [], [b], [], 'club')
  expect(entries).toEqual([])
})


it('names releases and opens their own pages', () => {
  const releases = [{ number: 6, changes: [{ id: 'older', number: 4, title: 'Keep photos' }], statusLabel: 'deployed', deployment: 'fly', releasedAt: '2026-10-30' }] as unknown as import('../../api').Release[]
  const entries = overviewActivity({ active: [], open: [], history: [] } as unknown as unknown as OverviewData, [], [], releases, 'club')
  expect(entries.map(e => [e.number, e.title, e.to])).toEqual([[undefined, 'Release 6 to fly', '/projects/club/releases/6']])
})

it('keeps the landed change and its release as separate records', () => {
  const history = [{ changeId: 'change', changeNumber: 4, title: 'Fix photos', when: '2026-10-30', waitingOn: { says: 'landed', tone: 'ok' } }]
  const releases = [{ number: 6, changes: [{ id: 'change', number: 4, title: 'Fix photos' }], statusLabel: 'deployed', deployment: 'fly', releasedAt: '2026-10-31' }] as unknown as import('../../api').Release[]
  const entries = overviewActivity({ active: [], open: [], history } as unknown as OverviewData, [], [], releases, 'club')
  expect(entries).toHaveLength(2)
  expect(entries[0]).toMatchObject({ title: 'Release 6 to fly', to: '/projects/club/releases/6', word: 'deployed' })
  expect(entries[1]).toMatchObject({ title: 'Fix photos', to: '/changes/change', waitingOn: { says: 'landed' } })
})

it('shows in-flight changes first, once each, even without history', () => {
  const data = { active: [
    { changeId: 'change', changeNumber: 1, title: 'First build', startedAt: '2026-10-30', waitingOn: { says: 'checking', tone: 'busy' } },
    { changeId: null, title: 'System run' },
  ], open: [
    { changeId: 'change', title: 'Duplicate' },
    { changeId: 'chat', changeNumber: 2, title: 'Chat build', waitingOn: { says: 'building', tone: 'busy' } },
  ], history: [] } as unknown as unknown as OverviewData
  expect(overviewActivity(data, [], [], [], 'club').map(e => [e.title, 'waitingOn' in e ? e.waitingOn.says : undefined])).toEqual([['First build', 'checking'], ['Chat build', 'building']])
})
