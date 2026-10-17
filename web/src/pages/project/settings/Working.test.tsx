// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../../data'
import { GuidanceTab } from './Working'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const block = (name: string, position: number) => ({ position, blockId: `00000000-${String(position).padStart(4, '0')}-7000-8000-000000000000`, name, version: 1, latestVersion: 1, disabled: false, bytes: 10 })
const guidanceAt = (revision: number, ...imports: ReturnType<typeof block>[]) => ({ revision, imports, generated: [], effective: { hash: 'abcdef0123456789', markdown: '', bytes: 0 } })
const guidance = (...imports: ReturnType<typeof block>[]) => guidanceAt(4, ...imports)
const path = '/projects/demo/guidance'

// Everything the tab reads besides the project's guidance, in the shapes the
// server sends when a project has adopted nothing.
function otherReads(url: string): Response {
  if (url.endsWith('/adoption')) return new Response(JSON.stringify({ packs: [], additions: [], exclusions: [], conditions: [] }))
  if (url.endsWith('/guidance/blocks')) return new Response(JSON.stringify([{ id: '00000000-0003-7000-8000-000000000000', name: 'plain-words', tags: '', latestVersion: 1, origin: 'stock', createdAt: '', updatedAt: '', bytes: 10 }]))
  if (url.endsWith('/lanes')) return new Response(JSON.stringify({ lanes: [], system: [] }))
  if (url.endsWith('/policies')) return new Response(JSON.stringify({ policies: [] }))
  return new Response('{}')
}

function mount(client: ReturnType<typeof newQueryClient>) {
  const el = document.createElement('div')
  const root = createRoot(el)
  act(() => { root.render(<QueryClientProvider client={client}><MemoryRouter><GuidanceTab slug="demo" /></MemoryRouter></QueryClientProvider>) })
  return { el, unmount: () => act(() => root.unmount()) }
}

const button = (el: HTMLElement, text: string) => [...el.querySelectorAll('button')].find((b) => b.textContent === text)!

describe('the guidance editor', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // Saving sends the whole list. A list the page never read is empty, and
  // saving it with one block added would drop every block the project had.
  it('stays locked when its first read fails, even with an older answer held', async () => {
    const client = newQueryClient()
    client.setQueryData(getKey(path), guidance(block('house-style', 1)))
    vi.stubGlobal('fetch', vi.fn(async (url: string) => url.endsWith(path)
      ? new Response(JSON.stringify({ error: 'not allowed' }), { status: 403 })
      : otherReads(url)))
    const { el, unmount } = mount(client)
    try {
      await vi.waitFor(() => expect(el.querySelector('.out-of-date')?.getAttribute('aria-label')).toContain('not allowed'))
      await vi.waitFor(() => expect(el.textContent).toContain('plain-words'))
      const picker = el.querySelector('select') as HTMLSelectElement
      await act(async () => {
        Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')!.set!.call(picker, 'plain-words')
        picker.dispatchEvent(new Event('change', { bubbles: true }))
      })
      expect(picker.value).toBe('plain-words')
      expect(button(el, 'Save').disabled).toBe(true)
      expect(button(el, 'Add').disabled).toBe(true)
      expect(el.textContent).toContain('reading the current guidance before editing')
      expect(el.textContent).not.toContain('No guidance imported yet')
    } finally {
      unmount()
      client.clear()
    }
  })

  it('seeds from its own read and keeps an unsaved draft through a refresh', async () => {
    const client = newQueryClient()
    let server = guidance(block('house-style', 1))
    let saved: unknown
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'PUT') { saved = JSON.parse(String(init.body)); return new Response(JSON.stringify(server)) }
      return url.endsWith(path) ? new Response(JSON.stringify(server)) : otherReads(url)
    }))
    const { el, unmount } = mount(client)
    try {
      await vi.waitFor(() => expect(el.textContent).toContain('house-style'))
      const enabled = () => [...el.querySelectorAll('tbody input[type=checkbox]')] as HTMLInputElement[]
      await act(async () => { enabled()[0].click() })
      server = guidanceAt(5, block('house-style', 1), block('prose', 2))
      await act(async () => { await client.invalidateQueries({ queryKey: ['get'] }) })
      await vi.waitFor(() => expect(client.getQueryData(getKey(path))).toEqual(server))
      expect(el.textContent).not.toContain('prose')
      expect(enabled()[0].checked).toBe(false)
      await act(async () => { button(el, 'Save').click() })
      // The draft keeps the revision it started from, not the refresh's.
      expect(saved).toEqual({ expectedRevision: 4, imports: [{ name: 'house-style', version: 1, disabled: true }] })
    } finally {
      unmount()
      client.clear()
    }
  })

  // The way of working moved on while the list was edited: the save is
  // refused, the list is kept, and the person decides.
  it('keeps the list when the way of working moved on and saves it over the new revision on request', async () => {
    const client = newQueryClient()
    let server = guidance(block('house-style', 1))
    const saved: Record<string, unknown>[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'PUT') {
        saved.push(JSON.parse(String(init.body)))
        return saved.length === 1
          ? new Response(JSON.stringify({ error: 'moved' }), { status: 409, headers: { 'X-Forge-Deny-Code': 'settings-moved' } })
          : new Response(JSON.stringify(server))
      }
      return url.endsWith(path) ? new Response(JSON.stringify(server)) : otherReads(url)
    }))
    const { el, unmount } = mount(client)
    try {
      await vi.waitFor(() => expect(el.textContent).toContain('house-style'))
      const enabled = () => [...el.querySelectorAll('tbody input[type=checkbox]')] as HTMLInputElement[]
      await act(async () => { enabled()[0].click() })
      server = guidanceAt(6, block('house-style', 1), block('prose', 2))
      await act(async () => { button(el, 'Save').click() })
      await vi.waitFor(() => expect(el.textContent).toContain('The list is now: house-style, prose'))
      expect(enabled()[0].checked).toBe(false)
      await act(async () => { button(el, 'Keep my list over it').click() })
      await act(async () => { button(el, 'Save').click() })
      expect(saved[1]).toEqual({ expectedRevision: 6, imports: [{ name: 'house-style', version: 1, disabled: true }] })
    } finally {
      unmount()
      client.clear()
    }
  })
})
