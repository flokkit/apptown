import { expect, it } from 'vitest'
import { overviewActivity } from './ApplicationOverview'
import type { OverviewData } from '../../parts/runActivity'
import type { DeploymentView } from '../../wire.gen'

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
