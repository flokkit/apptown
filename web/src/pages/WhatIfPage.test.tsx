// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { newQueryClient } from '../data'
import { withFeatures } from '../features.fixture'
import { settled } from '../settled'
import WhatIfPage from './WhatIfPage'

// Record ids are UUIDv7 strings.
const WHAT_IF = '00000000-0007-7000-8000-000000000000'
const CHANGE = '00000000-0041-7000-8000-000000000000'
const RUN = '00000000-0042-7000-8000-000000000000'
const LEDGER = '00000000-0001-7000-8000-000000000000'
const TILL = '00000000-0002-7000-8000-000000000000'
const SHOP = '00000000-0003-7000-8000-000000000000'
const MAIL = '00000000-0004-7000-8000-000000000000'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const whatIf = {
  id: WHAT_IF, pack: 'team', version: 2, outcomeLabels: { 'new-failures': 'failed', 'could-not-assess': 'unknown', 'no-new-failures': 'passed', 'not-evaluated': 'unknown', 'not-yet-judged': 'unknown' }, requestedBy: 'bls', statusLabel: 'stopped', state: 'interrupted', detail: 'AppTown restarted', projectsTotal: 4, projectsDone: 3,
  createdAt: '2026-10-05T00:00:00Z', finishedAt: '', rubrics: true, spendLimitMicros: 500000, costMicros: 800000, incomplete: 1, advanceWorkflows: true,
  movesBackProjects: 1,
  fleet: [{ block: 'img', check: 'scan', reason: 'no-image', words: 'no image of the app at this commit could be used', remedy: 'the app must ship an image', projects: 1 }],
  rows: [
    { projectId: LEDGER, projectSlug: 'ledger', sha: '0123456789abcdef', adoptionRevision: 2, block: 'lang/django', check: '', statusLabel: 'unknown', result: 'older-version', currentVersion: 31, proposedVersion: 27, detail: 'lang/django v31→v27' },
    { projectId: LEDGER, projectSlug: 'ledger', sha: '0123456789abcdef', adoptionRevision: 2, block: 'docs', check: 'readme', statusLabel: 'failed', result: 'would-fail', proposed: 'does-not-conform', detail: 'no README' },
    { projectId: LEDGER, projectSlug: 'ledger', sha: '0123456789abcdef', adoptionRevision: 2, block: 'hygiene', check: 'fixme', statusLabel: 'passed', result: 'unchanged', current: 'conforms', proposed: 'conforms' },
  ],
  projects: [
    { projectId: LEDGER, projectSlug: 'ledger', sha: '0123456789abcdef', adoptionRevision: 2, candidateDigest: 'd1', statusLabel: 'failed', outcome: 'new-failures', fixed: 0,
      newFailures: [{ block: 'docs', check: 'readme' }, { block: 'deps', check: 'audit', exceptedToday: true }], notAssessed: [], notAssessedBy: [],
      score: { checks: 3, judged: 3, passed: 1, failing: 2, couldNotAssess: 0, noLongerRequired: 0, newFailures: 2, fixed: 0, movesBack: 1, ruleChanges: 2 },
      moves: [{ rule: 'lang/django', from: 31, to: 27, back: true }, { rule: 'workflow implement', from: 40, to: 52, back: false }] },
    { projectId: TILL, projectSlug: 'till', sha: 'fedcba9876543210', adoptionRevision: 5, candidateDigest: 'd2', statusLabel: 'unknown', outcome: 'could-not-assess', fixed: 1, newFailures: [],
      notAssessed: [{ block: 'img', check: 'scan', reason: 'no-image', why: 'no image of the app at this commit could be used' }],
      notAssessedBy: [{ reason: 'no-image', words: 'no image of the app at this commit could be used', remedy: 'the app must ship an image', count: 1 }],
      score: { checks: 2, judged: 1, passed: 1, failing: 0, couldNotAssess: 1, noLongerRequired: 0, newFailures: 0, fixed: 1, movesBack: 0, ruleChanges: 0 }, moves: [] },
    { projectId: SHOP, projectSlug: 'shop', sha: 'aaaaaaaaaaaaaaaa', adoptionRevision: 1, candidateDigest: 'd3', statusLabel: 'passed', outcome: 'no-new-failures', fixed: 0, newFailures: [], notAssessed: [], notAssessedBy: [], score: { checks: 0, judged: 0, passed: 0, failing: 0, couldNotAssess: 0, noLongerRequired: 0, newFailures: 0, fixed: 0, movesBack: 0, ruleChanges: 0 }, moves: [], joins: true },
    { projectId: MAIL, projectSlug: 'mail', statusLabel: 'unknown', outcome: 'not-evaluated', fixed: 0, newFailures: [], notAssessed: [], notAssessedBy: [], score: { checks: 0, judged: 0, passed: 0, failing: 0, couldNotAssess: 0, noLongerRequired: 0, newFailures: 0, fixed: 0, movesBack: 0, ruleChanges: 0 }, moves: [] },
  ],
}

