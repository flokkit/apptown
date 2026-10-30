// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { newQueryClient } from '../../data'
import { settled } from '../../settled'
import { WhatIfsList } from './WhatIfs'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const ONE = '00000000-0007-7000-8000-000000000000'
const TWO = '00000000-0006-7000-8000-000000000000'
const project = (outcome: string, movesBack = 0) => ({ projectId: '', projectSlug: 'p', statusLabel: 'unknown', outcome, newFailures: [], notAssessed: [], notAssessedBy: [], moves: [], fixed: 0,
  score: { checks: 0, judged: 0, passed: 0, failing: 0, couldNotAssess: 0, noLongerRequired: 0, newFailures: 0, fixed: 0, movesBack, ruleChanges: movesBack } })

async function render(body: unknown) {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(body))))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><WhatIfsList /></MemoryRouter></QueryClientProvider>) })
  await settled(client)
  return { el, done: () => { act(() => root.unmount()); client.clear() } }
}

// The What-ifs list under Conformance is the way back to a what-if: newest
// first, which pack version, who asked and when, its state, and its answers
// counted by project — with how many projects it moves rules back on — each
// opening its own page.
describe('the what-ifs list', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('lists recent what-ifs with their answers and what moves back', async () => {
    const { el, done } = await render({ whatIfs: [
      { id: ONE, pack: 'apptown', version: 35, outcomeLabels: { 'new-failures': 'failed', 'could-not-assess': 'unknown', 'no-new-failures': 'passed', 'not-evaluated': 'unknown', 'not-yet-judged': 'unknown' }, requestedBy: 'bls', statusLabel: 'finished', state: 'done', createdAt: '2026-10-21T00:00:00Z', projectsDone: 2, projectsTotal: 2, advanceWorkflows: true, movesBackProjects: 1,
        projects: [project('new-failures', 2), project('could-not-assess')], rows: [], fleet: [] },
      { id: TWO, pack: 'team', version: 2, outcomeLabels: { 'new-failures': 'failed', 'could-not-assess': 'unknown', 'no-new-failures': 'passed', 'not-evaluated': 'unknown', 'not-yet-judged': 'unknown' }, requestedBy: 'jo', statusLabel: 'working', state: 'running', createdAt: '2026-10-20T00:00:00Z', projectsDone: 1, projectsTotal: 3, advanceWorkflows: false, movesBackProjects: 0,
        projects: [project('no-new-failures'), project('not-yet-judged'), project('not-yet-judged')], rows: [], fleet: [] },
    ] })
    const rows = [...el.querySelectorAll('tbody tr')].map((r) => r.textContent)
    expect(rows[0]).toContain('apptown v35and other workflows to newest')
    expect(rows[0]).toContain('1 ● failednew failures1 ● unknowncould not assess1 move rules back')
    expect(rows[1]).toContain('working')
    expect(rows[1]).toContain('1 ● passedno new failures2 ● unknownnot yet judged')
    expect(el.querySelector(`a[href="/conformance/what-ifs/${ONE}"]`)).not.toBeNull()
    done()
  })

  it('says when there are none', async () => {
    const { el, done } = await render({ whatIfs: [] })
    expect(el.textContent).toContain('No what-ifs yet.')
    done()
  })
})
