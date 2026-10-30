import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../data'
import type { ProjectFindingsView } from '../../wire.gen'
import { FindingsTab } from './Security'

// Record ids are UUIDv7 strings.
const RUN = '00000000-0001-7000-8000-000000000000'
const OTHER_RUN = '00000000-0009-7000-8000-000000000000'
const CHANGE = '00000000-0003-7000-8000-000000000000'

const source = { binding: 'osv', runId: RUN, runNumber: 4, changeId: CHANGE, changeNumber: 7, at: '2026-10-03T00:00:00Z', findings: 0 }

describe('the security page lists every scan report', () => {
  const page = (sources: ProjectFindingsView['sources']) => {
    const client = newQueryClient()
    client.setQueryData(getKey('/projects/game/findings'), {
      findings: [], sources, dispositions: [], projectSlug: 'game', repo: {},
      statusLabel: sources.length && !sources.some(s => s.unreadable) ? 'passed' : 'unknown',
      summary: { total: 0, dispositioned: 0, actionable: 0, bySeverity: {} },
    } satisfies ProjectFindingsView)
    return renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><FindingsTab slug="game" /></QueryClientProvider></MemoryRouter>)
  }

  it('links a report to the change its run worked on, not to its run id as a change', () => {
    const html = page([{ ...source, runId: OTHER_RUN, changeId: CHANGE }])
    expect(html).toContain(`href="/changes/${CHANGE}"`)
    expect(html).not.toContain(`href="/changes/${OTHER_RUN}"`)
    expect(html).toContain('change 7')
  })

  it('shows an unread report with its reason and does not say nothing was found', () => {
    const html = page([{ ...source, binding: 'gone', unreadable: 'no scanner named "gone" is configured' }])
    expect(html).toContain('gone')
    expect(html).toContain('no scanner named')
    expect(html).not.toContain('nothing found')
  })
})
