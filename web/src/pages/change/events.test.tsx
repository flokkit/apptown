// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import type { ChangeView } from '../../api'
import { eventSentence } from './events'
import { HistoryTab } from './Tabs'

// Record ids are UUIDv7 strings.
const RUN = '00000000-0007-7000-8000-000000000000'
const EVENT = '00000000-0001-7000-8000-000000000000'

describe('eventSentence', () => {
  it('says AppTown merged main into the change', () => {
    expect(eventSentence({ kind: 'main.merged', payload: { by: 'forge', step: 'rework', mainBranch: 'main', main: 'abcdef0123456789', sha: '0123456789abcdef' } }))
      .toBe('AppTown merged main (abcdef01) into this change before step rework, as commit 01234567.')
  })
  it('names the files when main could not be merged', () => {
    expect(eventSentence({ kind: 'main.conflict', payload: { step: 'rework', mainBranch: 'main', main: 'abcdef0123456789', conflicts: ['README.md', 'app.go'] } }))
      .toBe("AppTown could not merge main (abcdef01) into this change before step rework: README.md, app.go conflict. Catch up in the change's chat.")
  })
  it('leaves other events as recorded', () => {
    expect(eventSentence({ kind: 'commit.created', payload: { sha: 'x' } })).toBeNull()
  })
})

// The change's history says AppTown's merge of main in plain words.
describe('the change history', () => {
  it('shows AppTown merging main as a sentence', () => {
    const view = { runs: [], detail: { run: { id: RUN, number: 7 }, events: [
      { id: EVENT, runId: RUN, at: '2026-10-03T09:00:00Z', kind: 'main.merged', payload: { by: 'forge', step: 'rework', mainBranch: 'main', main: 'abcdef0123456789', sha: '0123456789abcdef' } },
    ] } } as unknown as ChangeView
    const el = document.createElement('div')
    el.innerHTML = renderToStaticMarkup(<MemoryRouter><HistoryTab view={view} runHref={(id) => `/runs/${id}`} /></MemoryRouter>)
    const cells = [...el.querySelectorAll('.event-log .ch-event-detail')].map((td) => td.textContent)
    expect(cells.join(' ')).toContain('AppTown merged main (abcdef01) into this change before step rework, as commit 01234567.')
    expect(el.querySelector('.event-log code')).toBeNull()
  })
})


// Routine plumbing is kept apart from the history a person reads, folded
// at the end, and each routine event's exact record is still there.
it('folds routine events apart and keeps every exact record', () => {
  const kinds = ['integration.resolved', 'integration.resolved', 'step.failed', 'integration.resolved', 'mystery.kind']
  const view = { runs: [], detail: { events: kinds.map((kind, i) => ({ id: String(i), kind, at: '2026-10-03T09:00:00Z', payload: { record: i, step: 'style' } })) } } as unknown as ChangeView
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><HistoryTab view={view} runHref={() => '/work'} /></MemoryRouter>)
  const inline = [...el.querySelectorAll('.event-log')][0]
  expect(inline.querySelectorAll('.ch-event')).toHaveLength(1)
  expect(inline.textContent).toContain('Check failed')
  expect(inline.textContent).toContain('style')
  const routine = el.querySelector('details.ch-routine')!
  expect(routine.hasAttribute('open')).toBe(false)
  expect(routine.querySelector('summary')?.textContent).toBe('4 routine events')
  expect(routine.querySelectorAll('.ch-event')).toHaveLength(4)
  for (const i of [0, 1, 3, 4]) expect(routine.textContent).toContain(`"record": ${i}`)
  expect(el.textContent).not.toContain('Activity recorded ×')
})

// A run still going on the latest attempt shows the change's state.
it('shows the latest run with the change state, not merely in progress', () => {
  const view = { statusLabel: 'failed', runs: [{ id: 'r1', number: 1, status: 'running', statusLabel: 'running', workflowName: 'land', role: 'landing', createdAt: '2026-10-03T09:00:00Z', updatedAt: '2026-10-03T09:00:00Z' }],
    detail: { run: { id: 'r1' }, events: [] } } as unknown as ChangeView
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><HistoryTab view={view} runHref={() => '/work'} /></MemoryRouter>)
  expect(el.textContent).toContain('failed')
  expect(el.textContent).not.toContain('in progress')
})
