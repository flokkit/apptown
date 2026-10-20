// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { newQueryClient, retryDelay } from '../data'
import { settled } from '../settled'
import WhatIfPage from './WhatIfPage'

// Record ids are UUIDv7 strings.
const WHAT_IF = '00000000-0007-7000-8000-000000000000'
const CHANGE = '00000000-0041-7000-8000-000000000000'
const LEDGER = '00000000-0001-7000-8000-000000000000'
const TILL = '00000000-0002-7000-8000-000000000000'
const SHOP = '00000000-0003-7000-8000-000000000000'
const MAIL = '00000000-0004-7000-8000-000000000000'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const whatIf = {
  id: WHAT_IF, pack: 'team', version: 2, requestedBy: 'bls', state: 'interrupted', detail: 'AppTown restarted', projectsTotal: 4, projectsDone: 3,
  createdAt: '2026-10-05T00:00:00Z', finishedAt: '', rubrics: true, spendLimitMicros: 500000, costMicros: 800000, incomplete: 1,
  rows: [
    { projectId: LEDGER, projectSlug: 'ledger', sha: '0123456789abcdef', adoptionRevision: 2, block: 'docs', check: 'readme', result: 'would-fail', proposed: 'does-not-conform', detail: 'no README' },
    { projectId: LEDGER, projectSlug: 'ledger', sha: '0123456789abcdef', adoptionRevision: 2, block: 'hygiene', check: 'fixme', result: 'unchanged', current: 'conforms', proposed: 'conforms' },
  ],
  projects: [
    { projectId: LEDGER, projectSlug: 'ledger', sha: '0123456789abcdef', adoptionRevision: 2, candidateDigest: 'd1', outcome: 'new-failures', fixed: 0,
      newFailures: [{ block: 'docs', check: 'readme' }, { block: 'deps', check: 'audit', exceptedToday: true }], notAssessed: [] },
    { projectId: TILL, projectSlug: 'till', sha: 'fedcba9876543210', adoptionRevision: 5, candidateDigest: 'd2', outcome: 'could-not-assess', fixed: 1, newFailures: [],
      notAssessed: [{ block: 'img', check: 'scan', reason: 'no-image', why: 'no image of the app at this commit could be used' }] },
    { projectId: SHOP, projectSlug: 'shop', sha: 'aaaaaaaaaaaaaaaa', adoptionRevision: 1, candidateDigest: 'd3', outcome: 'no-new-failures', fixed: 0, newFailures: [], notAssessed: [], joins: true },
    { projectId: MAIL, projectSlug: 'mail', outcome: 'not-evaluated', fixed: 0, newFailures: [], notAssessed: [] },
  ],
}

// fail is a server error answer: render sends it with status 503.
const fail = (error: string) => ({ __status: 503, error })