async function render(answer: (url: string, init?: RequestInit) => unknown) {
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string, init?: RequestInit) => {
    const a = answer(String(url), init) as { __status?: number }
    return new Response(JSON.stringify(a), { status: a?.__status ?? 200 })
  })))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => {
    root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/conformance/what-ifs/${WHAT_IF}`]}><Routes><Route path="/conformance/what-ifs/:id/projects/:slug" element={<WhatIfPage />} /><Route path="/conformance/what-ifs/:id" element={<WhatIfPage />} /></Routes></MemoryRouter></QueryClientProvider>)
  })
  await settled(client)
  return { el, client, done: () => { act(() => root.unmount()); client.clear() } }
}

// projectRows is the per-project table's rows.
// Each status as its word and its details (which open on hover or tap).
const chips = (root: Element) => [...root.querySelectorAll('.chip')].map((c) => `${c.textContent?.replace(/^●\s*/, '')}: ${c.getAttribute('data-help') ?? c.getAttribute('title') ?? ''}`)
const projectRows = (el: HTMLElement) => [...el.querySelectorAll('tbody tr')]

const button = (el: HTMLElement, text: string) => [...el.querySelectorAll('button')].find((b) => b.textContent?.startsWith(text)) as HTMLButtonElement

// A what-if's page is one row per project with its answer, worst first in
// the counts: new failures by check (an exception that stops covering one
// says so), what could not be assessed and why, fixes, the commit judged.
// A project the what-if did not reach is "not evaluated", never clear.
// Recommend is the pack page's act, greyed with why when the caller may
// not, or when the version is already recommended; the eval link names the
// version.
describe('the what-if page', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // A queued what-if's chip opens the owner's sentence in its own terms
  // ("Waiting its turn"), never the raw state "queued".
  it('explains a queued what-if as waiting, not queued', async () => {
    const queued = { ...whatIf, statusLabel: 'waiting', statusHelp: 'Waiting its turn: what-ifs and reviews run one at a time.', state: 'queued', detail: '', projectsDone: 0 }
    const v = await render((url) => url.includes('/authz/me') ? { instance: {} } : url.includes('/packs/team') ? { recommended: 1 } : queued)
    try {
      const head = chips(v.el).find((c) => c.startsWith('waiting'))
      expect(head).toBe('waiting: Waiting its turn: what-ifs and reviews run one at a time.')
      expect(chips(v.el).some((c) => c.endsWith(': queued'))).toBe(false)
    } finally { v.done() }
  })

  it('keeps phone answers brief without calling an unassessed project clear', async () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
    const { el, done } = await render(url => url.includes('/authz/me') ? { instance: {} } : url.includes('/packs/team') ? { recommended: 1 } : whatIf)
    try {
      const rows = projectRows(el)
      expect(rows).toHaveLength(4)
      expect(rows[0].textContent).toContain('failed')
      expect(chips(rows[3])).toEqual(['unknown: not evaluated'])
      expect(rows[0].textContent).not.toContain('0123456789')
      const link = rows[0].querySelector<HTMLAnchorElement>(`a[href="/conformance/what-ifs/${WHAT_IF}/projects/ledger"]`)!
      expect(link.textContent).toBe('Check details')
      await act(async () => link.click())
      expect(el.querySelector('h1')?.textContent).toBe('ledger')
      expect(el.textContent).toContain('0123456789')
      // Nothing folds away; a status's own details opening on tap is not a fold-down.
      expect(el.querySelector('details, [aria-expanded]:not(.chip-button)')).toBeNull()
    } finally { done() }
  })

  it('answers per project and says what it could not assess', async () => {
    const { el, done } = await render((url) => url.includes('/authz/me') ? { instance: {} } : url.includes('/packs/team') ? { recommended: 1 } : whatIf)
    const text = el.textContent ?? ''
    expect(text).toContain('What-if: team v2')
    expect(text).toContain('stopped')
    expect(text).toContain('at least $0.80 of $0.50')
    expect(text).toContain('4 projects1 ● failed1 ● unknown1 ● passed1 ● unknown1 move rules back')
    expect(chips(el).join(' | ')).toContain('failed: new failures | unknown: could not assess | passed: no new failures | unknown: not evaluated')
    expect(text).toContain("moves each project's other workflows to the newest version")
    // Across projects: the check, how many it is missing from, why, and what would make it assessable.
    expect(text).toContain('img · scan 1 of 4 — no image of the app at this commit could be usedTo assess: the app must ship an image')
    const rows = projectRows(el).map((r) => r.textContent)
    expect(rows[0]).toContain('ledger● failed3 checks · 1 pass · 2 failCheck details')
    expect(chips(projectRows(el)[0])).toEqual(['failed: new failures'])
    expect(rows[0]).not.toContain('0123456789')
    expect(rows[1]).toContain('till● unknown2 checks · 1 pass · 1 could not be checked1 fixed')
    expect(chips(projectRows(el)[1])).toEqual(['unknown: could not assess'])
    expect(projectRows(el)[1].querySelector('a[href$="/projects/till"]')).not.toBeNull()
    expect(rows[2]).toContain('shopwould join● passedno checks')
    expect(chips(projectRows(el)[2])).toEqual(['passed: no new failures'])
    expect(rows[3]).toContain('mail● unknown')
    expect(chips(projectRows(el)[3])).toEqual(['unknown: not evaluated'])
    expect(button(el, 'Recommend v2')).toBeUndefined()
    expect(el.querySelector(`a[href="/packs/team?whatIf=${WHAT_IF}"]`)).not.toBeNull()
    expect(el.querySelector('a[href="/evals/new?pack=team&version=2"]')).not.toBeNull()
    await act(async () => { (projectRows(el)[0].querySelector(`a[href="/conformance/what-ifs/${WHAT_IF}/projects/ledger"]`) as HTMLAnchorElement).click() })
    expect(el.textContent).toContain('1 rules move to an older version')
    expect(el.textContent).toContain('0123456789')
    expect(el.textContent).toContain('docs · readme')
    expect(el.textContent).toContain('deps · audit (exception approved)')
    expect(chips(el)).toContain('failed: would fail')
    expect(el.textContent).toContain('no README')
    expect(el.textContent).not.toContain('hygiene · fixme')
    // A rule move is its own line, the one going back first and marked; it is never shown as a check.
    expect(el.textContent).toContain('Rule changesmoves back: lang/django v31 → v27workflow implement v40 → v52')
    expect(el.textContent).not.toContain('lang/django · ')
    expect(el.textContent).not.toContain('not required → this version: not required')
    done()
  })

  it('links to the pack recommendation with the assessed what-if', async () => {
    const { el, done } = await render(() => whatIf)
    expect(el.querySelector(`a[href="/packs/team?whatIf=${WHAT_IF}"]`)?.textContent).toContain('Recommend v2')
    expect(button(el, 'Recommend')).toBeUndefined()
    done()
  })

  // Adopting asks each ticked project's own adopt door in turn, bound to
  // the candidate assessed; a project with none cannot be ticked. A refusal
  // is AppTown's words on that row and the next project is still asked;
  // an answer that never arrived (or a server failure) is "not known",
  // never "opened".
  it('adopts on the ticked projects, one request each, and says what came of each', async () => {
    const posted: [string, unknown][] = []
    const { el, client, done } = await render((url, init) => {
      if (init?.method === 'POST') {
        posted.push([url, JSON.parse(String(init.body))])
        if (url.includes('/projects/ledger/')) return { __status: 409, error: 'the project is on revision 3, not 2 — prepare again' }
        if (url.includes('/projects/till/')) return { __status: 503, error: 'database unavailable' }
        return { changeId: CHANGE, candidate: {}, workflow: 'quick-change', runId: RUN }
      }
      return url.includes('/authz/me') ? { instance: {} } : url.includes('/packs/team') ? { recommended: 1 } : whatIf
    })
    const box = (slug: string) => el.querySelector(`input[aria-label="adopt on ${slug}"]`) as HTMLInputElement
    expect(box('mail').disabled).toBe(true)
    expect(box('mail').title).toBe('not evaluated')
    expect(button(el, 'Adopt v2 on 0 projects').disabled).toBe(true)
    for (const slug of ['ledger', 'till', 'shop']) await act(async () => { box(slug).click() })
    await act(async () => { button(el, 'Adopt v2 on 3 projects').click() })
    expect(el.textContent).toContain("Opens one adoption Change per project, taking team v2 as assessed in this what-if, with each project's other workflows at the newest version. Each lands through its own review. 1 of 3 move rules back: ledger (lang/django v31 → v27).")
    await act(async () => { button(el, 'Open the Changes').click() })
    await settled(client)
    await vi.waitFor(() => expect(posted).toHaveLength(3))
    expect(posted[0]).toEqual(['/api/v1/projects/ledger/adoption/adopt', { fromWhatIf: WHAT_IF, viaChange: true }])
    expect(posted.map(([u]) => u.split('/')[4])).toEqual(['ledger', 'till', 'shop'])
    await vi.waitFor(() => expect(el.textContent).toContain('its Change'))
    const rows = projectRows(el).map((r) => r.textContent)
    expect(rows[0]).toContain('refused: the project is on revision 3, not 2 — prepare again')
    expect(rows[1]).toContain('not known whether it opened')
    expect(rows[1]).not.toContain('its Change')
    expect(rows[2]).toContain('its Change')
    expect(rows[2]).toContain('quick-change working')
    expect(projectRows(el)[2].querySelector(`a[href="/changes/${CHANGE}"]`)).not.toBeNull()
    expect(box('shop').disabled).toBe(true)
    done()
  })

  // A yes in advance: opening the confirmation reads each chosen project's
  // own question and shows its words (once for projects that share them);
  // each request sends back exactly what its project showed, and a project
  // that offers none is opened without one.
  it('shows each project its own words for a yes in advance and sends them back', async () => {
    const posted: [string, unknown][] = []
    const words: Record<string, unknown> = {
      ledger: { workflow: 'quick-change', acceptance: 'I accept it now', landing: ['I answer for it now'] },
      till: { workflow: 'quick-change', acceptance: 'I accept it now', landing: ['Ship it if clean'] },
      shop: { workflow: '', acceptance: '', landing: [], notOffered: 'this project has no quick-change or implement workflow' },
    }
    const { el, client, done } = await render((url, init) => {
      if (init?.method === 'POST') {
        posted.push([url, JSON.parse(String(init.body))])
        return { changeId: CHANGE, candidate: {}, workflow: 'quick-change', runId: RUN }
      }
      const asked = url.match(/projects\/([^/]+)\/adoption\/in-advance/)
      if (asked) return words[asked[1]]
      return url.includes('/authz/me') ? { instance: {} } : url.includes('/packs/team') ? { recommended: 1 } : whatIf
    })
    const box = (slug: string) => el.querySelector(`input[aria-label="adopt on ${slug}"]`) as HTMLInputElement
    for (const slug of ['ledger', 'till', 'shop']) await act(async () => { box(slug).click() })
    const tick = [...el.querySelectorAll('label')].find((l) => l.textContent?.includes('Approve now'))?.querySelector('input') as HTMLInputElement
    await act(async () => { tick.click() })
    await act(async () => { button(el, 'Adopt v2 on 3 projects').click() })
    await vi.waitFor(() => expect(el.querySelectorAll('[data-testid="in-advance-words"]')).toHaveLength(2))
    expect(el.textContent).toContain('ledger: you say yes now, meaning:“I accept it now”“I answer for it now”')
    expect(el.textContent).toContain('till: you say yes now, meaning:“I accept it now”“Ship it if clean”')
    expect(el.textContent).toContain('No yes in advance on shop: this project has no quick-change or implement workflow.')
    await act(async () => { button(el, 'Open the Changes').click() })
    await settled(client)
    await vi.waitFor(() => expect(posted).toHaveLength(3))
    expect(posted[0]).toEqual(['/api/v1/projects/ledger/adoption/adopt', { fromWhatIf: WHAT_IF, viaChange: true, inAdvance: { shown: ['I accept it now', 'I answer for it now'] } }])
    expect(posted[1]).toEqual(['/api/v1/projects/till/adoption/adopt', { fromWhatIf: WHAT_IF, viaChange: true, inAdvance: { shown: ['I accept it now', 'Ship it if clean'] } }])
    expect(posted[2]).toEqual(['/api/v1/projects/shop/adoption/adopt', { fromWhatIf: WHAT_IF, viaChange: true }])
    done()
  })

  // Opening an adoption Change starts its working run; when none could be
  // started the row says so and where to start one, never a bare link.
  it('says when an opened adoption Change has no run', async () => {
    const { el, client, done } = await render((url, init) => {
      if (init?.method === 'POST') return { changeId: CHANGE, candidate: {}, notStarted: 'this project has no quick-change or implement workflow to work the adoption on' }
      return url.includes('/authz/me') ? { instance: {} } : url.includes('/packs/team') ? { recommended: 1 } : whatIf
    })
    await act(async () => { (el.querySelector('input[aria-label="adopt on shop"]') as HTMLInputElement).click() })
    await act(async () => { button(el, 'Adopt v2 on 1 project').click() })
    await act(async () => { button(el, 'Open the Changes').click() })
    await settled(client)
    await vi.waitFor(() => expect(el.textContent).toContain('not started: this project has no quick-change or implement workflow'))
    expect(el.textContent).toContain('start a workflow from the change')
    done()
  })

  // A run belongs to the page and the person who confirmed it: when the
  // page goes (as it does when the person acting changes) no further
  // project is asked, so nobody's request goes out under another's name
  // (review WA-C01). Opening a Change refreshes what AppTown holds about
  // projects and Changes (WA-C02).
  it('asks no further project once the page has gone', async () => {
    const posted: string[] = []
    let release: () => void = () => {}
    const held = new Promise<void>((r) => { release = r })
    vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        posted.push(String(url))
        await held
        return new Response(JSON.stringify({ changeId: CHANGE, candidate: {} }))
      }
      const body = String(url).includes('/authz/me') ? { instance: {} } : String(url).includes('/packs/team') ? { recommended: 1 } : whatIf
      return new Response(JSON.stringify(body))
    })))
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/conformance/what-ifs/${WHAT_IF}`]}><Routes><Route path="/conformance/what-ifs/:id/projects/:slug" element={<WhatIfPage />} /><Route path="/conformance/what-ifs/:id" element={<WhatIfPage />} /></Routes></MemoryRouter></QueryClientProvider>)
    })
    await settled(client)
    for (const slug of ['ledger', 'shop']) await act(async () => { (el.querySelector(`input[aria-label="adopt on ${slug}"]`) as HTMLInputElement).click() })
    await act(async () => { button(el, 'Adopt v2 on 2 projects').click() })
    await act(async () => { button(el, 'Open the Changes').click() })
    await vi.waitFor(() => expect(posted).toHaveLength(1))
    act(() => root.unmount())
    release()
    await new Promise((r) => setTimeout(r, 50))
    expect(posted).toEqual(['/api/v1/projects/ledger/adoption/adopt'])
    client.clear()
  })

  it('refreshes projects and Changes after opening one', async () => {
    const { el, client, done } = await render((url, init) => {
      if (init?.method === 'POST') return { changeId: CHANGE, candidate: {} }
      return url.includes('/authz/me') ? { instance: {} } : url.includes('/packs/team') ? { recommended: 1 } : whatIf
    })
    client.setQueryData(['get', 'projects', 'shop', 'overview'], { slug: 'shop' })
    client.setQueryData(['get', 'changes'], [])
    await act(async () => { (el.querySelector('input[aria-label="adopt on shop"]') as HTMLInputElement).click() })
    await act(async () => { button(el, 'Adopt v2 on 1 project').click() })
    await act(async () => { button(el, 'Open the Changes').click() })
    await vi.waitFor(() => expect(el.textContent).toContain('its Change'))
    expect(client.getQueryState(['get', 'projects', 'shop', 'overview'])?.isInvalidated).toBe(true)
    expect(client.getQueryState(['get', 'changes'])?.isInvalidated).toBe(true)
    done()
  })

  // A what-if still running says how far it has got; a project it has not
  // reached is not yet judged, never "not evaluated", which says it ended.
  it('says a running what-if is running and its unreached projects not yet judged', async () => {
    const running = { ...whatIf, statusLabel: 'working', statusHelp: 'Working: comparing each project with the new version.', state: 'running', projectsDone: 1, projectsTotal: 4,
      outcomeLabels: { ...whatIf.outcomeLabels, 'not-yet-judged': 'waiting' },
      projects: whatIf.projects.map((p, i) => i === 0 ? p : { ...p, statusLabel: 'waiting', outcome: 'not-yet-judged', candidateDigest: undefined, newFailures: [], notAssessed: [], notAssessedBy: [] }) }
    const { el, done } = await render((url) => url.includes('/authz/me') ? { instance: {} } : url.includes('/packs/team') ? { recommended: 1 } : running)
    expect(el.textContent).toContain('● in progress 1 of 4')
    expect(chips(el)[0]).toBe('in progress: Working: comparing each project with the new version.')
    // A project it has not reached is waiting, not unknown.
    expect(el.textContent).toContain('3 ● waiting')
    expect(chips(el)).toContain('waiting: not yet judged')
    expect(el.textContent).not.toContain('not evaluated')
    expect(chips(el).join(' | ')).not.toContain('not evaluated')
    done()
  })


})
