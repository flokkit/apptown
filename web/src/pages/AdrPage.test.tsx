// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../data'
import AdrPage from './AdrPage'

// Record ids are UUIDv7 strings.
const ADR = '00000000-0004-7000-8000-000000000000'
const PROJECT = '00000000-0001-7000-8000-000000000000'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const held = {
  adr: { id: ADR, projectId: PROJECT, projectSlug: 'shop', number: 4, title: 'Old title', status: 'draft', bodyMd: 'old body', tags: '', supersedesId: null, changeId: null, createdAt: '', updatedAt: '2026-10-01T00:00:00Z' },
  supersededById: null,
  options: { contentEditable: true, canSupersede: false, transitions: [] },
}

// The decision editor starts only from an answer the page read itself: an
// answer another view left in the cache may be old, and a save from it
// would write old text back over newer (web-data.md, editor rule 1).
it('does not offer editing from a held answer before its own read arrives', async () => {
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})))
  const client = newQueryClient()
  client.setQueryData(getKey(`/adrs/${ADR}`), held)
  const el = document.createElement('div')
  const root = createRoot(el)
  try {
    await act(async () => {
      root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/adrs/${ADR}`]}><Routes><Route path="/adrs/:id" element={<AdrPage />} /></Routes></MemoryRouter></QueryClientProvider>)
    })
    expect(el.textContent).toContain('Old title')
    const edit = [...el.querySelectorAll('button')].find((b) => b.textContent === 'Edit') as HTMLButtonElement
    expect(edit.disabled).toBe(true)
  } finally {
    act(() => root.unmount())
    client.clear()
    vi.unstubAllGlobals()
  }
})

// Two people edit one proposed decision (review web-readgate-2026-10-08,
// W-03). The editor sends only the fields it changed, each with the text it
// started from, so a tags-only save leaves the other person's new rationale
// alone, and a body save from replaced text is refused and offers the
// current text rather than overwriting it.
describe('two editors on one decision', () => {
  afterEach(() => vi.unstubAllGlobals())
  type Rec = typeof held.adr
  function forge(rec: Rec) {
    const sent: Record<string, unknown>[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if ((init?.method ?? 'GET') === 'PATCH') {
        const body = JSON.parse(String(init!.body)) as Record<string, string> & { expected: Record<string, string> }
        sent.push(body)
        for (const k of ['title', 'bodyMd', 'tags'] as const) {
          if (k in body && body.expected?.[k] !== rec[k]) {
            return new Response(JSON.stringify({ error: 'this decision\'s text was edited since this edit started' }), { status: 409, headers: { 'X-Forge-Deny-Code': 'adr-text-moved' } })
          }
        }
        for (const k of ['title', 'bodyMd', 'tags'] as const) if (k in body) rec[k] = body[k]
        return new Response(JSON.stringify(rec))
      }
      return new Response(JSON.stringify({ ...held, adr: { ...rec } }))
    }))
    return sent
  }
  async function open() {
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/adrs/${ADR}`]}><Routes><Route path="/adrs/:id" element={<AdrPage />} /></Routes></MemoryRouter></QueryClientProvider>)
    })
    const btn = (t: string) => [...el.querySelectorAll('button')].find((b) => b.textContent === t) as HTMLButtonElement
    await vi.waitFor(() => expect(btn('Edit').disabled).toBe(false))
    await act(async () => { btn('Edit').click() })
    return { el, btn, done: () => { act(() => root.unmount()); client.clear() } }
  }
  const setValue = async (el: HTMLInputElement | HTMLTextAreaElement, v: string) => {
    await act(async () => {
      Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), 'value')!.set!.call(el, v)
      el.dispatchEvent(new Event('input', { bubbles: true }))
    })
  }

  it('keeps one person\'s rationale when the other changes only tags, and refuses a body save from replaced text', async () => {
    const rec = { ...held.adr, bodyMd: 'original rationale', tags: 'data' }
    const sent = forge(rec)
    const a = await open()
    const b = await open()
    await setValue(a.el.querySelector('textarea')!, 'new rationale from A')
    await act(async () => { a.btn('Save').click() })
    await vi.waitFor(() => expect(rec.bodyMd).toBe('new rationale from A'))

    await setValue(b.el.querySelectorAll('input')[1] as HTMLInputElement, 'data,storage')
    await act(async () => { b.btn('Save').click() })
    await vi.waitFor(() => expect(rec.tags).toBe('data,storage'))
    expect(rec.bodyMd).toBe('new rationale from A')
    expect(sent[1]).toEqual({ tags: 'data,storage', expected: { tags: 'data' } })

    await act(async () => { b.btn('Edit').click() })
    // B's page last read the body before A's save only if it never read again;
    // either way a save names the body it started from.
    await setValue(b.el.querySelector('textarea')!, 'B rewrites it')
    rec.bodyMd = 'A saved again meanwhile'
    await act(async () => { b.btn('Save').click() })
    await vi.waitFor(() => expect(b.el.textContent).toContain('Keep my edits'))
    expect(rec.bodyMd).toBe('A saved again meanwhile')
    a.done(); b.done()
  })
})
