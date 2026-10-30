// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import type { Project } from '../../../api'
import { newQueryClient } from '../../../data'
import { DangerZone, GeneralTab } from './General'

// Record ids are UUIDv7 strings.
const PROJECT = '00000000-0001-7000-8000-000000000000'
const PURGE = '00000000-0007-7000-8000-000000000000'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const project = (blurb: string, name = 'Shop', settingsRevision = 1): Project =>
  ({ id: PROJECT, slug: 'shop', name, namespace: '', icon: '', blurb, createdAt: '', requireIndependentApproval: false, settingsRevision } as Project)

function type(input: HTMLInputElement, value: string) {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

// mount renders the form in a page; `answer` is what the next save gets.
function mount(answer: () => Response, onChanged: () => void = () => {}) {
  const sent: Record<string, unknown>[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'PATCH') { sent.push(JSON.parse(String(init.body))); return answer() }
    return new Response(url.endsWith('/projects') || url.endsWith('/deployments') ? '[]' : '{}')
  }))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  const render = (p: Project, current = true) => act(async () => {
    root.render(<QueryClientProvider client={client}><MemoryRouter><GeneralTab project={p} current={current} onChanged={onChanged} /></MemoryRouter></QueryClientProvider>)
  })
  const inputs = () => [...el.querySelectorAll('input')] as HTMLInputElement[]
  const button = (text: string) => [...el.querySelectorAll('button')].find((b) => b.textContent === text)
  return { el, root, sent, render, inputs, button }
}

