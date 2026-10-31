// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { newQueryClient } from '../data'
import { settled } from '../settled'
import { EvalScorecardPage, EvalStartPage } from './Evals'

// Record ids are UUIDv7 strings.
const EVAL = '00000000-0007-7000-8000-000000000000'
const NEW_EVAL = '00000000-0009-7000-8000-000000000000'
const ARM1 = '00000000-0001-7000-8000-000000000000'
const ARM2 = '00000000-0002-7000-8000-000000000000'
const PROJECT = '00000000-0001-7000-8000-000000000000'
const CHANGE = '00000000-0012-7000-8000-000000000000'
const OPEN_CHANGE = '00000000-0013-7000-8000-000000000000'
const COPY1 = '00000000-0040-7000-8000-000000000000'
const COPY1_CHANGE = '00000000-0041-7000-8000-000000000000'
const COPY2 = '00000000-0042-7000-8000-000000000000'
const COPY2_CHANGE = '00000000-0043-7000-8000-000000000000'
const COMPLAINT3 = '00000000-0003-7000-8000-000000000000'
const COMPLAINT4 = '00000000-0004-7000-8000-000000000000'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const MEANING = 'I start this eval; its throwaway copies may land and deploy to the eval domain.'

const scorecard = {
  id: EVAL, startedBy: 'alice', startedAt: new Date().toISOString(), meaning: MEANING, subjectKind: 'model', subject: 'frontier',
  budgetMicros: 5_000_000, state: 'held', statusLabel: 'waiting for you', reason: '1 model call spent an unknown amount', costMicros: 1_250_000, incomplete: 1,
  tallies: [], comparable: 0, notComparable: 0,
  arms: [
    { id: ARM1, sourceProjectId: PROJECT, sourceChangeId: CHANGE, sourceChangeNumber: 12, arm: 'baseline', profile: 'standard', models: {}, state: 'scored', statusLabel: 'finished', projectId: COPY1, changeId: COPY1_CHANGE, changeNumber: 1, declares: [],
      score: { readyToLand: true, landed: true, deploy: 'deployed', checks: [{ step: 'tests', type: 'tests', status: 'pass', statusLabel: 'passed' }], requestMet: 'yes', reworks: 1, workSeconds: 600 } },
    { id: ARM2, sourceProjectId: PROJECT, sourceChangeId: CHANGE, sourceChangeNumber: 12, arm: 'variant', profile: 'frontier', models: {}, state: 'failed', statusLabel: 'failed', reason: 'its run ended before the work was ready to land', projectId: COPY2, changeId: COPY2_CHANGE, changeNumber: 1, declares: [],
      score: { readyToLand: false, landed: false, deploy: 'not reached', checks: [], reworks: 2 } },
  ],
  armCosts: { [ARM1]: { costMicros: 1_000_000, tokens: 10, incomplete: 0 }, [ARM2]: { costMicros: 250_000, tokens: 5, incomplete: 1 } },
  complaints: [
    { id: COMPLAINT3, about: 'way-of-working', packVersion: 'go-small@4', armId: ARM2, text: 'the pack asks for tables', source: 'run', seen: false, state: 'new', at: '' },
    { id: COMPLAINT4, about: 'apptown', armId: ARM2, text: 'the CLI hung', source: 'run', seen: false, state: 'new', at: '' },
  ],
}

async function render(path: string, page: React.ReactNode, route: string) {
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => {
    root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><Routes><Route path={route} element={page} /></Routes></MemoryRouter></QueryClientProvider>)
  })
  // settle waits for the page's reads to answer, then requires what the
  // test was waiting for: a step that never comes fails here.
  const settle = async (until: () => boolean) => {
    await settled(client)
    expect(until()).toBe(true)
  }
  return { el, settle, done: () => { act(() => root.unmount()); client.clear() } }
}

