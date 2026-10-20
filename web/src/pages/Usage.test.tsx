// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { settled } from '../settled'
import { UsageView } from './Usage'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const day = (ago: number) => new Date(Date.now() - ago * 86400e3).toISOString().slice(0, 10)
const label = (ago: number) => new Date(day(ago) + 'T00:00:00Z').toLocaleDateString('en-AU', { day: 'numeric', month: 'short', timeZone: 'UTC' })

describe('the daily usage chart', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // A phone has no hover: the chart's numbers must be on the page, not only
  // in a tooltip, and a person must be able to pick another day.
  it('spells out the latest used day and moves to another day with the arrow keys', async () => {
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
      const shown = () => el.querySelector('.usage-day')?.textContent ?? ''
      const chart = el.querySelector<HTMLElement>('[role="slider"]')!
      expect(shown()).toBe(`${label(1)}: 500 tokens — workflow 400, interactive 100`)
      expect(chart.getAttribute('aria-valuetext')).toBe(shown())

      await act(async () => { chart.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true })) })
      expect(shown()).toBe(`${label(2)}: no use`)
      await act(async () => { chart.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true })) })
      expect(shown()).toBe(`${label(3)}: 1.0k tokens — workflow 1.0k, interactive 0`)
    } finally {
      act(() => root.unmount())
      client.clear()
    }
  })

  // Spend AppTown could not count is not shown as a complete total.
  it('says "at least" when some model calls spent an unknown amount', async () => {
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
      expect(el.textContent).toContain('at least $')
      expect(el.textContent).toContain('2 model calls spent an unknown amount')
    } finally {
      act(() => root.unmount())
      client.clear()
    }
  })
})