describe('project settings form', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('shows description ownership and sends an explicit empty value to resume suggestions', async () => {
    const f = mount(() => new Response(JSON.stringify({ ...project(''), blurbSource: 'generated' })))
    try {
      await f.render({ ...project('Truck roster'), blurbSource: 'person' })
      expect(f.el.textContent).toContain('yours')
      await act(async () => { type(f.inputs()[2], '') })
      await act(async () => { f.button('Save')!.click() })
      expect(f.sent).toEqual([{ expectedRevision: 1, blurb: '' }])
      await f.render({ ...project('', 'Shop', 2), blurbSource: 'generated' })
      expect(f.el.textContent).toContain('suggested')
    } finally { act(() => f.root.unmount()) }
  })

  // The form starts only from an answer the page read itself, keeps a
  // person's edits through refreshes, and saves only what they changed with
  // the revision they started from.
  it('saves only the changed fields, with the revision the edit started from', async () => {
    const f = mount(() => new Response(JSON.stringify(project('fresh blurb', 'Shop 2', 3))))
    // An older answer: nothing can be edited or saved until the page has
    // read its own (a failed refresh leaves it like this).
    await f.render(project('old blurb'), false)
    expect(f.el.querySelector('fieldset')!.disabled).toBe(true)
    expect(f.button('Save')!.disabled).toBe(true)

    await f.render(project('fresh blurb', 'Shop', 2))
    expect(f.inputs()[2].value).toBe('fresh blurb')
    await act(async () => { type(f.inputs()[0], 'Shop 2') })
    // A background refresh after the edit keeps the person's work.
    await f.render(project('fresh blurb', 'Shop', 2))
    expect(f.inputs()[0].value).toBe('Shop 2')
    await act(async () => { f.button('Save')!.click() })
    expect(f.sent).toEqual([{ expectedRevision: 2, name: 'Shop 2' }])
    // The committed answer is the new starting point.
    await act(async () => { type(f.inputs()[0], 'Shop 3') })
    await act(async () => { f.button('Save')!.click() })
    expect(f.sent[1]).toEqual({ expectedRevision: 3, name: 'Shop 3' })
    act(() => f.root.unmount())
  })

  it('follows newer readings until someone edits, and never an older one that lands late', async () => {
    const f = mount(() => new Response('{}'))
    await f.render(project('first', 'Shop', 1))
    await f.render(project('second', 'Shop', 2))
    expect(f.inputs()[2].value).toBe('second')
    await f.render(project('first', 'Shop', 1))
    expect(f.inputs()[2].value).toBe('second')
    act(() => f.root.unmount())
  })

  // Someone saved in between: the save is refused, the draft is kept, and the
  // person sees what changed and chooses.
  it('keeps the draft on a conflict and lets the person keep their edits on top', async () => {
    let conflict = true
    const reread = vi.fn()
    const f = mount(() => conflict
      ? new Response(JSON.stringify({ error: 'settings moved' }), { status: 409, headers: { 'X-AppTown-Deny-Code': 'settings-moved' } })
      : new Response(JSON.stringify(project('theirs', 'Mine', 4))), reread)
    await f.render(project('ours', 'Shop', 2))
    await act(async () => { type(f.inputs()[0], 'Mine') })
    await act(async () => { f.button('Save')!.click() })
    await vi.waitFor(() => expect(f.el.textContent).toContain('Someone saved these settings after you started editing'))
    expect(f.inputs()[0].value).toBe('Mine')
    expect(f.button('Save')!.disabled).toBe(true)
    // The refusal makes the page read the settings again, once; until the
    // newer reading arrives there is nothing to keep edits on top of.
    expect(reread).toHaveBeenCalledTimes(1)
    expect(f.button('Keep my edits on top of theirs')!.disabled).toBe(true)
    // The page reads the settings as they are now.
    await f.render(project('theirs', 'Shop', 3))
    expect(f.el.textContent).toContain('One line about it is now “theirs”')
    await act(async () => { f.button('Keep my edits on top of theirs')!.click() })
    // The refusal is answered; its message goes with it.
    expect(f.el.textContent).not.toContain('settings moved')
    expect(f.inputs()[2].value).toBe('theirs')
    expect(f.inputs()[0].value).toBe('Mine')
    conflict = false
    await act(async () => { f.button('Save')!.click() })
    expect(f.sent[1]).toEqual({ expectedRevision: 3, name: 'Mine' })
    act(() => f.root.unmount())
  })

  it('keeps the draft and says why when a save fails', async () => {
    const f = mount(() => new Response(JSON.stringify({ error: 'AppTown is restarting' }), { status: 503 }))
    await f.render(project('ours'))
    await act(async () => { type(f.inputs()[0], 'Mine') })
    await act(async () => { f.button('Save')!.click() })
    await vi.waitFor(() => expect(f.el.textContent).toContain('AppTown is restarting'))
    expect(f.inputs()[0].value).toBe('Mine')
    expect(f.button('Save')!.disabled).toBe(false)
    act(() => f.root.unmount())
  })
  it('moves the address with a plain save and shows the committed record', async () => {
    const committed = { ...project('ours', 'Shop', 2), slug: 'renamed' }
    const f = mount(() => new Response(JSON.stringify(committed)))
    await f.render(project('ours'))
    await act(async () => { type(f.el.querySelector<HTMLInputElement>('[aria-label="Address"]')!, 'renamed') })
    await act(async () => { f.button('Save')!.click() })
    await vi.waitFor(() => expect(f.el.textContent).toContain('saved'))
    expect(f.sent).toEqual([{ expectedRevision: 1, slug: 'renamed' }])
    expect((f.el.querySelector('[aria-label="Address"]') as HTMLInputElement).value).toBe('renamed')
    act(() => f.root.unmount())
  })

})