describe('the eval scorecard', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // Unknown spend is never shown as a known total, a held eval offers its
  // starter Continue, and complaints are grouped by what they are about.
  it('says "at least", offers Continue when held, and groups complaints by about', async () => {
    const calls: [string, string][] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      calls.push([init?.method ?? 'GET', url])
      return new Response(JSON.stringify(url.endsWith('/continue') ? { ...scorecard, state: 'running' } : scorecard))
    }))
    const r = await render(`/evals/${EVAL}`, <EvalScorecardPage />, '/evals/:id')
    try {
      await r.settle(() => !!r.el.textContent?.includes('Continue'))
      const text = r.el.textContent ?? ''
      expect(text).toContain('Spent at least $1.25 of $5.00')
      expect(text).toContain('at least $0.25')
      expect(text).toContain('About the way of working (1)')
      expect(text).toContain('go-small@4')
      expect(text).toContain('About AppTown (1)')
      expect(text).toContain('acceptance is not measured')
      // Changes are named by their numbers: the source and each copy's.
      expect(text).toContain('Change 12, re-run')
      expect(text).toContain("the copy's Change 1")
      expect(text).toContain('Change 12 varied')
      const button = [...r.el.querySelectorAll('button')].find((b) => b.textContent === 'Continue')!
      await act(async () => { button.click() })
      await r.settle(() => calls.some(([m, u]) => m === 'POST' && u.endsWith(`/evals/${EVAL}/continue`)))
      expect(calls.some(([m, u]) => m === 'POST' && u.endsWith(`/evals/${EVAL}/continue`))).toBe(true)
    } finally {
      r.done()
    }
  })

  // A just-started eval: every arm queued or working, none scored.
  it('shows an eval whose arms have no score yet', async () => {
    const started = { ...scorecard, state: 'running', statusLabel: 'working', reason: '', costMicros: 0, incomplete: 0, armCosts: {}, complaints: [],
      arms: [
        { id: ARM1, sourceProjectId: PROJECT, sourceChangeId: CHANGE, arm: 'baseline', profile: 'standard', models: {}, state: 'working', statusLabel: 'working', projectId: COPY1, changeId: COPY1_CHANGE, declares: [], score: { readyToLand: false, landed: false, checks: [], reworks: 0 } },
        { id: ARM2, sourceProjectId: PROJECT, sourceChangeId: CHANGE, arm: 'variant', profile: 'frontier', models: {}, state: 'queued', statusLabel: 'waiting', declares: [], score: { readyToLand: false, landed: false, checks: null, reworks: 0 } },
      ] }
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(started))))
    const r = await render(`/evals/${EVAL}`, <EvalScorecardPage />, '/evals/:id')
    try {
      await r.settle(() => !!r.el.textContent?.includes('waiting'))
      expect(r.el.textContent).toContain('working')
      expect(r.el.textContent).toContain('waiting')
    } finally {
      r.done()
    }
  })

  // A score that could not be read is not known: none of its facts is
  // shown as no or zero, and cost and complaints, read on their own, stay.
  it('does not show an unreadable stored score as known facts', async () => {
    const unread = { ...scorecard, arms: [scorecard.arms[0], { ...scorecard.arms[1], state: 'scored', reason: '',
      score: { readyToLand: false, landed: false, checks: [], reworks: 0, notComparable: true,
        unreadable: 'its stored score could not be read: json: cannot unmarshal string into Go struct field Score.landed of type bool' } }] }
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(unread))))
    const r = await render('/evals/7', <EvalScorecardPage />, '/evals/:id')
    try {
      await r.settle(() => !!r.el.textContent?.includes('stored score could not be read'))
      const card = [...r.el.querySelectorAll('.eval-arm')].find((c) => c.textContent?.includes('stored score could not be read'))!
      const facts = card.querySelectorAll('dt')
      const pairs = [...facts].map((dt) => [dt.textContent, dt.nextElementSibling?.textContent])
      expect(pairs).not.toContainEqual(['Landed', 'no'])
      expect(pairs).not.toContainEqual(['Ready to land', 'no'])
      expect(pairs).not.toContainEqual(['Reworks', '0'])
      expect(pairs.map(([t]) => t)).toEqual(['Score', 'Cost', 'Complaints'])
    } finally {
      r.done()
    }
  })

  it('says a person who may not read evals cannot', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'no' }), { status: 403 })))
    const r = await render(`/evals/${EVAL}`, <EvalScorecardPage />, '/evals/:id')
    try {
      await r.settle(() => !!r.el.textContent?.includes('manage packs'))
      expect(r.el.textContent).toContain('Evals are read by standards administrators and auditors')
    } finally {
      r.done()
    }
  })
})

