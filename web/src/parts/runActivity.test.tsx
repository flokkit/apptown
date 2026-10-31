// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
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

it('leaves run counts in the change record, keeping history to title, outcome and time', () => {
  const html = renderToStaticMarkup(<MemoryRouter><ChangeHistoryTable changes={[row()]} /></MemoryRouter>)
  expect(html).not.toContain('How it went')
  expect(html).not.toContain('completed run')
  expect(html).toContain('Change the page heading')
  expect(html).toContain('landed')
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

it.each(['failed', 'stopped', 'finished'] as const)('does not claim elapsed idle time as Took for %s changes', statusLabel => {
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><ChangeHistoryTable changes={[row({ state: 'abandoned', statusLabel, tookS: 458640 })]} /></MemoryRouter>)
  expect(el.querySelector('[data-label="took"]')).toBeNull()
  expect(el.textContent).not.toContain('127.4h')
})

it('keeps history source and duration behind the row on a phone', () => {
  vi.stubGlobal('matchMedia', () => ({ matches: true }))
  try {
    const el = document.createElement('div')
    el.innerHTML = renderToStaticMarkup(<MemoryRouter><ChangeHistoryTable changes={[row()]} showProject /></MemoryRouter>)
    expect(el.querySelector('.glance-name')?.textContent).toContain('Change the page heading')
    expect(el.querySelector('.glance-state')?.textContent).toContain('landed')
    expect(el.querySelector('tr[aria-expanded]')?.getAttribute('aria-expanded')).toBe('false')
    el.querySelectorAll('details').forEach(d => d.remove())
    expect(el.textContent).not.toContain('demo-smoke')
    expect(el.textContent).not.toContain('2m')
  } finally { vi.unstubAllGlobals() }
})
