// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../data'
import { renderToStaticMarkup } from 'react-dom/server'
import { settled } from '../settled'
import { TimingView, UsageView } from './Usage'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const day = (ago: number) => new Date(Date.now() - ago * 86400e3).toISOString().slice(0, 10)
const label = (ago: number) => new Date(day(ago) + 'T00:00:00Z').toLocaleDateString('en-AU', { day: 'numeric', month: 'short', timeZone: 'UTC' })

describe('the daily usage chart', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // A phone has no hover: the chart's numbers must be on the page, not only
  // in a tooltip, and a person must be able to pick another day.
  it('shows daily detail only after selection and supports arrow keys', async () => {
    const totals = { total: 1500, input: 1000, output: 500, cacheRead: 0, costMicros: 0 }
    const report = {
      windowDays: 30, totals, byKind: [], byModel: [], byDay: [], byProject: [], byWorkflow: [],
      byDayKind: [
        { day: day(3), kind: 'step', total: 5000, cached: 4000, costMicros: 0 },
        { day: day(1), kind: 'step', total: 400, costMicros: 0 },
        { day: day(1), kind: 'console', total: 100, costMicros: 0 },
      ],
    }
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(report))))
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    try {
      await act(async () => {
        root.render(<QueryClientProvider client={client}><UsageView project="demo" /></QueryClientProvider>)
      })
      await settled(client)
      expect(el.querySelector('[role=slider]')).not.toBeNull()
      expect(el.querySelector('section[aria-label="New tokens per day"] [role=slider]')).not.toBeNull()
      expect(el.querySelector('.usage-chart')?.getAttribute('aria-valuetext')).toContain('500 new tokens')
      await act(async () => { [...el.querySelectorAll('button')].find(b => b.textContent === 'Details')!.click() })
      const shown = () => el.querySelector('.usage-day')?.textContent ?? ''
      const chart = el.querySelector<HTMLElement>('[role="slider"]')!
      expect(shown()).toBe('')
      expect(chart.getAttribute('aria-valuetext')).toBe(`${label(1)}: 500 new tokens (Automated work 400, Chat 100)`)

      await act(async () => { chart.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true })) })
      expect(shown()).toBe(`${label(2)}: no use`)
      await act(async () => { chart.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true })) })
      expect(shown()).toBe(`${label(3)}: 1.0k new tokens (Automated work 1.0k, Chat 0) · 4.0k from the cache`)
    } finally {
      act(() => root.unmount())
      client.clear()
    }
  })

  it('counts tokens from instance work and distinguishes spend without recorded tokens', async () => {
    const report = {
      totals: { total: 700, costMicros: 67100 }, byKind: [], byModel: [], byProject: [], byWorkflow: [],
      byDayKind: [{ day: day(1), kind: 'narrative', total: 700, costMicros: 67100 }],
    }
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(report))))
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    try {
      await act(async () => { root.render(<QueryClientProvider client={client}><UsageView /></QueryClientProvider>) })
      await settled(client)
      expect(el.querySelector('[role=slider]')?.getAttribute('aria-valuetext')).toContain('700 new tokens')
      expect(el.querySelector('[role=slider]')?.getAttribute('aria-valuetext')).toContain('Other work 700')
      report.byDayKind[0].total = 0
      report.totals.total = 0
      await act(async () => { await client.invalidateQueries() })
      await settled(client)
      expect(el.querySelector('[role=slider]')?.getAttribute('aria-valuetext')).toContain('tokens not recorded · $0.07')
      expect(el.querySelector('[role=slider]')?.getAttribute('aria-valuetext')).not.toContain('0 tokens')
      expect(el.textContent).not.toContain('tallest day 1')
      expect(el.textContent).not.toContain('No usage recorded yet.')
      expect(el.textContent).toContain('Tokens not recorded.')
    } finally { act(() => root.unmount()); client.clear() }
  })

  // Spend AppTown could not count is not shown as a complete total.
  it('qualifies the price when some calls are unpriced', async () => {
    const report = {
      windowDays: 30, totals: { total: 15, input: 10, output: 5, cacheRead: 0, cacheWrite: 0, costMicros: 40, incomplete: 2 },
      byKind: [], byModel: [], byDay: [], byProject: [], byWorkflow: [], byDayKind: [],
    }
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(report))))
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    try {
      await act(async () => {
        root.render(<QueryClientProvider client={client}><UsageView project="demo" /></QueryClientProvider>)
      })
      await settled(client)
      expect(el.querySelector('.usage-figure')?.textContent).toContain('Cost, 30 days$0.00')
      // One line under the cost: how many records have unknown or partial
      // cost. Their tokens are not counted as unpriced: a record may hold
      // priced calls, or no known tokens at all.
      expect(el.querySelector('.usage-figure')?.textContent).toContain('2 records have unknown or partial cost')
      expect(el.querySelector('.usage-figure')?.textContent).not.toContain('with no price')
      // A window with no use names no kinds of use in the legend.
      expect(el.querySelector('.usage-legend')).toBeNull()
    } finally {
      act(() => root.unmount())
      client.clear()
    }
  })
})

