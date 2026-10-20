import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../data'
import type { RunFindingsView } from '../../wire.gen'
import { RunFindings } from './Findings'

// Record ids are UUIDv7 strings.
const RUN = '00000000-0005-7000-8000-000000000000'

const page = (unread: RunFindingsView['unread']) => {
  const client = newQueryClient()
  client.setQueryData(getKey(`/runs/${RUN}/findings`), {
    findings: [], unread, projectSlug: 'game',
    summary: { total: 0, dispositioned: 0, actionable: 0, bySeverity: {} },
  } satisfies RunFindingsView)
  return renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><RunFindings runId={RUN} onChange={() => {}} /></QueryClientProvider></MemoryRouter>)
}

// A report AppTown could not read leaves its findings unknown: the change
// page lists it with the reason and never says there are no findings.
describe("a run's findings on the change page", () => {
  it('shows an unread report with its reason, not "no findings"', () => {
    const html = page([{ name: 'gone', kind: 'scan-report', reason: 'no scanner named "gone" is configured' }])
    expect(html).toContain('not read')
    expect(html).toContain('no scanner named')
    expect(html).not.toContain('no findings')
  })
  it('says no findings when every report was read and none had any', () => {
    expect(page([])).toContain('no findings')
  })
})
