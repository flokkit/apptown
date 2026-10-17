// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { ChangeHistoryTable, ChangeRow } from './runActivity'

// Record ids are UUIDv7 strings.
const CHANGE = '00000000-0021-7000-8000-000000000000'

const row = (over: Partial<ChangeRow> = {}): ChangeRow => ({
  changeId: CHANGE, changeNumber: 21, title: 'Change the page heading', type: 'feature', state: 'shipped', completedRuns: 2, merged: true,
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