describe('model prices', () => {
  afterEach(() => { vi.unstubAllGlobals() })
  it('describes source-less historical spend without claiming call counts and retains catalog and provider provenance', async () => {
    const report = {
      windowDays: 30, totals: { total: 44, input: 40, output: 4, cacheRead: 0, cacheWrite: 0, costMicros: 0, incomplete: 0, providerReported: 0, catalog: 0, unpriced: 4 },
      byKind: [{ kind: 'console', total: 44, costMicros: 0, unpriced: 4 }], byModel: [], byDay: [], byProject: [], byWorkflow: [], byDayKind: [],
    }
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(report))))
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    try {
      await act(async () => { root.render(<QueryClientProvider client={client}><UsageView project="demo" /></QueryClientProvider>) })
      await settled(client)
      expect(el.querySelector('.usage-figure')?.textContent).toContain('not priced')
      // A missing price is a word, set apart from an amount.
      expect(el.querySelector('.usage-figure .stat-value')?.classList.contains('stat-word')).toBe(true)
      // Where prices came from is a detail, on the Details tab.
      expect(el.textContent).not.toContain('without a price source')
      await act(async () => { [...el.querySelectorAll('button')].find(b => b.textContent === 'Details')!.click() })
      expect(el.textContent).toContain('4 spend records without a price source')
      report.totals.costMicros = 3000
      report.totals.providerReported = 2
      report.totals.catalog = 1
      report.totals.unpriced = 1
      await act(async () => { await client.invalidateQueries() })
      await settled(client)
      expect(el.querySelector('.usage-figure')?.textContent).toContain('at least $0.003')
      expect(el.querySelector('.usage-figure .stat-value')?.classList.contains('stat-word')).toBe(false)
      expect(el.textContent).toContain('2 reported by the provider')
      expect(el.textContent).toContain('1 from the model catalog')
      expect(el.textContent).toContain('1 spend record without a price source')
    } finally { act(() => root.unmount()); client.clear() }
  })
})

describe('where the time goes', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // A step's spend is the ledger's: "at least" when some of it went
  // unrecorded, "spend unknown" when none of it was recorded.
  it('qualifies a step spend the ledger could not count whole', async () => {
    const step = (name: string, costMicros: number, costIncomplete: number) => ({
      stage: 'apply', name, type: 'agent/pi', runs: 1, medianMs: 1000, p90Ms: 1000, maxMs: 1000, waitMedianMs: 0, costMicros, costIncomplete, reworks: 0,
    })
    const report = { days: 7, workflows: [{ workflow: 'quick-change', days: 7, runs: 1, totalMedianMs: 2000, steps: [step('build', 30000, 1), step('review', 0, 1), step('plan', 20000, 0)] }] }
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(report))))
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    try {
      await act(async () => {
        root.render(<QueryClientProvider client={client}><TimingView days={7} /></QueryClientProvider>)
      })
      await settled(client)
      const rows = [...el.querySelectorAll('.bar-row-head')].map((r) => r.textContent ?? '')
      expect(rows[0]).toContain('· at least $0.03')
      expect(rows[1]).toContain('· spend unknown')
      expect(rows[2]).toContain('· $0.02')
      expect(rows[2]).not.toContain('at least')
      expect(el.textContent).toContain('at least $0.05 in agent time')
    } finally {
      act(() => root.unmount())
      client.clear()
    }
  })
})

// The typical run is measured over completed runs; with none in the window the
// owner sends no duration, and the page makes no claim of one ("takes 0s").
it('says nothing of a typical run when no run completed', async () => {
  const report = { days: 7, workflows: [{ workflow: 'land', days: 7, runs: 2, steps: [{ stage: 'check', name: 'tests', type: 'check', runs: 2, medianMs: 1000, p90Ms: 1000, maxMs: 1000, waitMedianMs: 0, costMicros: 0, costIncomplete: 0, reworks: 0 }] }] }
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(report))))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  try {
    await act(async () => {
      root.render(<QueryClientProvider client={client}><TimingView days={7} /></QueryClientProvider>)
    })
    await settled(client)
    expect(el.textContent).toContain('2 runs')
    expect(el.textContent).toContain('tests')
    expect(el.textContent).not.toContain('a typical run takes')
  } finally {
    act(() => root.unmount())
    client.clear()
  }
})

// A record with unknown or partial cost is counted as a record: neither its
// surviving priced tokens nor a missing usage reading become "tokens with no
// price" (review R3, 8 Nov).
it.each([0, 100])('does not call incomplete usage an exact unpriced token count: %s surviving tokens', total => {
  const client = newQueryClient()
  client.setQueryData(getKey('/usage?window=30&project=shop'), {
    totals: { total, costMicros: total ? 1000000 : 0, incomplete: 1, unpriced: 0 },
    byKind: [], byModel: [], byProject: [], byWorkflow: [], byDayKind: [],
  })
  try {
    const html = renderToStaticMarkup(<QueryClientProvider client={client}><UsageView project="shop" /></QueryClientProvider>)
    expect(html).not.toContain('with no price')
    expect(html).toContain('1 record has unknown or partial cost')
  } finally { client.clear() }
})
