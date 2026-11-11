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
  const prompt = vi.spyOn(window, 'prompt').mockImplementation(() => { throw new Error('native dialog') })
  const recheck = vi.fn()
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><ProjectPage view={view} slug="shop" readOnly={false} onRunOne={recheck} /></MemoryRouter></QueryClientProvider>))
    expect(el.textContent?.toLowerCase()).toContain(v.detail)
    const button = (text: string) => [...el.querySelectorAll('button')].find(b => b.textContent === text)!
    act(() => button('Re-check').click())
    expect(recheck).toHaveBeenCalledWith('shop')
    await act(async () => button('Accept risk').click())
    expect(el.textContent).toContain('root-toolchain may not be met for tools version 2')
    expect(el.textContent).toContain('this check version, until revoked')
    expect(el.textContent).toContain('An undecided result stays undecided')
    expect(button('Accept risk').disabled).toBe(true)
    expect(fetch.mock.calls.some(args => String(args[0]).endsWith('/dispositions'))).toBe(false)
    const input = el.querySelector<HTMLInputElement>('[aria-label="Reason for accepting this risk"]')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'This app does not need a declared toolchain')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => button('Accept risk').click())
    expect(prompt).not.toHaveBeenCalled()
    const decision = fetch.mock.calls.find(args => String(args[0]).endsWith('/projects/shop/dispositions'))
    expect(decision).toBeDefined()
    const body = JSON.parse((decision![1] as RequestInit).body as string)
    expect(body).toEqual({ fingerprint: 'guidance|tools@2|root-toolchain|', status: 'accepted', justification: 'This app does not need a declared toolchain' })
  } finally { act(() => root.unmount()); client.clear() }
})

it('confirms a finding in place with an optional note, and cancellation records nothing', async () => {
  const v = { id: 'flag', projectSlug: 'shop', evaluator: 'llm', lane: 'flagged', state: 'flagged', statusLabel: 'possible problem', severity: 'medium', block: 'tools', version: 2, check: 'root-toolchain', detail: 'Missing toolchain' }
  const view = { projects: [{ slug: 'shop', name: 'Shop', standing: 'conforms', sentence: 'One possible problem', lanes: {} }], byProject: { shop: [v] }, exceptions: {}, adjudications: {}, priorConfirmations: {}, checkDefs: {}, openRemediations: [] } as unknown as ConformanceView
  const fetch = vi.fn(async (_url: RequestInfo | URL, _init?: RequestInit) => new Response('{}'))
  vi.stubGlobal('fetch', fetch)
  const prompt = vi.spyOn(window, 'prompt').mockImplementation(() => { throw new Error('native dialog') })
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  const button = (text: string) => [...el.querySelectorAll('button')].find(b => b.textContent === text)!
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><ProjectPage view={view} slug="shop" readOnly={false} onRunOne={() => {}} /></MemoryRouter></QueryClientProvider>))
    await act(async () => button('Confirm finding').click())
    expect(el.querySelector('[aria-label="Finding note (optional)"]')).not.toBeNull()
    await act(async () => button('Cancel').click())
    expect(fetch.mock.calls.some(args => String(args[0]).endsWith('/confirm'))).toBe(false)
    await act(async () => button('Confirm finding').click())
    await act(async () => button('Confirm finding').click())
    const call = fetch.mock.calls.find(args => String(args[0]).endsWith('/verdicts/flag/confirm'))!
    expect(JSON.parse(String(call[1]?.body))).toEqual({ note: '' })
    expect(prompt).not.toHaveBeenCalled()
  } finally { act(() => root.unmount()); client.clear() }
})

it('leads with per-check current evidence and keeps dated reviews underneath, outside snapshots', async () => {
  const v = { id: 'v', reviewId: 'r', sha: 'abcdef123456', at: '2026-11-07T03:02:00Z', projectSlug: 'shop', evaluator: 'sh', lane: 'unassessed', state: 'stale', statusLabel: 'old', severity: 'medium', block: 'tools', version: 2, check: 'clean' }
  const view = { projects: [{ slug: 'shop', name: 'Shop', standing: 'conforms-so-far', sentence: 'Not checked', lanes: {} }], byProject: { shop: [v] }, exceptions: {}, adjudications: {}, priorConfirmations: {}, checkDefs: {}, openRemediations: [], reviews: [{ id: 'r', number: 12, trigger: 'landing' }] } as unknown as ConformanceView
  const fetch = vi.fn(async (url: RequestInfo | URL) => new Response(JSON.stringify(String(url).endsWith('/history') ? { history: [{ reviewId: 'r', at: v.at, findings: 0, flagged: 0, unassessed: 0 }] } : { fixes: [] })))
  vi.stubGlobal('fetch', fetch)
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  const render = (readOnly: boolean) => <QueryClientProvider client={client}><MemoryRouter><ProjectPage view={view} slug="shop" readOnly={readOnly} onRunOne={() => {}} /></MemoryRouter></QueryClientProvider>
  try {
    await act(async () => root.render(render(false)))
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 25)) })
    // The current view acts on the project; a snapshot only shows it.
    expect(el.textContent).toContain('Check this project')
    expect(el.textContent).toContain('landing #12')
    // The Result says the judgement is not on the current commit; the
    // judged line beside it does not say so again.
    const row = [...el.querySelectorAll('tbody tr')].find(r => r.textContent?.includes('landing #12'))!
    expect(row.textContent).toContain('No judgement on the current commit')
    expect(row.textContent).not.toContain('judged on an older commit')
    expect(el.textContent).not.toContain('Not run yet')
    expect(el.textContent).toContain('No current judgement')
    expect(el.textContent).toContain('Review · 7 Nov')
    expect(el.textContent!.indexOf('commit abcdef12')).toBeLessThan(el.textContent!.indexOf('Dated snapshots'))
    expect(el.querySelector('a[href="/conformance/reviews/r/projects/shop"]')).not.toBeNull()
    // A review's counts are the numbers alone: the column label names them.
    expect(['Findings', 'Possible problems', 'Not judged'].map(l => el.querySelector(`td[data-label="${l}"]`)?.textContent)).toEqual(['0', '0', '0'])
    await act(async () => root.render(render(true)))
    expect(el.textContent).not.toContain('Check this project')
    expect(el.textContent).not.toContain('Dated snapshots')
  } finally { act(() => root.unmount()); client.clear() }
})

