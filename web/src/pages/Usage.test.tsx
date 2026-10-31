// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
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
        { day: day(3), kind: 'step', total: 1000, costMicros: 0 },
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
      expect(el.querySelector('[role=slider]')).toBeNull()
      await act(async () => { [...el.querySelectorAll('button')].find(b => b.textContent === 'Details')!.click() })
      const shown = () => el.querySelector('.usage-day')?.textContent ?? ''
      const chart = el.querySelector<HTMLElement>('[role="slider"]')!
      expect(shown()).toBe('')
      expect(chart.getAttribute('aria-valuetext')).toBe(`${label(1)}: 500 tokens · Automated work 400, Chat 100`)

      await act(async () => { chart.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true })) })
      expect(shown()).toBe(`${label(2)}: no use`)
      await act(async () => { chart.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true })) })
      expect(shown()).toBe(`${label(3)}: 1.0k tokens · Automated work 1.0k, Chat 0`)
    } finally {
      act(() => root.unmount())
      client.clear()
    }
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
      expect(el.querySelector('.stat-card')?.textContent).toContain('Cost in 30 days$0.00')
      expect(el.textContent).toContain('Some calls not priced.')
    } finally {
      act(() => root.unmount())
      client.clear()
    }
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
      expect(rows[0]).toContain('· at least $0.0300')
      expect(rows[1]).toContain('· spend unknown')
      expect(rows[2]).toContain('· $0.0200')
      expect(rows[2]).not.toContain('at least')
      expect(el.textContent).toContain('at least $0.0500 in agent time')
    } finally {
      act(() => root.unmount())
      client.clear()
    }
  })
})
