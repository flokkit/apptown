// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { newQueryClient } from '../../data'
import type { ChangeView } from '../../api'
import { ApproveEvidence, MainConflictLine, StatePanel, requestShortfall } from './StatePanel'

// Record ids are UUIDv7 strings.
const CHANGE = '00000000-0004-7000-8000-000000000000'

const evidence = (shortfall?: string) => renderToStaticMarkup(
  <MemoryRouter><QueryClientProvider client={newQueryClient()}>
    <ApproveEvidence changeId={CHANGE} soFar="The tab title changed." shortfall={shortfall} />
  </QueryClientProvider></MemoryRouter>)

describe('the approval says when the summary reads the work as short of the request', () => {
  // A change whose summary says the request is unfinished must say so next
  // to the approve buttons, not only in the prose above them.
  it('warns with the shortfall when the request is not met or only partly met', () => {
    expect(requestShortfall({ requestMet: 'no', requestGap: 'only the tab title changed' })).toBe('only the tab title changed.')
    expect(requestShortfall({ requestMet: 'partly', requestGap: '' })).toBe('it does only part of what was asked.')
    const html = evidence(requestShortfall({ requestMet: 'no', requestGap: 'only the tab title changed' }))
    expect(html).toContain('class="ch-shortfall"')
    expect(html).toContain('only the tab title changed')
  })
  // Met and unknown say nothing: unknown is not a warning, nor a pass.
  it.each(['yes', 'unknown', undefined])('says nothing when the reading is %s', (met) => {
    expect(requestShortfall({ requestMet: met, requestGap: 'ignored' })).toBeUndefined()
    expect(evidence(undefined)).not.toContain('ch-shortfall')
  })
})

// Main the forge could not merge in is said where the change's state is,
// with the files and the command that catches up in the chat.
describe('main that conflicts with the change', () => {
  it('names the files and the catch-up', () => {
    const html = renderToStaticMarkup(<MainConflictLine conflict={{ mainBranch: 'main', main: 'abc', step: 'rework', files: ['README.md'],
      catchUp: 'git fetch "$FORGE_MAIN_REMOTE" main && git merge --no-edit -m "Merge main into change-4" FETCH_HEAD' }} />)
    const el = document.createElement('div')
    el.innerHTML = html
    expect(el.textContent).toBe('main has moved and the forge could not merge it in before step rework: README.md conflicts. Catch up in the change\'s chat: git fetch "$FORGE_MAIN_REMOTE" main && git merge --no-edit -m "Merge main into change-4" FETCH_HEAD')
  })
})

describe('the state panel', () => {
  it('says main conflicts while the change has not caught up', () => {
    const conflict = { mainBranch: 'main', main: 'abc', step: 'rework', files: ['README.md'], catchUp: 'git fetch' }
    const view = { change: { id: CHANGE, number: 4, state: 'in_progress' }, projectSlug: 'demo', detail: null, stateDetail: '', runs: [], mainConflict: conflict } as unknown as ChangeView
    const html = (v: ChangeView) => renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={newQueryClient()}>
      <StatePanel view={v} onChange={() => {}} workTab={() => '/work'} />
    </QueryClientProvider></MemoryRouter>)
    expect(html(view)).toContain('the forge could not merge it in before step rework')
    expect(html({ ...view, mainConflict: null })).not.toContain('could not merge')
  })
})
