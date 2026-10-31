// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, RouterProvider, createMemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient, getKey } from '../data'
import { settled } from '../settled'
import Conformance from './Conformance'

// Record ids are UUIDv7 strings.
const REVIEW = '00000000-0001-7000-8000-000000000000'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

describe('following a conformance review', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

  // Reproduction from the 28 September review. The cached list still says the
  // review is running while its refresh is in flight, and the cached detail
  // says it is done. The page must not follow it again from the stale list:
  // before, it followed, ended and followed again until React stopped it.
  it('finishing a review refreshes the list once and does not follow it again', async () => {
    const client = newQueryClient()
    const review = { id: REVIEW, status: 'running', projects: 0, projectsTotal: 0, verdicts: 0, errors: 0, cost: 0, startedAt: '2026-09-28T00:00:00Z', finishedAt: '', actor: 'reviewer' }
    const done = { ...review, status: 'done' }
    const view = { lens: { kind: 'current', review }, projects: [], reviews: [review], summary: { projects: 0, byStanding: {}, findings: 0, flagged: 0, high: 0, checks: 0 } }
    client.setQueryData(getKey('/conformance'), view)
    client.setQueryData(getKey(`/conformance/reviews/${REVIEW}`), { review: done, verdicts: [] })
    // The list's refreshes are held until the test lets them answer, so the
    // stale list is what the page sees for as long as the test looks.
    let reads = 0
    const held: (() => void)[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.endsWith('/conformance')) {
        reads++
        if (reads > 12) throw new Error('review repeatedly restarted')
        await new Promise<void>((r) => { held.push(r) })
        return new Response(JSON.stringify({ ...view, lens: { kind: 'current', review: done }, reviews: [done] }))
      }
      if (url.endsWith(`/conformance/reviews/${REVIEW}`)) return new Response(JSON.stringify({ review: done, verdicts: [] }))
      return new Response('{}')
    }))
    const original = console.error
    vi.spyOn(console, 'error').mockImplementation((...args) => {
      if (String(args[0]).includes('Maximum update depth exceeded')) throw new Error('review completion entered an infinite update loop')
      original(...args)
    })
    const el = document.createElement('div')
    const root = createRoot(el)
    try {
      await act(async () => {
        root.render(<QueryClientProvider client={client}><MemoryRouter><Conformance tab="remediation" /></MemoryRouter></QueryClientProvider>)
      })
      // The refresh is in flight: the list still says running, the detail
      // says done, and the page has followed and ended once.
      await vi.waitFor(() => expect(held.length).toBeGreaterThan(0))
      await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
      expect(client.getQueryData<typeof view>(getKey('/conformance'))?.lens.review.status).toBe('running')
      expect(reads).toBeLessThanOrEqual(2)
      // The refreshed list arrives, saying done: nothing follows it again.
      await act(async () => { for (const answer of held.splice(0)) answer() })
      await settled(client)
      expect(client.getQueryData<typeof view>(getKey('/conformance'))?.lens.review.status).toBe('done')
      expect(reads).toBeLessThanOrEqual(2)
    } finally {
      act(() => root.unmount())
      client.clear()
    }
  }, 3000)
})

describe('a queued conformance review', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // A review waiting for the conformance queue's worker is followed like a
  // running one: the header says queued, and no second review is offered at
  // any moment. Every DOM change is checked, not only the last: the page once
  // committed the queued review beside "Run a review now" and followed it
  // from an effect a moment later, and a test that looked at the right moment
  // saw it (11 October).
  it('is followed and says queued', async () => {
    const client = newQueryClient()
    const review = { id: REVIEW, status: 'queued', trigger: 'manual', projects: 0, projectsTotal: 0, projectsDone: 0, verdicts: 0, errors: 0, cost: 0, startedAt: '2026-10-10T00:00:00Z', finishedAt: '', actor: 'reviewer', currentProject: '' }
    const view = { lens: { kind: 'current', review }, projects: [], reviews: [review], summary: { projects: 0, byStanding: {}, findings: 0, flagged: 0, high: 0, checks: 0 } }
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.endsWith('/conformance')) return new Response(JSON.stringify(view))
      if (url.endsWith(`/conformance/reviews/${REVIEW}`)) return new Response(JSON.stringify({ review, verdicts: [] }))
      return new Response('{}')
    }))
    const el = document.createElement('div')
    const root = createRoot(el)
    const offered: string[] = []
    const watch = new MutationObserver(() => {
      if (el.textContent?.includes('Run a review now') && el.textContent.includes('Queued')) offered.push(el.textContent)
    })
    watch.observe(el, { subtree: true, childList: true, characterData: true })
    try {
      await act(async () => {
        root.render(<QueryClientProvider client={client}><MemoryRouter><Conformance tab="remediation" /></MemoryRouter></QueryClientProvider>)
      })
      // The header's progress, not the lens banner's "Queued.", shows it followed.
      await vi.waitFor(() => expect(el.querySelector('.bar-track')?.parentElement?.textContent).toContain('Queued'))
      await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
      expect(offered, 'a second review offered beside the queued one').toEqual([])
      expect(el.textContent).not.toContain('Run a review now')
    } finally {
      watch.disconnect()
      act(() => root.unmount())
      client.clear()
    }
  }, 3000)
})

