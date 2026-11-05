// @vitest-environment jsdom
// @ts-expect-error -- this package has no Node types; only this read is used.
import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { getKey, newQueryClient } from '../data'
import Console from './Console'

// The terminal stands still: no canvas, no socket.
vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    loadAddon() {} open() {} focus() {} reset() {} write() {} dispose() {}
    onData() { return { dispose() {} } }
    onResize() { return { dispose() {} } }
  },
}))
vi.mock('@xterm/addon-fit', () => ({ FitAddon: class { fit() {} } }))
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
import { NARROW_QUERY } from '../chrome'
import { ChangeContext } from './console/ChangeBar'
import type { ChangeView } from '../api'

// Record ids are UUIDv7 strings; a change is named by its number, which the
// page knows once the change is read.
const CHANGE = '00000000-0049-7000-8000-000000000000'
const ON_CHANGE = `?change=${CHANGE}`

const firstPaint = (search: string, view: object = { statusLabel: 'waiting for you', change: { id: CHANGE, number: 49, title: 't', state: 'in_progress' } }) => {
  window.history.replaceState(null, '', `/projects/p/console${search}`)
  const client = newQueryClient()
  client.setQueryData(getKey(`/changes/${CHANGE}`), view)
  return renderToStaticMarkup(
    <MemoryRouter initialEntries={[`/projects/p/console${search}`]}>
      <QueryClientProvider client={client}>
        <Routes><Route path="/projects/:slug/console" element={<Console />} /></Routes>
      </QueryClientProvider>
    </MemoryRouter>)
}

describe('a chat says which kind it is from its first paint', () => {
  // Before the session answers, a change chat already says it works on
  // that change; it never shows the project chat's "nothing is built here"
  // while it connects.
  it('a change chat names its change while it connects', () => {
    const html = firstPaint(ON_CHANGE)
    expect(html).not.toContain('connecting…')
    expect(html).toContain('Loading…')
    expect(html).toContain('Change chat')
    expect(html).toContain('Starting your helper…')
    expect(html).toContain('aria-label="your message"')
    // A change chat carries no intro card: its box says what to do.
    expect(html).not.toContain('Checks and an approval come before it lands.')
    expect(html).not.toContain('Project chat')
    expect(html).not.toContain('The helper opens a change for the work.')
    expect(html).not.toContain('questions &amp; setup')
  })
  it('the project chat says nothing is built in it', () => {
    const html = firstPaint('')
    expect(html).toContain('Project chat')
    expect(html).toContain('The helper opens a change for the work.')
    expect(html).not.toContain('Change chat')
  })
  it('anchors the offscreen terminal at the top instead of below the conversation', () => {
    const el = document.createElement('div')
    el.innerHTML = firstPaint('')
    const terminal = el.querySelector<HTMLElement>('.console-term')!
    expect(terminal.classList.contains('console-term-offscreen')).toBe(true)
    const css = readFileSync('src/pages/Console.css', 'utf8')
    expect(css).toMatch(/\.console-term-offscreen\s*\{[^}]*position:\s*absolute;\s*top:\s*0;/)
  })
  // The intro uses the glossary's names for the two chats, not room names.
  it('neither chat calls itself a workshop or a front desk', () => {
    for (const search of [ON_CHANGE, '']) {
      const el = document.createElement('div')
      el.innerHTML = firstPaint(search)
      const words = [el.textContent ?? '', ...Array.from(el.querySelectorAll('[title]'), (n) => n.getAttribute('title') ?? '')].join(' ')
      expect(words.toLowerCase()).not.toMatch(/workshop|front desk|ordinary words/)
    }
  })
})

// On a phone, what the chat is about steps aside while you type
// (styles/console.css hides .console-extra while the message box has focus); the
// conversation's own controls never do.
describe('the chat keeps its input when the intro steps aside', () => {
  it('the intro is in the part that steps aside', () => {
    const el = document.createElement('div')
    el.innerHTML = firstPaint('')
    const extra = el.querySelector('.console-extra')!
    expect(extra.textContent).toContain('The helper opens a change for the work.')
    expect(el.querySelector('.console-head')!.closest('.console-extra')).toBeNull()
  })
})

