// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { getKey, newQueryClient } from '../../data'
import Console from '../Console'
import { renderToStaticMarkup } from 'react-dom/server'
import { LAND_REQUEST, StartedChanges, WorkshopActions } from './ChangeBar'

// Record ids are UUIDv7 strings.
const CHANGE = '00000000-0001-7000-8000-000000000000'
const OTHER = '00000000-0004-7000-8000-000000000000'


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

// The message Check and land posts in the person's name names the button
// they pressed, not another one.
describe('check and land in the chat', () => {
  it('says the button the person pressed', () => {
    expect(LAND_REQUEST).toContain('I pressed Check and land')
    expect(LAND_REQUEST).not.toMatch(/Land button/)
  })
})

// What a change chat shows of its change is read like any other answer: the
// bar offers nothing on a change it has not read, and a status whose refresh
// failed ("shipped", "needs you", the three verbs) stays, marked as old.
describe("the change a chat works on", () => {
  const view = (state: string, doing = '') => ({ change: { id: CHANGE, number: 1, title: 'Blue cat', state }, state: doing })
  let change: () => Response
  beforeEach(() => {
    Element.prototype.scrollIntoView = () => {}
    vi.stubGlobal('WebSocket', class { send() {} close() {} })
    vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      const u = String(url)
      if (u === '/api/v1/projects/p/console') {
        return new Response(JSON.stringify({ sessions: [{ id: 's1', projectSlug: 'p', model: 'm', createdAt: '', state: 'detached', changeId: CHANGE, openedBy: 'bls', mayAct: true }] }))
      }
      if (u === `/api/v1/changes/${CHANGE}`) return change()
      if (u.includes('/messages')) return new Response(JSON.stringify({ messages: [] }))
      if (u.endsWith('/changes')) return new Response(JSON.stringify({ changes: [] }))
      return new Response('{}')
    }))
  })
  afterEach(() => vi.unstubAllGlobals())
  const down = () => new Response(JSON.stringify({ error: 'database unavailable' }), { status: 400 })

  const mount = async (client: QueryClient = newQueryClient()) => {
    window.history.replaceState(null, '', `/projects/p/console?change=${CHANGE}`)
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<MemoryRouter initialEntries={[`/projects/p/console?change=${CHANGE}`]}>
        <QueryClientProvider client={client}>
          <Routes><Route path="/projects/:slug/console" element={<Console />} /></Routes>
        </QueryClientProvider>
      </MemoryRouter>)
    })
    return { el, done: () => { act(() => root.unmount()); client.clear() } }
  }

  it('offers no Check and land on a change it could not read, and says so', async () => {
    change = down
    const { el, done } = await mount()
    await vi.waitFor(() => expect(el.querySelector('.error .chip')?.getAttribute('title')).toContain('database unavailable'))
    expect(el.textContent).not.toContain('Check and land')
    done()
  })

  for (const [what, v, shows] of [
    ['shipped', view('shipped'), 'Shipped to main.'],
    ['waiting on a person', view('in progress', 'waiting on you'), 'Review and approve →'],
    ['in progress', view('in progress'), '✓ Check and land'],
  ] as const) {
    it(`keeps a change ${what} shown under a stale note when its refresh fails`, async () => {
      change = down
      const client = newQueryClient()
      client.setQueryData(getKey(`/changes/${CHANGE}`), v)
      const { el, done } = await mount(client)
      // The bar and the change's card wait for the chats list (who may act),
      // a read independent of the change's refresh; either may answer first,
      // so wait for the whole picture, not the stale note alone (a push gate
      // saw the note before the list answered, 11 October).
      await vi.waitFor(() => {
        expect([...el.querySelectorAll('.chip.unknown')].some(chip => chip.getAttribute('aria-label')?.includes('(database unavailable)'))).toBe(true)
        expect(el.textContent).toContain(shows)
        expect(el.textContent).toContain('Blue cat')
      })
      done()
    })
  }
})

// The changes a chat started are a read like any other: a failed read is
// not "none", and an old empty answer says it may be old.
describe('the changes a chat started', () => {
  afterEach(() => vi.unstubAllGlobals())
  const mount = async (reply: () => Response, seed?: unknown) => {
    vi.stubGlobal('fetch', vi.fn(async () => reply()))
    const client = newQueryClient()
    if (seed) client.setQueryData(getKey('/console/s1/changes'), seed)
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<MemoryRouter><QueryClientProvider client={client}><StartedChanges sessionId="s1" /></QueryClientProvider></MemoryRouter>)
    })
    return { el, done: () => { act(() => root.unmount()); client.clear() } }
  }
  const down = () => new Response(JSON.stringify({ error: 'database unavailable' }), { status: 400 })
  it('says a failed read failed, never shows it as none', async () => {
    const { el, done } = await mount(down)
    await vi.waitFor(() => expect(el.querySelector('.error .chip')?.getAttribute('title')).toContain('database unavailable'))
    done()
  })
  it('shows nothing for an answer of none', async () => {
    const { el, done } = await mount(() => new Response(JSON.stringify({ changes: [] })))
    await act(async () => { await new Promise((r) => setTimeout(r, 20)) })
    expect(el.textContent).toBe('')
    done()
  })
  it('says an old answer of none may be old when its refresh fails', async () => {
    const { el, done } = await mount(down, { changes: [] })
    await vi.waitFor(() => expect([...el.querySelectorAll('.chip.unknown')].some(chip => chip.getAttribute('aria-label')?.includes('(database unavailable)'))).toBe(true))
    done()
  })
})

// The bar names who the change waits on; it never claims the checks passed,
// which a pass with unreadable evidence would make untrue.
describe('the workshop bar', () => {
  it('says the change waits for a person without claiming the checks passed', () => {
    const view = { change: { id: OTHER, number: 4, title: 'Blue cat', state: 'in_progress' }, state: 'waiting on you' } as Parameters<typeof WorkshopActions>[0]['view']
    const html = renderToStaticMarkup(<QueryClientProvider client={newQueryClient()}><MemoryRouter>
      <WorkshopActions changeId={OTHER} view={view} robotBusy={false} />
    </MemoryRouter></QueryClientProvider>)
    expect(html).toContain('waiting for a person to approve')
    expect(html).not.toContain('passed')
  })
})