async function render(answer: (url: string, init?: RequestInit) => unknown) {
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const a = answer(String(url), init) as { __status?: number }
    return new Response(JSON.stringify(a), { status: a?.__status ?? 200 })
  }))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => {
    root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/conformance/what-ifs/${WHAT_IF}`]}><Routes><Route path="/conformance/what-ifs/:id" element={<WhatIfPage />} /></Routes></MemoryRouter></QueryClientProvider>)
  })
  await settled(client)
  return { el, client, done: () => { act(() => root.unmount()); client.clear() } }
}

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

  it('answers per project and says what it could not assess', async () => {
    const { el, done } = await render((url) => url.includes('/authz/me') ? { instance: {} } : url.includes('/packs/team') ? { recommended: 1 } : whatIf)
    const text = el.textContent ?? ''
    expect(text).toContain('What-if: team v2')
    expect(text).toContain('interrupted')
    expect(text).toContain('at least $0.80 of $0.50')
    expect(text).toContain('4 projects1 new failures1 could not assess1 no new failures1 not evaluated')
    const rows = [...el.querySelectorAll('tbody tr')].map((r) => r.textContent)
    expect(rows[0]).toContain('ledgernew failuresdocs · readmedeps · audit (excepted today)')
    expect(rows[0]).toContain('0123456789')
    expect(rows[1]).toContain('tillcould not assessimg · scan: no image of the app at this commit could be used1')
    expect(rows[2]).toContain('shopwould joinno new failures')
    expect(rows[3]).toContain('mailnot evaluated')
    expect(button(el, 'Recommend v2').disabled).toBe(true)
    expect(text).toContain('needs standards:manage-packs')
    expect(el.querySelector('a[href="/evals/new?pack=team&version=2"]')).not.toBeNull()
    await act(async () => { button(el, 'checks').click() })
    expect(el.textContent).toContain('would fail')
    expect(el.textContent).toContain('no README')
    expect(el.textContent).not.toContain('hygiene · fixme')
    done()
  })

  it('recommends the version for someone who manages packs', async () => {
    const posted: [string, unknown][] = []
    const { el, client, done } = await render((url, init) => {
      if (init?.method === 'POST') { posted.push([url, JSON.parse(String(init.body))]); return {} }
      return url.includes('/authz/me') ? { instance: { 'standards:manage-packs': true } } : url.includes('/packs/team') ? { recommended: 1 } : whatIf
    })
    await act(async () => { button(el, 'Recommend v2').click() })
    await act(async () => { button(el, 'Recommend it').click() })
    await settled(client)
    expect(posted).toEqual([['/api/v1/packs/team/recommended', { version: 2, whatIf: WHAT_IF }]])
    done()
  })

  // A read the controls depend on that failed is said as such: never as
  // "recommended" from an old answer, never as a missing grant when what the
  // caller holds is unknown (review WR-C01). Server errors are retried
  // twice; the fake clock runs that schedule out.
  const retried = async (client: { isFetching: () => number }) => {
    await act(async () => { await vi.advanceTimersByTimeAsync(retryDelay(0) + retryDelay(1)) })
    await vi.waitFor(() => expect(client.isFetching()).toBe(0))
  }

  it('says the pack could not be read, not that a version is recommended', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const { el, client, done } = await render((url) => url.includes('/authz/me') ? { instance: {} } : url.includes('/packs/team') ? fail('pack storage unavailable') : whatIf)
    await retried(client)
    expect(el.textContent).toContain('The pack could not be read: pack storage unavailable')
    expect(el.textContent).not.toContain('is recommended')
    expect(el.textContent).not.toContain('needs standards:manage-packs')
    expect(button(el, 'Recommend v2').disabled).toBe(true)
    vi.useRealTimers()
    done()
  })

  it('marks a pack answer whose refresh failed as out of date', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    let down = false
    const { el, client, done } = await render((url) => url.includes('/authz/me') ? { instance: {} } : url.includes('/packs/team') ? (down ? fail('pack storage unavailable') : { recommended: 2 }) : whatIf)
    expect(el.textContent).toContain('v2 is recommended')
    down = true
    await act(async () => { void client.refetchQueries({ queryKey: ['get', 'packs', 'team'] }) })
    await retried(client)
    expect(el.querySelector('[aria-label^="out of date"], .out-of-date')).not.toBeNull()
    vi.useRealTimers()
    done()
  })

  it('does not say a grant is missing when what the caller holds is unknown', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const { el, client, done } = await render((url) => url.includes('/authz/me') ? fail('identity unavailable') : url.includes('/packs/team') ? { recommended: 1 } : whatIf)
    await retried(client)
    expect(el.textContent).not.toContain('needs standards:manage-packs')
    expect(el.textContent).toContain('what you may do could not be read: identity unavailable')
    expect(button(el, 'Recommend v2').disabled).toBe(true)
    vi.useRealTimers()
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
        return { changeId: CHANGE, candidate: {} }
      }
      return url.includes('/authz/me') ? { instance: {} } : url.includes('/packs/team') ? { recommended: 1 } : whatIf
    })
    const box = (slug: string) => el.querySelector(`input[aria-label="adopt on ${slug}"]`) as HTMLInputElement
    expect(box('mail').disabled).toBe(true)
    expect(box('mail').title).toBe('not evaluated')
    expect(button(el, 'Adopt v2 on 0 projects').disabled).toBe(true)
    for (const slug of ['ledger', 'till', 'shop']) await act(async () => { box(slug).click() })
    await act(async () => { button(el, 'Adopt v2 on 3 projects').click() })
    expect(el.textContent).toContain('Opens one adoption Change per project, taking team v2 as assessed in this what-if.')
    await act(async () => { button(el, 'Open the Changes').click() })
    await settled(client)
    await vi.waitFor(() => expect(posted).toHaveLength(3))
    expect(posted[0]).toEqual(['/api/v1/projects/ledger/adoption/adopt', { fromWhatIf: WHAT_IF, viaChange: true }])
    expect(posted.map(([u]) => u.split('/')[4])).toEqual(['ledger', 'till', 'shop'])
    await vi.waitFor(() => expect(el.textContent).toContain('its Change'))
    const rows = [...el.querySelectorAll('tbody tr')].map((r) => r.textContent)
    expect(rows[0]).toContain('refused: the project is on revision 3, not 2 — prepare again')
    expect(rows[1]).toContain('not known whether it opened')
    expect(rows[1]).not.toContain('its Change')
    expect(rows[2]).toContain('its Change')
    expect(el.querySelectorAll('tbody tr')[2].querySelector(`a[href="/changes/${CHANGE}"]`)).not.toBeNull()
    expect(box('shop').disabled).toBe(true)
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
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        posted.push(String(url))
        await held
        return new Response(JSON.stringify({ changeId: CHANGE, candidate: {} }))
      }
      const body = String(url).includes('/authz/me') ? { instance: {} } : String(url).includes('/packs/team') ? { recommended: 1 } : whatIf
      return new Response(JSON.stringify(body))
    }))
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/conformance/what-ifs/${WHAT_IF}`]}><Routes><Route path="/conformance/what-ifs/:id" element={<WhatIfPage />} /></Routes></MemoryRouter></QueryClientProvider>)
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
    const running = { ...whatIf, state: 'running', projectsDone: 1, projectsTotal: 4,
      projects: whatIf.projects.map((p, i) => i === 0 ? p : { ...p, outcome: 'not-yet-judged', candidateDigest: undefined, newFailures: [], notAssessed: [] }) }
    const { el, done } = await render((url) => url.includes('/authz/me') ? { instance: {} } : url.includes('/packs/team') ? { recommended: 1 } : running)
    expect(el.textContent).toContain('running · 1 of 4')
    expect(el.textContent).toContain('3 not yet judged')
    expect(el.textContent).not.toContain('not evaluated')
    done()
  })

  it('does not offer to recommend the recommended version', async () => {
    const { el, done } = await render((url) => url.includes('/authz/me') ? { instance: { 'standards:manage-packs': true } } : url.includes('/packs/team') ? { recommended: 2 } : whatIf)
    expect(button(el, 'Recommend v2').disabled).toBe(true)
    expect(el.textContent).toContain('v2 is recommended')
    done()
  })
})
