// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { newQueryClient } from '../../data'
import { settled } from '../../settled'
import { PackUpgrades, WhatIfsList } from './WhatIfs'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const ONE = '00000000-0007-7000-8000-000000000000'
const TWO = '00000000-0006-7000-8000-000000000000'
const project = (outcome: string, movesBack = 0) => ({ projectId: '', projectSlug: 'p', statusLabel: 'unknown', outcome, newFailures: [], notAssessed: [], notAssessedBy: [], moves: [], fixed: 0,
  score: { checks: 0, judged: 0, passed: 0, failing: 0, couldNotAssess: 0, noLongerRequired: 0, newFailures: 0, fixed: 0, movesBack, ruleChanges: movesBack } })

// Each status as its word and its details (which open on hover or tap).
const chips = (root: Element) => [...root.querySelectorAll('.chip')].map((c) => `${c.textContent?.replace(/^●\s*/, '')}: ${c.getAttribute('data-help') ?? c.getAttribute('title') ?? ''}`).join(' | ')

async function render(body: unknown) {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(body))))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><WhatIfsList /></MemoryRouter></QueryClientProvider>) })
  await settled(client)
  return { el, done: () => { act(() => root.unmount()); client.clear() } }
}

// The what-ifs list on Pack upgrades is the way back to a what-if: newest
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
    const rowEls = [...el.querySelectorAll('tbody tr')]
    const rows = rowEls.map((r) => r.textContent)
    expect(rows[0]).toContain('apptown v35 · and other workflows to newest')
    expect(rows[0]).toContain('1 ● failed1 ● unknown1 move rules back')
    expect(chips(rowEls[0])).toContain('failed: new failures | unknown: could not assess')
    expect(rows[1]).toContain('in progress')
    expect(rows[1]).toContain('1 ● passed2 ● unknown')
    expect(chips(rowEls[1])).toContain('passed: no new failures | unknown: not yet judged')
    expect(el.querySelector(`a[href="/conformance/what-ifs/${ONE}"]`)).not.toBeNull()
    done()
  })

  it('says when there are none', async () => {
    const { el, done } = await render({ whatIfs: [] })
    expect(el.textContent).toContain('None run yet.')
    done()
  })
})

// Pack upgrades leads with the projects on an older version of a pack, and
// tries the recommended version on one in a press, changing nothing in it.
it('lists projects behind and tries the upgrade on one', async () => {
  const calls: { url: string; body?: string }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, body: init?.body as string | undefined })
    return new Response(JSON.stringify(url.endsWith('/authz/me') ? { instance: { 'conformance:review': true } } : url.endsWith('/wow') ? { packs: [{ pack: 'house', enabled: true, recommended: 2, versions: [{ version: 1, projects: ['old'] }, { version: 2, projects: ['new'] }] }] }
      : url.endsWith('/conformance/what-ifs') ? { whatIfs: [] } : url.endsWith('/conformance/what-if') ? { id: ONE } : {}))
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><PackUpgrades /></MemoryRouter></QueryClientProvider>) })
    await settled(client)
    const behind = el.querySelector('section[aria-label="Projects behind"]')!
    expect(behind.querySelectorAll('tbody tr')).toHaveLength(1)
    expect(behind.querySelector('tbody tr')!.textContent).toContain('old')
    await act(async () => { [...behind.querySelectorAll('button')].find(b => b.textContent === 'Try the upgrade')!.click() })
    await settled(client)
    const started = calls.find(c => c.url.endsWith('/conformance/what-if') && c.body)
    expect(JSON.parse(started!.body!)).toMatchObject({ pack: 'house', version: 2, projects: ['old'] })
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

// A what-if can try a pack's only version on projects not yet on it (a pack
// version's own page offers the same), so a single-version pack is offered.
it('offers a pack with one version to try', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith('/authz/me') ? { instance: { 'conformance:review': true } }
    : url.endsWith('/wow') ? { packs: [{ pack: 'conventions-only', enabled: true, recommended: 1, versions: [{ version: 1, projects: ['club'] }] }] }
    : url.endsWith('/conformance/what-ifs') ? { whatIfs: [] } : {}))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><PackUpgrades /></MemoryRouter></QueryClientProvider>) })
    await settled(client)
    expect(el.textContent).not.toContain('nothing to try')
    expect([...el.querySelectorAll('option')].map(o => o.textContent)).toContain('conventions-only')
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
