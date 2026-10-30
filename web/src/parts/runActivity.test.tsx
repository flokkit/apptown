// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { ActiveRunList, ChangeHistoryTable, ChangeRow, RunRow } from './runActivity'

// Record ids are UUIDv7 strings.
const CHANGE = '00000000-0021-7000-8000-000000000000'

const row = (over: Partial<ChangeRow> = {}): ChangeRow => ({
  changeId: CHANGE, changeNumber: 21, title: 'Change the page heading', type: 'feature', state: 'shipped', stateLabel: 'shipped', statusLabel: 'landed', completedRuns: 2, merged: true,
  project: 'demo-smoke', workflows: 'quick-change', runs: 2, when: '2026-10-01T08:50:00Z', tookS: 120, ...over,
})

// On a phone the history is a stack of cards (styles.css table.stack): the
// change's title leads each card and opens it from anywhere on the card,
// and the cells under it say what they are without the column heads.
describe('the change history on a phone', () => {
  const doc = () => {
    const el = document.createElement('div')
    el.innerHTML = renderToStaticMarkup(<MemoryRouter><ChangeHistoryTable changes={[row()]} showProject /></MemoryRouter>)
    return el
  }
  it('is a stacked table whose lead cell opens the change', () => {
    const el = doc()
    expect(el.querySelector('table')!.classList.contains('stack')).toBe(true)
    const lead = el.querySelectorAll('tbody tr td.stack-lead')
    expect(lead).toHaveLength(1)
    const open = lead[0].querySelector('a.tap-open')!
    expect(open.getAttribute('href')).toBe(`/changes/${CHANGE}`)
    expect(open.textContent).toContain('Change the page heading')
    expect(open.textContent).toContain('#21')
  })
  it('names a bare number by its column', () => {
    const took = [...doc().querySelectorAll('td')].find((td) => td.textContent === '2m')!
    expect(took.getAttribute('data-label')).toBe('took')
  })
})

// "1 completed runs" was on every Home and project history row.
describe('the change history counts runs in words', () => {
  const how = (over: Partial<ChangeRow>) => {
    const el = document.createElement('div')
    el.innerHTML = renderToStaticMarkup(<MemoryRouter><ChangeHistoryTable changes={[row(over)]} /></MemoryRouter>)
    return el.querySelectorAll('tbody td')[2].textContent
  }
  it('says one completed run, and two completed runs', () => {
    expect(how({ completedRuns: 1, runs: 1 })).toBe('1 completed run')
    expect(how({ completedRuns: 2, runs: 2 })).toBe('2 completed runs')
  })
  it('says one run when it cannot tell which completed', () => {
    expect(how({ completedRuns: undefined, runs: 1 })).toBe('1 run')
  })
})

// A blocked change that the server offers to close shows "Close it" beside
// its link; one it does not offer shows none.
describe('a blocked change on the needs-you list', () => {
  const blocked = (next: RunRow['next']): RunRow => ({
    runId: '00000000-0022-7000-8000-000000000000', runNumber: 3, changeId: CHANGE, changeNumber: 13, workflow: 'adopt', title: 'Adopt the way of working: html-game',
    status: 'running', stage: 'checks', state: 'blocked', statusLabel: 'failed' as const, project: 'demo-smoke', startedAt: '2026-10-01T08:50:00Z', durationS: 60, next,
  })
  const buttons = (r: RunRow) => {
    const el = document.createElement('div')
    el.innerHTML = renderToStaticMarkup(<QueryClientProvider client={newQueryClient()}><MemoryRouter><ActiveRunList runs={[r]} showProject /></MemoryRouter></QueryClientProvider>)
    return { text: [...el.querySelectorAll('button')].map((b) => b.textContent), link: el.querySelector('a')!.getAttribute('href') }
  }
  it('offers Close it when the server does, and keeps the row a link', () => {
    expect(buttons(blocked([{ act: 'abandon', stepId: null, step: '' }]))).toEqual({ text: ['Close it'], link: `/changes/${CHANGE}` })
  })
  it('offers nothing the server did not send', () => {
    expect(buttons(blocked([])).text).toEqual([])
  })
})