// On a phone the conversation keeps the room: what the chat is about folds
// to one line that says where the change stands, and opens on a tap. On a
// desk the fold is open (its line hidden by styles/console.css), as the cards
// always were.
describe('what the chat is about folds to one line on a phone', () => {
  const parse = (html: string) => { const el = document.createElement('div'); el.innerHTML = html; return el }
  describe('on a phone', () => {
    const had = window.matchMedia
    beforeEach(() => {
      window.matchMedia = ((q: string) => ({ matches: q === NARROW_QUERY, media: q, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia
    })
    afterEach(() => { window.matchMedia = had })
    // The head names the change once; the fold line says where it stands.
    it('a change chat starts folded: the head names the change, the fold line says where it stands', () => {
      const page = parse(firstPaint(ON_CHANGE))
      const about = page.querySelector('details.console-about')!
      expect(about.hasAttribute('open')).toBe(false)
      expect(about.querySelector('summary')!.textContent).toBe('About this change · ● waiting for you')
      expect(page.querySelector('.console-head h1')!.textContent).toBe('Change 49')
    })
    // A worker that cannot be reached qualifies the conversation itself:
    // that line stays in view with the fold closed, for someone who may
    // only look as much as for the chat's owner.
    it('says the conversation is the last copy when the worker cannot be reached, outside the fold', async () => {
      Element.prototype.scrollIntoView = () => {}
      vi.stubGlobal('WebSocket', class { send() {} close() {} })
      vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
      vi.stubGlobal('fetch', vi.fn(async (url: string) => {
        const u = String(url)
        if (u === '/api/v1/projects/p/console') {
          return new Response(JSON.stringify({ sessions: [{ id: 's1', projectSlug: 'p', model: 'm', createdAt: '', state: 'unavailable',
            changeId: CHANGE, openedBy: 'ana', mayAct: false, placement: 'worker', recordAsOf: '2026-10-16T01:00:00Z' }] }))
        }
        if (u === `/api/v1/changes/${CHANGE}`) return new Response(JSON.stringify({ change: { id: CHANGE, number: 49, title: 't', state: 'in_progress' } }))
        if (u.includes('/messages')) return new Response(JSON.stringify({ messages: [{ i: 0, role: 'helper', text: 'Done.' }] }))
        if (u.endsWith('/changes')) return new Response(JSON.stringify({ changes: [] }))
        return new Response('{}')
      }))
      window.history.replaceState(null, '', `/projects/p/console${ON_CHANGE}`)
      const client = newQueryClient()
      const el = document.createElement('div')
      const root = createRoot(el)
      await act(async () => {
        root.render(<MemoryRouter initialEntries={[`/projects/p/console${ON_CHANGE}`]}>
          <QueryClientProvider client={client}>
            <Routes><Route path="/projects/:slug/console" element={<Console />} /></Routes>
          </QueryClientProvider>
        </MemoryRouter>)
      })
      try {
        await vi.waitFor(() => expect(el.textContent).toContain('The worker cannot be reached'))
        expect(el.querySelector('details.console-about')!.hasAttribute('open')).toBe(false)
        // The switch shares the About line and stays outside its disclosure.
        const aboutLine = el.querySelector('.console-about-line')!
        expect(aboutLine.querySelector('summary')?.textContent).toContain('About this change')
        expect(aboutLine.querySelector('[role="switch"]')).not.toBeNull()
        expect(el.querySelectorAll('[role="switch"]')).toHaveLength(1)
        expect(aboutLine.querySelector('[role="switch"]')!.closest('details')).toBeNull()
        await act(async () => { (aboutLine.querySelector('[role="switch"]') as HTMLInputElement).click() })
        expect(el.querySelector('details.console-about')!.hasAttribute('open')).toBe(false)
        const line = Array.from(el.querySelectorAll('strong')).find((n) => n.textContent?.startsWith('The worker cannot be reached'))!
        expect(line.closest('details')).toBeNull()
      } finally {
        act(() => root.unmount()); client.clear(); vi.unstubAllGlobals()
      }
    })
    it('the fold line and the change card say the same words', () => {
      const view = { statusLabel: 'waiting for you', change: { id: CHANGE, number: 49, title: 't', state: 'in_progress' }, commits: [{}] }
      const line = parse(firstPaint(ON_CHANGE, view)).querySelector('details.console-about > summary')!.textContent
      expect(line).toBe('About this change · ● waiting for you')
      expect(renderToStaticMarkup(<ChangeContext view={view as unknown as ChangeView} />)).toContain('waiting for you')
    })
  })
  it('on a desk the fold is open', () => {
    expect(parse(firstPaint(ON_CHANGE)).querySelector('details.console-about')!.hasAttribute('open')).toBe(true)
  })
  // A project chat whose intro was dismissed has nothing to fold.
  it('a project chat with its intro dismissed shows no empty fold', () => {
    localStorage.setItem('chat-intro-seen-p', '1')
    try {
      expect(parse(firstPaint('')).querySelector('details.console-about')).toBeNull()
    } finally { localStorage.removeItem('chat-intro-seen-p') }
  })
})

// Once the conversation starts, phone controls fold without removing the
// preview or landing actions. The choice belongs to this page view only.
describe('phone change controls', () => {
  let messageCount = 1
  let messagesReady = Promise.resolve()
  beforeEach(() => {
    messagesReady = Promise.resolve()
    localStorage.clear()
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: q === NARROW_QUERY, media: q, addEventListener() {}, removeEventListener() {} }))
    vi.stubGlobal('WebSocket', class { send() {} close() {} })
    vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      const u = String(url)
      if (u.includes('/messages')) await messagesReady
      const data = u.endsWith('/projects/p/console')
        ? { sessions: [{ id: 's1', projectSlug: 'p', state: 'detached', changeId: CHANGE, openedBy: 'ana', mayAct: true }] }
        : u.endsWith(`/changes/${CHANGE}`)
          ? { statusLabel: 'in progress', change: { id: CHANGE, number: 49, title: 't', state: 'in_progress' } }
          : u.endsWith('/preview') ? { state: 'live', url: 'https://preview.example' }
            : u.includes('/messages') ? { messages: Array.from({ length: messageCount }, (_, i) => ({ i, role: 'you', text: 'Hello' })), turn: 'handed-back' }
              : { changes: [] }
      return new Response(JSON.stringify(data))
    }))
  })
  afterEach(() => vi.unstubAllGlobals())

  async function mount() {
    window.history.replaceState(null, '', `/projects/p/console${ON_CHANGE}`)
    const client = newQueryClient()
    const el = document.createElement('div')
    document.body.append(el)
    const root = createRoot(el)
    await act(async () => {
      root.render(<MemoryRouter initialEntries={[`/projects/p/console${ON_CHANGE}`]}>
        <QueryClientProvider client={client}>
          <Routes><Route path="/projects/:slug/console" element={<Console />} /></Routes>
        </QueryClientProvider>
      </MemoryRouter>)
    })
    await vi.waitFor(() => expect(el.textContent).toContain('▶ Open'))
    return { el, client, done: () => { act(() => root.unmount()); el.remove(); client.clear() } }
  }
  function mainActions(el: HTMLElement) {
    for (const text of ['▶ Open', '✓ Check and land']) {
      const button = [...el.querySelectorAll('button')].find(b => b.textContent === text)!
      expect(button).toBeDefined()
      expect(button.closest('[hidden]')).toBeNull()
    }
  }
  function secondary(el: HTMLElement, hidden: boolean) {
    for (const selector of ['.tryit-restart', '.head-menu', '.console-secondary']) {
      expect((el.querySelector(selector) as HTMLElement).hidden).toBe(hidden)
    }
    expect(el.querySelector('.console-secondary summary')?.textContent).toBe('About this change')
    expect(el.querySelector('.console-secondary [role="switch"]')).not.toBeNull()
  }
  it('folds after a message, opens from either toggle, and always keeps the main actions', async () => {
    messageCount = 1
    // Preview readiness does not mean the independent conversation read has
    // arrived. Hold it back to exercise that ordering on every run.
    let releaseMessages!: () => void
    messagesReady = new Promise<void>(resolve => { releaseMessages = resolve })
    const { el, done } = await mount()
    try {
      const toggle = el.querySelector<HTMLButtonElement>('.change-controls-toggle')!
      expect(toggle?.getAttribute('aria-expanded')).toBe('true')
      await act(async () => { releaseMessages() })
      await vi.waitFor(() => expect(toggle?.getAttribute('aria-expanded')).toBe('false'))
      expect(toggle.getAttribute('aria-label')).toBe('Show change controls')
      secondary(el, true); mainActions(el)
      act(() => toggle.click())
      expect(toggle.getAttribute('aria-expanded')).toBe('true')
      expect(toggle.getAttribute('aria-label')).toBe('Hide change controls')
      secondary(el, false); mainActions(el)
      act(() => el.querySelector<HTMLButtonElement>('.change-controls-title')!.click())
      expect(toggle.getAttribute('aria-expanded')).toBe('false')
      secondary(el, true); mainActions(el)
    } finally { done() }
  })
  it('starts an empty chat unfolded, folds on its first message, and preserves an explicit choice on refresh', async () => {
    messageCount = 0
    const { el, client, done } = await mount()
    try {
      const toggle = el.querySelector<HTMLButtonElement>('.change-controls-toggle')!
      expect(toggle?.getAttribute('aria-expanded')).toBe('true')
      secondary(el, false); mainActions(el)
      await act(async () => { client.setQueryData(getKey('/console/s1/messages?from=0'), { messages: [{ i: 0, role: 'you', text: 'Hello' }] }); await new Promise(r => setTimeout(r, 0)) })
      expect(toggle.getAttribute('aria-expanded')).toBe('false')
      act(() => toggle.click())
      await act(async () => { client.setQueryData(getKey('/console/s1/messages?from=0'), { messages: [{ i: 0, role: 'you', text: 'Hello' }, { i: 1, role: 'helper', text: 'Hi' }] }); await new Promise(r => setTimeout(r, 0)) })
      expect(toggle.getAttribute('aria-expanded')).toBe('true')
      act(() => el.querySelector<HTMLTextAreaElement>('textarea')!.focus())
      expect(toggle.getAttribute('aria-expanded')).toBe('false')
      secondary(el, true); mainActions(el)
      act(() => el.querySelector<HTMLTextAreaElement>('textarea')!.blur())
      expect(toggle.getAttribute('aria-expanded')).toBe('true')
    } finally { done() }
  })
  it('keeps desktop controls unfolded after messages', async () => {
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
    messageCount = 1
    const { el, done } = await mount()
    try {
      expect(el.querySelector('.change-controls-toggle')).toBeNull()
      expect(el.querySelector<HTMLDetailsElement>('.head-menu')!.hidden).toBe(false)
      expect(el.querySelector<HTMLDetailsElement>('.console-about')!.open).toBe(true)
      mainActions(el)
    } finally { done() }
  })
})

