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
    const cells = [...el.querySelectorAll('tbody td')].map((td) => td.textContent)
    expect(cells.join(' ')).toContain('AppTown merged main (abcdef01) into this change before step rework, as commit 01234567.')
    expect(el.querySelector('tbody code')).toBeNull()
  })
})