// Every row in the no-current-judgement section shows its owner's status
// chip: a check not run yet is never a bare dash beside a chip for an old one.
it('shows a status chip on every row without a current judgement', async () => {
  const stale = { id: 'v1', reviewId: 'r', sha: 'abcdef123456', at: '2026-11-07T03:02:00Z', projectSlug: 'shop', evaluator: 'sh', lane: 'unassessed', state: 'stale', statusLabel: 'unknown', severity: 'medium', block: 'tools', version: 2, check: 'clean' }
  const notRun = { ...stale, id: 'v2', state: 'not-run', statusLabel: 'unknown', check: 'tidy' }
  const view = { projects: [{ slug: 'shop', name: 'Shop', standing: 'conforms-so-far', sentence: '', lanes: {} }], byProject: { shop: [stale, notRun] }, exceptions: {}, adjudications: {}, priorConfirmations: {}, checkDefs: {}, openRemediations: [], reviews: [{ id: 'r', number: 12, trigger: 'landing' }] } as unknown as ConformanceView
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ history: [], fixes: [] }))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><ProjectPage view={view} slug="shop" readOnly onRunOne={() => {}} /></MemoryRouter></QueryClientProvider>))
    const rows = [...el.querySelectorAll('tbody tr')]
    expect(rows).toHaveLength(2)
    for (const row of rows) expect(row.querySelector('.chip')).not.toBeNull()
    expect(el.textContent).not.toContain('—')
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

// The lead line is a summary: checks that could not run are counted there and
// each reason stays on its own check row further down.
it('counts checks that could not run in the lead line and keeps each reason on its row', async () => {
  const lanes = { conforms: 0, excepted: 0, findings: 0, flagged: 0, unassessed: 0, broken: 2, na: 0 }
  const reasons = ['hadolint error (exit -1): no Docker to run hadolint/hadolint:v2.12.0-alpine', 'semgrep error (exit -1): no Docker to run semgrep/semgrep:1.99.0']
  const vs = reasons.map((detail, i) => ({ id: `v${i}`, projectSlug: 'shop', evaluator: 'tool', lane: 'broken', state: 'error', statusLabel: 'unknown', severity: 'medium', block: 'tools', version: 1, check: `scan-${i}`, detail }))
  const view = { projects: [{ slug: 'shop', name: 'Shop', standing: 'review-broken', sentence: `Review incomplete · 2 not known: ${reasons.join('; ')}`, lanes, notKnown: reasons.map(detail => ({ detail, evaluator: 'tool' })) }], byProject: { shop: vs }, exceptions: {}, adjudications: {}, priorConfirmations: {}, checkDefs: {}, openRemediations: [] } as unknown as ConformanceView
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{}')))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><ProjectPage view={view} slug="shop" readOnly onRunOne={() => {}} /></MemoryRouter></QueryClientProvider>))
    const lead = el.querySelector('.page-head + .muted.small, .muted.small')!.textContent ?? ''
    expect(lead).toContain("2 checks couldn't run")
    for (const r of reasons) expect(lead).not.toContain(r)
    for (const r of reasons) expect(el.textContent).toContain(r)
  } finally { act(() => root.unmount()); client.clear() }
})

// A finding row already says "No judgement on the current commit" for an old
// result, so its evidence line names the commit without saying it again.
it('does not repeat the older-commit note in a finding row', async () => {
  const lanes = { conforms: 0, excepted: 0, findings: 1, flagged: 0, unassessed: 0, broken: 0, na: 0 }
  const v = { id: 'old', reviewId: 'r', sha: 'abcdef123456', at: '2026-11-07T03:02:00Z', projectSlug: 'shop', evaluator: 'sh', lane: 'findings', state: 'stale', statusLabel: 'failed', severity: 'medium', block: 'tools', version: 2, check: 'clean' }
  const view = { projects: [{ slug: 'shop', name: 'Shop', standing: 'has-findings', sentence: '1 finding', lanes }], byProject: { shop: [v] }, exceptions: {}, adjudications: {}, priorConfirmations: {}, checkDefs: {}, openRemediations: [], reviews: [{ id: 'r', number: 5, trigger: 'manual' }] } as unknown as ConformanceView
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{}')))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><ProjectPage view={view} slug="shop" readOnly onRunOne={() => {}} /></MemoryRouter></QueryClientProvider>))
    expect(el.textContent).toContain('No judgement on the current commit')
    expect(el.textContent).toContain('commit abcdef12')
    expect(el.textContent).toContain('review #5')
    expect(el.textContent).not.toContain('judged on an older commit')
  } finally { act(() => root.unmount()); client.clear() }
})
