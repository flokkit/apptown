// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { ActiveRunList, RunRow } from './runActivity'

// Record ids are UUIDv7 strings.
const CHANGE = '00000000-0021-7000-8000-000000000000'


describe('a blocked change on the needs-you list', () => {
  const blocked = (next: RunRow['next']): RunRow => ({ waitingOn: { kind: 'check', says: 'review failed', tone: 'bad', run: null, step: null, checks: 1, who: [], you: 'yes' },
    runId: '00000000-0022-7000-8000-000000000000', runNumber: 3, changeId: CHANGE, changeNumber: 13, workflow: 'adopt', title: 'Adopt the way of working: html-game',
    status: 'running', stage: 'checks', state: 'blocked', statusLabel: 'failed' as const, project: 'demo-smoke', startedAt: '2026-10-01T08:50:00Z', durationS: 60, next,
  })
  const buttons = (r: RunRow) => {
    const el = document.createElement('div')
    el.innerHTML = renderToStaticMarkup(<QueryClientProvider client={newQueryClient()}><MemoryRouter><ActiveRunList runs={[r]} showProject /></MemoryRouter></QueryClientProvider>)
    return { text: [...el.querySelectorAll('a.btn, button')].map((b) => b.textContent), link: el.querySelector('a')!.getAttribute('href') }
  }
  it('offers its next step, not Close it, and keeps the row a link', () => {
    expect(buttons(blocked([{ act: 'abandon', stepId: null, step: '' }]))).toEqual({ text: ['Fix it'], link: `/changes/${CHANGE}` })
  })
  it('offers Fix it for a failed change even when the list carries no acts', () => {
    expect(buttons(blocked([])).text).toEqual(['Fix it'])
  })
  it('offers nothing else the server did not send', () => {
    expect(buttons({ ...blocked([]), waitingOn: { ...blocked([]).waitingOn, kind: 'work', says: 'working', you: 'no' } } as RunRow).text).toEqual([])
  })
})