describe('starting an eval', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // The one click comes after the meaning is shown, and the meaning shown
  // is what is sent.
  it('shows what starting means before the one click, and sends it', async () => {
    const posts: unknown[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        posts.push(JSON.parse(String(init.body)))
        return new Response(JSON.stringify({ id: NEW_EVAL, arms: [] }), { status: 201 })
      }
      if (url.includes('/evals/start')) return new Response(JSON.stringify({ meaning: MEANING, profiles: ['standard', 'frontier'], packs: [], maxChanges: 5 }))
      if (url.includes('/changes')) return new Response(JSON.stringify([{ id: CHANGE, number: 12, title: 'Add search', state: 'shipped', baseSha: 'abc' }, { id: OPEN_CHANGE, number: 13, title: 'Open', state: 'in_progress' }]))
      if (url.includes('/projects')) return new Response(JSON.stringify([{ slug: 'shop', name: 'Shop' }]))
      return new Response('{}')
    }))
    const r = await render('/evals/new', <EvalStartPage />, '/evals/new')
    try {
      await r.settle(() => !!r.el.textContent?.includes('an AI setup'))
      const modelRadio = [...r.el.querySelectorAll('input[type=radio]')].find((i) => i.parentElement?.textContent?.includes('an AI setup')) as HTMLInputElement
      await act(async () => { modelRadio.click() })
      await r.settle(() => r.el.querySelectorAll('select').length === 2)
      const [profile, project] = [...r.el.querySelectorAll('select')]
      await act(async () => {
        profile.value = 'frontier'; profile.dispatchEvent(new Event('change', { bubbles: true }))
        project.value = 'shop'; project.dispatchEvent(new Event('change', { bubbles: true }))
      })
      await r.settle(() => r.el.querySelectorAll('input[type=checkbox]').length > 0)
      expect(r.el.querySelectorAll('input[type=checkbox]').length).toBe(1) // only the landed Change
      await act(async () => { (r.el.querySelector('input[type=checkbox]') as HTMLInputElement).click() })
      expect(r.el.textContent).not.toContain(MEANING)
      const review = [...r.el.querySelectorAll('button')].find((b) => b.textContent?.startsWith('Review'))!
      await act(async () => { review.click() })
      expect(r.el.textContent).toContain(MEANING)
      expect(posts).toHaveLength(0)
      const start = [...r.el.querySelectorAll('button')].find((b) => b.textContent === 'Start the eval')!
      await act(async () => { start.click() })
      await r.settle(() => posts.length > 0)
      expect(posts[0]).toEqual({ changes: [CHANGE], profile: 'frontier', budgetDollars: 5, shown: MEANING })
    } finally {
      r.done()
    }
  })

  // A start AppTown refuses shows AppTown's words naming the change.
  it('says which change was refused when the start is refused', async () => {
    const refusal = 'shop#12 is itself in an eval\'s copy'
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') return new Response(JSON.stringify({ error: refusal }), { status: 400 })
      if (url.includes('/evals/start')) return new Response(JSON.stringify({ meaning: MEANING, profiles: ['standard', 'frontier'], packs: [], maxChanges: 5 }))
      if (url.includes('/changes')) return new Response(JSON.stringify([{ id: CHANGE, number: 12, title: 'Add search', state: 'shipped', baseSha: 'abc' }]))
      if (url.includes('/projects')) return new Response(JSON.stringify([{ slug: 'shop', name: 'Shop' }]))
      return new Response('{}')
    }))
    const r = await render('/evals/new', <EvalStartPage />, '/evals/new')
    try {
      await r.settle(() => !!r.el.textContent?.includes('an AI setup'))
      const modelRadio = [...r.el.querySelectorAll('input[type=radio]')].find((i) => i.parentElement?.textContent?.includes('an AI setup')) as HTMLInputElement
      await act(async () => { modelRadio.click() })
      await r.settle(() => r.el.querySelectorAll('select').length === 2)
      const [profile, project] = [...r.el.querySelectorAll('select')]
      await act(async () => {
        profile.value = 'frontier'; profile.dispatchEvent(new Event('change', { bubbles: true }))
        project.value = 'shop'; project.dispatchEvent(new Event('change', { bubbles: true }))
      })
      await r.settle(() => r.el.querySelectorAll('input[type=checkbox]').length > 0)
      await act(async () => { (r.el.querySelector('input[type=checkbox]') as HTMLInputElement).click() })
      await act(async () => { [...r.el.querySelectorAll('button')].find((b) => b.textContent?.startsWith('Review'))!.click() })
      await act(async () => { [...r.el.querySelectorAll('button')].find((b) => b.textContent === 'Start the eval')!.click() })
      await r.settle(() => !!r.el.textContent?.includes('is itself in an eval'))
      expect(r.el.textContent).toContain(refusal)
    } finally {
      r.done()
    }
  })

  // A what-if's "Try in an eval" lands here with its pack version chosen.
  it('starts from the pack version a link names', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.includes('/evals/start')) return new Response(JSON.stringify({ meaning: MEANING, profiles: ['standard'], packs: [{ name: 'go-small', latest: 4, recommended: 3 }], maxChanges: 5 }))
      return new Response('[]')
    }))
    const r = await render('/evals/new?pack=go-small&version=3', <EvalStartPage />, '/evals/new')
    try {
      await r.settle(() => !!r.el.textContent?.includes('a pack version'))
      const selects = [...r.el.querySelectorAll('select')]
      expect(selects.some((s) => s.value === 'go-small')).toBe(true)
      expect(selects.some((s) => s.value === '3')).toBe(true)
    } finally {
      r.done()
    }
  })

  // A pack version varies instead of a model: its versions are offered,
  // the recommended one named, and the start names the pack and version.
  it('starts an eval of a pack version', async () => {
    const posts: unknown[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        posts.push(JSON.parse(String(init.body)))
        return new Response(JSON.stringify({ id: NEW_EVAL, arms: [] }), { status: 201 })
      }
      if (url.includes('/evals/start')) return new Response(JSON.stringify({ meaning: MEANING, profiles: ['standard'], packs: [{ name: 'go-small', latest: 4, recommended: 3 }], maxChanges: 5 }))
      if (url.includes('/changes')) return new Response(JSON.stringify([{ id: CHANGE, number: 12, title: 'Add search', state: 'shipped', baseSha: 'abc' }]))
      if (url.includes('/projects')) return new Response(JSON.stringify([{ slug: 'shop', name: 'Shop' }]))
      return new Response('{}')
    }))
    const r = await render('/evals/new', <EvalStartPage />, '/evals/new')
    try {
      await r.settle(() => !!r.el.textContent?.includes('a pack version'))
      // A pack version is what an eval varies first.
      const pick = (label: string) => [...r.el.querySelectorAll('select')].find((s) => [...s.options].some((o) => o.text === label)) as HTMLSelectElement
      await act(async () => { const s = pick('a pack…'); s.value = 'go-small'; s.dispatchEvent(new Event('change', { bubbles: true })) })
      const versions = [...pick('v4').options].map((o) => o.text)
      expect(versions).toEqual(['v4', 'v3 (recommended now)', 'v2', 'v1'])
      await act(async () => { const s = pick('a project…'); s.value = 'shop'; s.dispatchEvent(new Event('change', { bubbles: true })) })
      await r.settle(() => r.el.querySelectorAll('input[type=checkbox]').length > 0)
      await act(async () => { (r.el.querySelector('input[type=checkbox]') as HTMLInputElement).click() })
      await act(async () => { [...r.el.querySelectorAll('button')].find((b) => b.textContent?.startsWith('Review'))!.click() })
      expect(r.el.textContent).toContain('pack version go-small@4')
      await act(async () => { [...r.el.querySelectorAll('button')].find((b) => b.textContent === 'Start the eval')!.click() })
      await r.settle(() => posts.length > 0)
      expect(posts[0]).toEqual({ changes: [CHANGE], pack: 'go-small', version: 4, budgetDollars: 5, shown: MEANING })
    } finally {
      r.done()
    }
  })
})

