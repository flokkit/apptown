// @vitest-environment jsdom
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

const firstPaint = (search: string, view: object = { change: { id: CHANGE, number: 49, title: 't', state: 'in_progress' } }) => {
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
    expect(html).toContain('connecting…')
    expect(html).toContain('Change chat')
    expect(html).toContain('works on <strong>change 49</strong>')
    expect(html).not.toContain('Project chat')
    expect(html).not.toContain('Nothing is built here')
    expect(html).not.toContain('questions &amp; setup')
  })
  it('the project chat says nothing is built in it', () => {
    const html = firstPaint('')
    expect(html).toContain('Project chat')
    expect(html).toContain('Nothing is built here')
    expect(html).not.toContain('Change chat')
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
// (styles.css hides .console-extra while the message box has focus); the
// conversation's own controls never do.
describe('the chat keeps its input when the intro steps aside', () => {
  it('the intro is in the part that steps aside', () => {
    const el = document.createElement('div')
    el.innerHTML = firstPaint(ON_CHANGE)
    const extra = el.querySelector('.console-extra')!
    expect(extra.textContent).toContain('This chat works on change 49')
    expect(el.querySelector('.console-head')!.closest('.console-extra')).toBeNull()
  })
})

// On a phone the conversation keeps the room: what the chat is about folds
// to one line that says where the change stands, and opens on a tap. On a
// desk the fold is open (its line hidden by styles.css), as the cards
// always were.
describe('what the chat is about folds to one line on a phone', () => {
  const parse = (html: string) => { const el = document.createElement('div'); el.innerHTML = html; return el }
  describe('on a phone', () => {
    const had = window.matchMedia
    beforeEach(() => {
      window.matchMedia = ((q: string) => ({ matches: q === NARROW_QUERY, media: q, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia
    })
    afterEach(() => { window.matchMedia = had })
    it('a change chat starts folded, its line naming the change and where it stands', () => {
      const about = parse(firstPaint(ON_CHANGE)).querySelector('details.console-about')!
      expect(about.hasAttribute('open')).toBe(false)
      expect(about.querySelector('summary')!.textContent).toBe('change 49 · nothing changed yet')
      expect(about.textContent).toContain('This chat works on change 49')
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
        const line = Array.from(el.querySelectorAll('strong')).find((n) => n.textContent?.startsWith('The worker cannot be reached'))!
        expect(line.closest('details')).toBeNull()
      } finally {
        act(() => root.unmount()); client.clear(); vi.unstubAllGlobals()
      }
    })
    it('the line and the change card say the same words', () => {
      const view = { change: { id: CHANGE, number: 49, title: 't', state: 'in_progress' }, commits: [{}] }
      const line = parse(firstPaint(ON_CHANGE, view)).querySelector('details.console-about > summary')!.textContent
      expect(line).toBe('change 49 · saved, not landed yet')
      expect(renderToStaticMarkup(<ChangeContext view={view as unknown as ChangeView} />)).toContain('saved, not landed yet')
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