// Opening and attaching must leave the same composer (and its draft) in place.
describe('writing while the helper starts', () => {
  afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); localStorage.clear(); sessionStorage.clear() })
  async function mount(fail = false, sayFails = false) {
    localStorage.clear(); sessionStorage.clear()
    let opened!: (r: Response) => void
    const sockets: { onopen?: () => void; onclose?: () => void }[] = []
    const said: string[] = []
    vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
    vi.stubGlobal('WebSocket', class {
      constructor() { sockets.push(this) }
      send() {} close() {}
      onopen?: () => void
      onclose?: () => void
    })
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      const u = String(url)
      if (u.endsWith('/projects/p/console')) {
        if (init?.method === 'POST') return new Promise<Response>(resolve => { opened = resolve })
        return new Response('{"sessions":[]}')
      }
      if (u.endsWith('/say')) { said.push(JSON.parse(String(init?.body)).text); return sayFails ? new Response('{"error":"Could not send"}', { status: 400 }) : new Response('{}') }
      if (u.includes('/messages')) return new Response('{"messages":[]}')
      return new Response('{"changes":[]}')
    }))
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => root.render(<MemoryRouter initialEntries={['/projects/p/console']}>
      <QueryClientProvider client={client}><Routes><Route path="/projects/:slug/console" element={<Console />} /></Routes></QueryClientProvider>
    </MemoryRouter>))
    await vi.waitFor(() => expect(opened).toBeDefined())
    return { el, said, client,
      open: async () => act(async () => opened(new Response(JSON.stringify(fail ? { error: 'No helper here' } : {
        session: { id: 's1', projectSlug: 'p', state: 'detached', openedBy: 'ana', mayAct: true },
      }), { status: fail ? 400 : 200 }))),
      attach: () => act(() => sockets[sockets.length - 1].onopen!()),
      done: () => { act(() => root.unmount()); client.clear() },
    }
  }
  async function type(el: HTMLElement, words: string) {
    await act(async () => {
      const box = el.querySelector('textarea')!
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(box, words)
      box.dispatchEvent(new Event('input', { bubbles: true }))
    })
  }
  it('keeps the composer enabled, appends to one queued bubble, and sends once across reconnects', async () => {
    const { el, said, open, attach, done } = await mount()
    try {
      const composer = el.querySelector('textarea')!
      expect(composer.disabled).toBe(false)
      expect(el.querySelector('.chat-starting')?.textContent).toContain('Starting your helper…')
      for (const words of ['Make it blue', 'And larger']) {
        await type(el, words)
        await act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Send')!.click())
      }
      expect(said).toEqual([])
      expect(el.querySelectorAll('.bubble.you.pending')).toHaveLength(1)
      expect(el.textContent).toContain('will send when your helper is ready')
      await type(el, 'Still writing')
      await open()
      expect(el.querySelector('.chat-starting')?.textContent).toContain('Connecting…')
      expect(el.querySelector('textarea')).toBe(composer)
      expect(composer.value).toBe('Still writing')
      expect(said).toEqual([])
      await attach()
      expect(el.querySelector('.chat-starting')).toBeNull()
      expect(said).toEqual(['Make it blue\n\nAnd larger'])
      await attach()
      expect(said).toHaveLength(1)
      expect(el.textContent).not.toContain('will send when your helper is ready')
    } finally { done() }
  })
  it('keeps a queued message and the newer draft when zooming into the terminal', async () => {
    const { el, said, open, attach, done } = await mount()
    try {
      await type(el, 'Make it blue')
      await act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Send')!.click())
      await type(el, 'Still writing')
      const composer = el.querySelector('textarea')!
      await open()
      await act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Terminal ▾')!.click())
      expect(el.querySelector<HTMLElement>('.chat-pane')!.hidden).toBe(true)
      await attach()
      expect(said).toEqual(['Make it blue'])
      await act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === '← back to the chat')!.click())
      expect(el.querySelector('textarea')).toBe(composer)
      expect(composer.value).toBe('Still writing')
      expect(el.querySelector<HTMLElement>('.chat-pane')!.hidden).toBe(false)
    } finally { done() }
  })
  it('restores a refused queued message beside the newer draft without resending it', async () => {
    const { el, said, open, attach, done } = await mount(false, true)
    try {
      await type(el, 'Make it blue')
      await act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Send')!.click())
      await type(el, 'Still writing')
      await open(); await attach()
      await vi.waitFor(() => expect(el.textContent).toContain('Could not send'))
      expect(el.querySelector('textarea')!.value).toBe('Make it blue\n\nStill writing')
      await attach()
      expect(said).toEqual(['Make it blue'])
    } finally { done() }
  })
  it('keeps the existing error and project link when opening fails', async () => {
    const { el, open, done } = await mount(true)
    try {
      await open()
      await vi.waitFor(() => expect(el.textContent).toContain("Chat can't open here: No helper here"))
      expect(el.querySelector('a')?.getAttribute('href')).toBe('/projects/p')
      expect(el.querySelector('.chat-starting')).toBeNull()
    } finally { done() }
  })
})