describe('the side-by-side scorecard', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // Each Change shows the original, both arms and their checks against the
  // base finding by finding, a check only one setup declares marked; the
  // tally states N and leaves out what is not comparable; an arm's diff is
  // read from the eval's own route when asked for.
  it('compares the arms finding by finding, with diffs and a tally', async () => {
    const row = (arm: string, extra: object) => ({ block: 'hygiene', version: 1, check: 'no-fixme', kind: 'shell', base: 'does-not-conform', arm, ...extra })
    const pack = { ...scorecard, state: 'finished', reason: '', subjectKind: 'pack', subject: 'go-small@4', subjectDigest: 'abc123', incomplete: 0, complaints: [],
      tallies: [{ measure: 'new findings', better: 1, same: 0, worse: 0, unknown: 0 }], comparable: 1, notComparable: 1,
      armCosts: { [ARM1]: { costMicros: 1, tokens: 1, incomplete: 0 }, 2: { costMicros: 2, tokens: 1, incomplete: 0 } },
      arms: [
        { ...scorecard.arms[0], declares: [{ block: 'hygiene', version: 1 }], score: { ...scorecard.arms[0].score,
          compared: { head: 'h1', base: [], arm: [], rows: [row('does-not-conform', { new: ['./b.txt'], alreadyThere: ['./a.txt'] }), { block: 'strict', version: 2, check: 'has-readme', kind: 'shell', base: 'does-not-conform', arm: 'does-not-conform', notComparable: 'failing on both, and the check lists no occurrences to compare' }] } } },
        { ...scorecard.arms[1], state: 'scored', reason: '', declares: [{ block: 'hygiene', version: 1 }, { block: 'strict', version: 2 }], score: { ...scorecard.arms[0].score,
          compared: { head: 'h2', base: [], arm: [], rows: [row('conforms', { fixed: ['./a.txt'] })] } } },
      ] }
    const fetched: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      fetched.push(url)
      if (url.includes('/diff')) return new Response('+FIXME new', { headers: { 'Content-Type': 'text/plain' } })
      return new Response(JSON.stringify(pack))
    }))
    const r = await render(`/evals/${EVAL}`, <EvalScorecardPage />, '/evals/:id')
    try {
      await r.settle(() => !!r.el.textContent?.includes('finding by finding'))
      const text = r.el.textContent ?? ''
      expect(text).toContain('with go-small@4 against as it runs now')
      expect(text).toContain('Over 1 Change (1 not comparable, left out)')
      expect(text).toContain('tested exactly abc123')
      expect(text).toContain('The original')
      expect(text).toContain('1 new: ./b.txt')
      expect(text).toContain('1 fixed: ./a.txt')
      expect(text).toContain('not comparable: failing on both')
      expect(text).toContain('only the varied setup declares this')
      expect(text).toContain('1 new, 0 fixed, 1 check not comparable')
      const show = [...r.el.querySelectorAll('button')].find((b) => b.textContent === 'Show its diff')!
      await act(async () => { show.click() })
      await r.settle(() => !!r.el.textContent?.includes('+FIXME new'))
      expect(fetched.some((u) => u.endsWith(`/evals/${EVAL}/arms/${ARM1}/diff`))).toBe(true)
      expect(r.el.textContent).toContain('+FIXME new')
    } finally {
      r.done()
    }
  })
})
