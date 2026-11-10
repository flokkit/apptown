import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import type { ChangeView } from '../../api'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../data'
import { MemoryRouter } from 'react-router-dom'
import { ChangeHeader, ChangesTab, RequestEntry } from './Head'

// Record ids are UUIDv7 strings.
const CHANGE = '00000000-0001-7000-8000-000000000000'

const view = (state: 'landing' | 'abandoned'): ChangeView => ({
  waitingOn: { kind: 'work', says: state === 'landing' ? 'working' : 'abandoned', tone: 'neutral', run: null, step: null, checks: 0, who: [], you: 'no' }, change: { id: CHANGE, number: 1, title: 'Game', state, stateLabel: state, body: '', branch: 'change' },
  statusLabel: state === 'landing' ? 'working' : 'abandoned', evidence: { merged: true }, editing: 0, runs: [], commits: [],
} as unknown as ChangeView)

const empty = 'No committed changes since this change started.'
describe('change state and committed evidence', () => {
  it.each([['landing', 'working'], ['abandoned', 'abandoned']] as const)('keeps %s visible even with a merge receipt', (state, label) => {
    const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={newQueryClient()}><ChangeHeader view={view(state)} /></QueryClientProvider></MemoryRouter>)
    expect(html).toContain(`${label}</span>`)
    expect(html).not.toContain('>merged</span>')
  })
  // Who asked is said only when the record names them; an older change
  // whose opener was never recorded says nothing rather than guess.
  it('says who asked for the change only when the record names them', () => {
    const head = (v: ChangeView) => renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={newQueryClient()}><ChangeHeader view={v} /></QueryClientProvider></MemoryRouter>)
    const known = view('landing')
    known.change.openedBy = 'alice via mcp/claude'
    expect(head(known)).toContain('Asked by alice via mcp/claude')
    expect(head(view('landing'))).not.toContain('Asked by')
  })
  it('shows the precise empty message only after a successful comparison', () => {
    const footer = (v: ChangeView) => renderToStaticMarkup(<QueryClientProvider client={newQueryClient()}><ChangesTab view={v} /></QueryClientProvider>)
    const emptyView = { ...view('landing'), committedDiff: { status: 'empty' as const, message: empty } }
    expect(footer(emptyView)).toContain(empty)
    const unreadable = { ...view('landing'), committedDiff: { status: 'unavailable' as const, message: 'Branch unavailable' } }
    const html = footer(unreadable)
    expect(html).not.toContain(empty)
    expect(html).toContain('Branch unavailable')
    expect(footer(view('landing'))).not.toContain(empty)
  })
  // A change with no code of its own says so, and reads its release's diff
  // only when the comparison with the release before it was made.
  it('says a change with no code of its own shipped main, without asking for a branch diff', () => {
    const footer = (v: ChangeView) => renderToStaticMarkup(<QueryClientProvider client={newQueryClient()}><ChangesTab view={v} /></QueryClientProvider>)
    const said = 'This change has no code of its own: it shipped main as it stood at 5b9ec68, as release #16 to main.'
    const noCode = (since?: number) => ({ ...view('landing'), committedDiff: { status: 'no-code', message: said, shipped: { release: 16, since } } } as unknown as ChangeView)
    const first = footer(noCode())
    expect(first).toContain(said)
    expect(first).not.toContain('Loading…')
    expect(first).not.toContain(empty)
    expect(footer(noCode(15))).toContain('Loading…')
  })
})

it('uses a short display title for a request copied into the title, preserving human titles', () => {
  const v = view('landing')
  v.change.body = 'A ride calendar with dates and places for every ride in the coming season.'
  v.change.title = v.change.body.slice(0, 60)
  const render = () => renderToStaticMarkup(<QueryClientProvider client={newQueryClient()}><ChangeHeader view={v} /></QueryClientProvider>)
  expect(render()).toContain('</span> Ride calendar</h1>')
  v.change.title = 'Winter rides'
  expect(render()).toContain('</span> Winter rides</h1>')
})

import { agentReport, firstLine } from './Head'
it('keeps identifier underscores and removes self-reported verification lines', () => {
  const report = agentReport('Set WP_HOME for HTTPS.\nVerified with curl and npm test.\n- Verified with the preview.\nKept WP_SITEURL unchanged.')
  expect(report).toBe('Set WP_HOME for HTTPS.\nKept WP_SITEURL unchanged.')
  expect(firstLine(report)).toBe('Set WP_HOME for HTTPS.')
})

it('quotes only the short request and keeps its full record and ticket reachable', () => {
 const v = view('landing'); v.change.body = 'Fix Egg Catch.\nWHAT CONFORMANCE LOOKS LIKE: long rubric.'; v.change.externalRef = 'EGG-4'
 const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={newQueryClient()}><RequestEntry view={v} reload={() => {}} /></QueryClientProvider></MemoryRouter>)
 expect(html).toContain('Fix Egg Catch.')
 expect(html).not.toContain('WHAT CONFORMANCE LOOKS LIKE')
 expect(html).toContain(`/changes/${CHANGE}/records/request`)
 expect(html).toContain('EGG-4')
})

// The section under the change's diff is the latest run's own diff. A run
// that committed nothing (a landing, after a person saved the work) says so
// in words, never as an empty diff under the change's one-file diff.
it("says the latest run changed nothing of its own rather than showing an empty diff", () => {
  const RUN = '00000000-0014-7000-8000-000000000000'
  const client = newQueryClient()
  client.setQueryData([...getKey(`/runs/${RUN}/diff`), '#text'], '')
  const html = renderToStaticMarkup(<QueryClientProvider client={client}><ChangesTab view={view('landing')} run={{ id: RUN, number: 14 } as never} /></QueryClientProvider>)
  expect(html).toContain('What run 14 changed')
  expect(html).toContain('Run 14 changed no files of its own.')
  expect(html).not.toContain('(empty diff)')
})
