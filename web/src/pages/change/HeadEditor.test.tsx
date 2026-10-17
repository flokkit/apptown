// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import type { ChangeView } from '../../api'
import { newQueryClient } from '../../data'
import { ChangeBrief } from './Head'

// Record ids are UUIDv7 strings.
const CHANGE = '00000000-0005-7000-8000-000000000000'
const PROJECT = '00000000-0001-7000-8000-000000000000'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const view = (body: string, editRevision: number, externalRef = ''): ChangeView => ({
  change: { id: CHANGE, number: 5, projectId: PROJECT, type: 'feature', title: 'Cart', body, externalRef, runId: null, createdAt: '', state: 'in_progress', editRevision },
  evidence: {}, editing: 0, runs: [], commits: [],
} as unknown as ChangeView)

function type(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype
  Object.getOwnPropertyDescriptor(proto, 'value')!.set!.call(el, value)
  el.dispatchEvent(new Event('input', { bubbles: true }))
}

function mount(answer: () => Response) {
  const sent: Record<string, unknown>[] = []
  vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
    if (init?.method === 'PATCH') { sent.push(JSON.parse(String(init.body))); return answer() }
    return new Response('[]')
  }))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  const render = (v: ChangeView, current = true) => act(async () => {
    root.render(<QueryClientProvider client={client}><ChangeBrief view={v} current={current} reload={() => {}} /></QueryClientProvider>)
  })
  const button = (text: string) => [...el.querySelectorAll('button')].find((b) => b.textContent === text)
  const textarea = () => el.querySelector('textarea') as HTMLTextAreaElement
  return { el, root, sent, render, button, textarea }
}

describe('change description editor', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('opens only on a current reading, keeps the draft while the page polls, and saves only what changed', async () => {
    const f = mount(() => new Response(JSON.stringify({ id: CHANGE, editRevision: 3 })))
    await f.render(view('old brief', 1), false)
    expect(f.button('edit')!.disabled).toBe(true)
    await f.render(view('the brief', 2))
    await act(async () => { f.button('edit')!.click() })
    await act(async () => { type(f.textarea(), 'my brief') })
    // A poll brings the same change back; the draft stays.
    await f.render(view('the brief', 2))
    expect(f.textarea().value).toBe('my brief')
    await act(async () => { f.button('Save')!.click() })
    expect(f.sent).toEqual([{ expectedEditRevision: 2, body: 'my brief' }])
    act(() => f.root.unmount())
  })

  it('keeps the draft on a conflict and lets the person keep their edits on top', async () => {
    let conflict = true
    const f = mount(() => conflict
      ? new Response(JSON.stringify({ error: 'moved' }), { status: 409, headers: { 'X-Forge-Deny-Code': 'change-edit-moved' } })
      : new Response(JSON.stringify({ id: CHANGE, editRevision: 4 })))
    await f.render(view('the brief', 2))
    await act(async () => { f.button('edit')!.click() })
    await act(async () => { type(f.textarea(), 'my brief') })
    await act(async () => { f.button('Save')!.click() })
    await vi.waitFor(() => expect(f.el.textContent).toContain('Someone edited this change after you started'))
    expect(f.textarea().value).toBe('my brief')
    await f.render(view('their brief', 3, 'JIRA-9'))
    expect(f.el.textContent).toContain('What was asked for is now: “their brief”')
    await act(async () => { f.button('Keep my edits on top of theirs')!.click() })
    expect(f.textarea().value).toBe('my brief')
    conflict = false
    await act(async () => { f.button('Save')!.click() })
    expect(f.sent[1]).toEqual({ expectedEditRevision: 3, body: 'my brief' })
    act(() => f.root.unmount())
  })

  it('says why a save failed and keeps the draft', async () => {
    const f = mount(() => new Response(JSON.stringify({ error: 'the forge is restarting' }), { status: 503 }))
    await f.render(view('the brief', 0))
    await act(async () => { f.button('edit')!.click() })
    await act(async () => { type(f.textarea(), 'my brief') })
    await act(async () => { f.button('Save')!.click() })
    await vi.waitFor(() => expect(f.el.textContent).toContain('the forge is restarting'))
    expect(f.textarea().value).toBe('my brief')
    act(() => f.root.unmount())
  })
})