describe('a project page while its review refreshes', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

  const lanes = { conforms: 0, excepted: 0, findings: 0, flagged: 0, unassessed: 0, broken: 0, na: 0 }
  // Fixture ids are UUIDv7 strings made from a small number.
  const uuid = (n: number) => `00000000-${String(n).padStart(4, '0')}-7000-8000-000000000000`
  const verdict = (id: number, reviewId: number, check: string, state: string) => ({
    id: uuid(id), reviewId: uuid(reviewId), projectSlug: 'shop', sha: 'abc1234', block: 'web', version: 1, check, evaluator: 'sh', evaluatorVer: '1',
    mode: 'review', state, severity: 'low', detail: `${check} detail`, at: '2026-10-08T00:00:00Z',
  })
  const review = (id: number, status: string) => ({ id: uuid(id), status, trigger: 'manual', actor: 'bls', startedAt: '2026-10-08T00:00:00Z', finishedAt: '', projects: 1, verdicts: 0, errors: 0, cost: 0, projectsTotal: 1, projectsDone: 0, currentProject: 'shop' })
  // A running review's lens: each reading has judged one more check, so
  // successive answers differ the way a polled running review's do.
  const viewOf = (reviewId: number, judged: number) => {
    const vs = Array.from({ length: judged }, (_, i) => verdict(reviewId * 100 + i + 1, reviewId, `check-${i + 1}`, 'conforms'))
    return {
      lens: { kind: 'review', review: review(reviewId, 'done') },
      projects: [{ projectId: uuid(1), slug: 'shop', name: 'Shop', standing: 'conforms', sentence: 'meets everything', lanes: { ...lanes, conforms: judged }, total: judged, judged, high: 0, adopted: ['web'], sha: 'abc1234', judgedAt: '2026-10-08T00:00:00Z' }],
      byProject: { shop: vs }, byCheck: {}, checkDefs: {}, exceptions: {}, adjudications: {}, priorConfirmations: {},
      summary: { projects: 1, byStanding: {}, findings: 0, flagged: 0, high: 0, checks: judged }, reviews: [],
    }
  }

  // The guarantee: a background refresh keeps the folds the person opened
  // (a lane and a check inside it); only showing another review starts over.
  // Before, the lanes were components made inside the page's render, so
  // every new answer remounted them folded.
  it('keeps open folds across a refresh and resets them for another review', async () => {
    const client = newQueryClient()
    let judged = 2
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      const m = /\/conformance\?review=00000000-(\d{4})-/.exec(url)
      if (m) return new Response(JSON.stringify(viewOf(Number(m[1]), judged)))
      return new Response('{}')
    }))
    const router = createMemoryRouter([
      { path: '/conformance/reviews/:reviewId/projects/:slug', element: <Conformance tab="project" /> },
    ], { initialEntries: [`/conformance/reviews/${uuid(1)}/projects/shop`] })
    const el = document.createElement('div')
    document.body.appendChild(el)
    const root = createRoot(el)
    const settle = () => settled(client)
    const lane = () => [...el.querySelectorAll('strong')].find((s) => s.textContent === 'Conforms')!.closest('.row') as HTMLElement
    const rowOf = (check: string) => [...el.querySelectorAll('span')].find((s) => s.textContent === check)?.closest('.row') as HTMLElement | undefined
    try {
      await act(async () => { root.render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>) })
      await settle()
      expect(rowOf('check-1')).toBeUndefined() // the Conforms lane starts folded
      await act(async () => { lane().click() })
      await act(async () => { rowOf('check-1')!.click() })
      expect(el.textContent).toContain('check-1 detail')

      judged = 3
      await act(async () => { await client.refetchQueries() })
      await settle()
      expect(lane().textContent, 'the refreshed answer is shown').toContain('(3)')
      expect(rowOf('check-1'), 'the opened lane stays open').toBeDefined()
      expect(el.textContent, 'the opened check stays open').toContain('check-1 detail')

      // Review 2's answer is already held, as after visiting it earlier, so
      // the page does not pass through loading on the way.
      client.setQueryData(getKey(`/conformance?review=${uuid(2)}`), viewOf(2, 3))
      await act(async () => { await router.navigate(`/conformance/reviews/${uuid(2)}/projects/shop`) })
      await settle()
      expect(lane(), 'review 2 is shown').toBeDefined()
      expect(rowOf('check-1'), 'another review starts with its lanes folded').toBeUndefined()
    } finally {
      act(() => root.unmount())
      el.remove()
      client.clear()
    }
  }, 5000)
})

// Starting a fleet check or fix must remain reachable away from Projects.
it.each(['projects', 'reviews', 'checks', 'remediations', 'ways', 'what-ifs'] as const)('keeps fleet actions available on %s', async tab => {
  const client = newQueryClient()
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith('/conformance') ? {
    lens: { kind: 'current' }, projects: [], reviews: [], summary: { findings: 2, flagged: 0, incompleteProjects: 0 }, byCheck: {}, checkDefs: {}, adjudications: {}, checks: [],
  } : url.endsWith('/wow') ? { packs: [], blocks: [] } : url.endsWith('/remediations') ? { remediations: [], occupancy: { workers: 2, busy: 0 } } : url.endsWith('/what-ifs') ? { whatIfs: [] } : {}))))
  const el = document.createElement('div'); const root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><Conformance tab={tab} /></MemoryRouter></QueryClientProvider>))
    await settled(client)
    expect([...el.querySelectorAll('button')].map(b => b.textContent)).toContain('Check now')
    expect([...el.querySelectorAll('button')].map(b => b.textContent)).toContain('Fix 2 problems')
  } finally { await act(async () => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