describe('deleting a project', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  const mount = async (el: HTMLElement) => {
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={newQueryClient()}><MemoryRouter><DangerZone slug="shop" /></MemoryRouter></QueryClientProvider>)
    })
    return root
  }
  const button = (el: HTMLElement, text: string) => [...el.querySelectorAll('button')].find((b) => b.textContent?.startsWith(text))!

  // Deleting returns at once: the card then says what the deletion is
  // waiting on, and Cancel is offered while nothing was taken down.
  it('asks for the deletion and then shows what it waits on', async () => {
    let state: Record<string, unknown> = { projectId: PROJECT, slug: 'shop', state: 'active' }
    const deletes: unknown[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'DELETE') {
        deletes.push(JSON.parse(String(init.body)))
        state = { projectId: PROJECT, slug: 'shop', state: 'deleting', requestedBy: 'boss', remaining: ['staging', 'main'] }
        return new Response(JSON.stringify(state), { status: 202 })
      }
      if (url.endsWith('/deletion')) return new Response(JSON.stringify(state))
      return new Response(url.endsWith('/deployments') ? '[{"name":"main"},{"name":"staging"}]' : '{}')
    }))
    const el = document.createElement('div')
    const root = await mount(el)
    await vi.waitFor(() => expect(button(el, 'Delete')).toBeTruthy())
    await act(async () => { button(el, 'Delete').click() })
    await act(async () => { type(el.querySelector('input')!, 'shop') })
    await act(async () => { button(el, 'Delete shop').click() })
    await vi.waitFor(() => expect(el.textContent).toContain('staging, main'))
    expect(deletes).toEqual([{ confirm: 'shop' }])
    expect(button(el, 'Cancel deletion').disabled).toBe(false)
    act(() => root.unmount())
  })

  // What the deletion would take down is shown only once it is known: while
  // the deployments are still being read the row says so and Delete stays
  // greyed; an empty answer reads "no deployments".
  it('keeps Delete greyed until the deployments are known', async () => {
    let answer: (r: Response) => void = () => {}
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.endsWith('/deletion')) return new Response(JSON.stringify({ projectId: PROJECT, slug: 'shop', state: 'active' }))
      if (url.endsWith('/deployments')) return new Promise<Response>((res) => { answer = res })
      return new Response('{}')
    }))
    const el = document.createElement('div')
    const root = await mount(el)
    await vi.waitFor(() => expect(button(el, 'Delete')).toBeTruthy())
    await act(async () => { button(el, 'Delete').click() })
    await act(async () => { type(el.querySelector('input')!, 'shop') })
    expect(el.textContent).toContain('reading…')
    expect(el.textContent).not.toContain('no deployments')
    expect(button(el, 'Delete shop').disabled).toBe(true)
    await act(async () => { answer(new Response('[]')) })
    await vi.waitFor(() => expect(el.textContent).toContain('no deployments'))
    expect(button(el, 'Delete shop').disabled).toBe(false)
    act(() => root.unmount())
  })

  // A refresh that fails after a good answer leaves the deployments unknown:
  // the old answer is marked stale, the row says unknown, and Delete greys.
  it('greys Delete when the latest read of the deployments failed', async () => {
    let fail = false
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.endsWith('/deletion')) return new Response(JSON.stringify({ projectId: PROJECT, slug: 'shop', state: 'active' }))
      if (url.endsWith('/deployments')) return fail ? new Response(JSON.stringify({ error: 'inventory unavailable' }), { status: 403 }) : new Response('[]')
      return new Response('{}')
    }))
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={client}><MemoryRouter><DangerZone slug="shop" /></MemoryRouter></QueryClientProvider>)
    })
    await vi.waitFor(() => expect(button(el, 'Delete')).toBeTruthy())
    await act(async () => { button(el, 'Delete').click() })
    await act(async () => { type(el.querySelector('input')!, 'shop') })
    await vi.waitFor(() => expect(el.textContent).toContain('no deployments'))
    fail = true
    await act(async () => { await client.invalidateQueries() })
    await vi.waitFor(() => expect(el.textContent).toContain('inventory unavailable'))
    expect(el.textContent).not.toContain('no deployments')
    expect(button(el, 'Delete shop').disabled).toBe(true)
    act(() => root.unmount())
  })

  // Once its purge was admitted, a deleted project cannot be restored, and
  // the card says so beside the greyed button.
  it('greys Restore once the purge has started', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.endsWith('/deletion')) {
        return new Response(JSON.stringify({ projectId: PROJECT, slug: 'shop', state: 'deleted', requestedBy: 'boss', deletedAt: '2026-10-01T00:00:00Z', keptUntil: '2026-10-31T00:00:00Z', purgeStartedAt: '2026-10-09T00:00:00Z', purge: { id: PURGE, state: 'failed', detail: 'purge incomplete: set 3 of main' } }))
      }
      return new Response(url.endsWith('/deployments') ? '[]' : '{}')
    }))
    const el = document.createElement('div')
    const root = await mount(el)
    await vi.waitFor(() => expect(el.textContent).toContain('Deleted'))
    expect(button(el, 'Restore project').disabled).toBe(true)
    expect(el.textContent).toContain('purge has started; restoring is no longer available')
    expect(el.textContent).toContain('purge incomplete: set 3 of main')
    act(() => root.unmount())
  })
})
