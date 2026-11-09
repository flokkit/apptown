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

it('states a clean old scan and changed main together without claiming main passed', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/game/findings'), {
    sources: [source], findings: [], summary: { actionable: 0 }, repo: { stale: true }, statusLabel: 'unknown',
  })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><FindingsTab slug="game" /></QueryClientProvider></MemoryRouter>)
  expect(html).toContain('Main changed since the last scan.')
  expect(html).toContain('No problems found in the last scan; main has changed since.')
  expect(html).not.toContain('Not scanned since main changed.')
  expect(html).not.toContain('chip pass')
  client.clear()
})

it('leads unreadable scans with a plain consequence and a next action, the diagnostic after them', () => {
 const client=newQueryClient()
 client.setQueryData(getKey('/projects/game/findings'), {sources:[{...source, unreadable:'SARIF parse: invalid character'}],findings:[],summary:{actionable:0},repo:{},statusLabel:'unknown'})
 const html=renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><FindingsTab slug="game"/></QueryClientProvider></MemoryRouter>)
 expect(html).toContain("One scan report couldn&#x27;t be read; security checks are incomplete.")
 expect(html).toContain('Ask a project member with permission to open a change to re-run the scan.')
 const consequence = html.indexOf('security checks are incomplete')
 expect(html.indexOf('Ask a project member')).toBeGreaterThan(consequence)
 expect(html.indexOf('SARIF parse')).toBeGreaterThan(html.indexOf('Ask a project member'))
 client.clear()
})


it('offers the scan action to a viewer who may open a change', () => {
 const client=newQueryClient()
 client.setQueryData(getKey('/authz/me?project=game'),{project:{'change:create':true}})
 client.setQueryData(getKey('/projects/game/findings'),{sources:[{...source,unreadable:'SARIF parse failed'}],findings:[],summary:{actionable:0},repo:{},statusLabel:'unknown'})
 const html=renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><FindingsTab slug="game"/></QueryClientProvider></MemoryRouter>)
 expect(html).toMatch(/<a[^>]*href="\/projects\/game"[^>]*>Open a change to re-run the scan<\/a>/)
 expect(html).not.toContain('Ask a project member')
 client.clear()
})

describe('the problems list with nothing in it', () => {
  const render = (view: object) => {
    const client = newQueryClient()
    client.setQueryData(getKey('/projects/game/findings'), view)
    const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><FindingsTab slug="game" /></QueryClientProvider></MemoryRouter>)
    client.clear()
    return html
  }

  it('says the last scan found no problems when every scan was read', () => {
    const html = render({ sources: [source], findings: [], summary: { actionable: 0 }, repo: {}, statusLabel: 'failed' })
    expect(html).toMatch(/aria-label="Problems".*No problems in the last scan\./)
  })

  it('keeps the unread and no-scan lines and does not claim a clean scan', () => {
    const unread = render({ sources: [{ ...source, unreadable: 'SARIF parse failed' }], findings: [], summary: { actionable: 0 }, repo: {}, statusLabel: 'unknown' })
    expect(unread).toContain("security checks are incomplete")
    expect(unread).not.toContain('No problems in the last scan.')
    const none = render({ sources: [], findings: [], summary: { actionable: 0 }, repo: {}, statusLabel: 'unknown' })
    expect(none).toContain('No scans yet.')
    expect(none).not.toContain('No problems in the last scan.')
  })
})
