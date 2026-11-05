// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../../data'
import type { ConformanceView } from '../../wire.gen'
import { ProjectPage } from './ProjectPage'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

it('keeps an undecided AI reason with Re-check and an explicit recorded-risk decision', async () => {
  const lanes = { conforms: 0, excepted: 0, findings: 0, flagged: 0, unassessed: 0, broken: 1, na: 0 }
  const v = { id: 'verdict', projectSlug: 'shop', evaluator: 'llm', lane: 'broken', state: 'error', statusLabel: 'unknown', severity: 'medium', block: 'tools', version: 2, check: 'root-toolchain', detail: 'check did not finish: token limit reached' }
  const view = { projects: [{ slug: 'shop', name: 'Shop', standing: 'review-broken', sentence: 'Review incomplete', lanes }], byProject: { shop: [v] }, exceptions: {}, adjudications: {}, priorConfirmations: {}, checkDefs: {}, openRemediations: [] } as unknown as ConformanceView
  const fetch = vi.fn(async (_url: RequestInfo | URL, _init?: RequestInit) => new Response('{}'))
  vi.stubGlobal('fetch', fetch)
  const prompt = vi.spyOn(window, 'prompt').mockReturnValue('This app does not need a declared toolchain')
  const recheck = vi.fn()
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><ProjectPage view={view} slug="shop" readOnly={false} onRunOne={recheck} /></MemoryRouter></QueryClientProvider>))
    expect(el.textContent?.toLowerCase()).toContain(v.detail)
    const button = (text: string) => [...el.querySelectorAll('button')].find(b => b.textContent === text)!
    act(() => button('Re-check').click())
    expect(recheck).toHaveBeenCalledWith('shop')
    await act(async () => button('Record a decision').click())
    expect(prompt.mock.calls[0][0]).toContain('An undecided result stays undecided')
    const decision = fetch.mock.calls.find(args => String(args[0]).endsWith('/projects/shop/dispositions'))
    expect(decision).toBeDefined()
    const body = JSON.parse((decision![1] as RequestInit).body as string)
    expect(body).toEqual({ fingerprint: 'guidance|tools@2|root-toolchain|', status: 'accepted', justification: 'This app does not need a declared toolchain' })
  } finally { act(() => root.unmount()); client.clear() }
})
