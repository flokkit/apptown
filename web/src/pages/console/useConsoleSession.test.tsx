// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes, useNavigate, type NavigateFunction } from 'react-router-dom'
import { getKey, newQueryClient } from '../../data'
import Console from '../Console'

// Record ids are UUIDv7 strings.
const C1 = '00000000-0001-7000-8000-000000000000'
const C2 = '00000000-0002-7000-8000-000000000000'

// Which chat the page opens is decided on an answer this page read itself:
// a chat is opened only when AppTown said, just now and for this chat
// address, that there is none. A failed read, an old answer, or an answer
// read for another change's chat opens nothing.

// The terminal is its own part (useTerminal.test.tsx); here it stands still.
vi.mock('./useTerminal', () => ({ useTerminal: () => 'attached' }))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

type Reply = { status?: number; body: unknown } | Promise<{ status?: number; body: unknown }>
const chat = (id: string, changeId: string | null = null) => ({ id, projectSlug: 'p', model: 'm', createdAt: '', state: 'detached', changeId, openedBy: 'bls', mayAct: true })

// AppTown answers the page's requests: `list` answers the project's chats (a
// function, so a test can change its answer or hold it), every open is
// recorded and answered with a new chat, and the other reads answer empty.
function forge(list: () => Reply, openReply: (body: { changeId?: string; placement?: string }) => { status?: number; body: unknown; code?: string } = (b) => ({ status: 201, body: { session: chat('new-' + (b.changeId ?? 'desk'), b.changeId ?? null) } })) {
  const opens: { changeId?: string; placement?: string }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const u = String(url)
    const json = (r: { status?: number; body: unknown; code?: string }) =>
      new Response(JSON.stringify(r.body), { status: r.status ?? 200, headers: r.code ? { 'X-AppTown-Deny-Code': r.code } : {} })
    if (u === '/api/v1/projects/p/console' && init?.method === 'POST') {
      const body = JSON.parse(String(init.body ?? '{}'))
      opens.push(body)
      return json(openReply(body))
    }
    if (u === '/api/v1/projects/p/console') return json(await list())
    if (u.includes('/messages')) return json({ body: { messages: [] } })
    if (u.endsWith('/changes')) return json({ body: { changes: [] } })
    if (u.startsWith('/api/v1/changes/')) return json({ body: { change: { id: C1, number: 1, title: 'a change', state: 'in progress' }, state: '' } })
    return json({ body: {} })
  }))
  return opens
}

let navigate: NavigateFunction
function Nav() { navigate = useNavigate(); return null }

async function page(search: string, client: QueryClient = newQueryClient()) {
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => {
    root.render(
      <MemoryRouter initialEntries={[`/projects/p/console${search}`]}>
        <QueryClientProvider client={client}>
          <Nav />
          <Routes><Route path="/projects/:slug/console" element={<Console />} /></Routes>
        </QueryClientProvider>
      </MemoryRouter>)
  })
  return { el, client, done: () => { act(() => root.unmount()); client.clear() } }
}

// A wait long enough for any open the page was going to send to be sent.
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 30)) })

beforeEach(() => {
  sessionStorage.clear()
  Element.prototype.scrollIntoView = () => {}
})
afterEach(() => vi.unstubAllGlobals())

describe('choosing a chat', () => {
  it('opens no chat when the list of chats could not be read, and says so', async () => {
    const opens = forge(() => ({ status: 400, body: { error: 'database unavailable' } }))
    const { el, done } = await page(`?change=${C1}`)
    await vi.waitFor(() => expect(el.textContent).toContain("This project's chats could not be read: database unavailable"))
    await settle()
    expect(opens).toEqual([])
    done()
  })

  it('opens one chat when AppTown says this change has none', async () => {
    const opens = forge(() => ({ body: { sessions: [chat('other', C2)] } }))
    const { done } = await page(`?change=${C1}`)
    await vi.waitFor(() => expect(opens).toEqual([{ changeId: C1 }]))
    await settle()
    expect(opens).toHaveLength(1)
    done()
  })

  it("takes this change's chat when AppTown has one, opening none", async () => {
    const opens = forge(() => ({ body: { sessions: [chat('mine', C1)] } }))
    const { el, done } = await page(`?change=${C1}`)
    await vi.waitFor(() => expect(el.querySelector('[title="mine"]')).not.toBeNull())
    await settle()
    expect(opens).toEqual([])
    done()
  })

  for (const [what, held] of [['an empty', []], ['a full', [chat('other', C2)]]] as const) {
    it(`opens no chat on ${what} old answer whose refresh failed, and says it may be old`, async () => {
      const opens = forge(() => ({ status: 400, body: { error: 'database unavailable' } }))
      const client = newQueryClient()
      client.setQueryData(getKey('/projects/p/console'), { sessions: held })
      const { el, done } = await page(`?change=${C1}`, client)
      await vi.waitFor(() => expect(el.querySelector('.out-of-date')?.getAttribute('aria-label')).toContain('(database unavailable)'))
      await settle()
      expect(opens).toEqual([])
      done()
    })
  }

  it('shows a refused open and does not try again when the list is read again', async () => {
    let sessions: unknown[] = []
    const opens = forge(() => ({ body: { sessions } }), () => ({ status: 409, body: { error: 'the worker cannot take this chat' } }))
    const { el, client, done } = await page(`?change=${C1}`)
    await vi.waitFor(() => expect(el.textContent).toContain('the worker cannot take this chat'))
    // A newer answer (another change's chat opened meanwhile) still holds
    // none for this change.
    sessions = [chat('other', C2)]
    await act(async () => { await client.refetchQueries({ queryKey: getKey('/projects/p/console') }) })
    await settle()
    expect(opens).toHaveLength(1)
    done()
  })

  it("moving to another change's chat waits for that address's own reading", async () => {
    let answer: Reply = { body: { sessions: [chat('one', C1)] } }
    const opens = forge(() => answer)
    const { el, done } = await page(`?change=${C1}`)
    await vi.waitFor(() => expect(el.querySelector('[title="one"]')).not.toBeNull())
    // The next reading is on its way: the answer held from change 1's
    // reading (no chat for change 2) must not open one.
    let release!: (r: { body: unknown }) => void
    answer = new Promise((r) => { release = r })
    await act(async () => { navigate(`/projects/p/console?change=${C2}`) })
    await settle()
    expect(opens).toEqual([])
    // It answers with change 2's chat, opened elsewhere meanwhile.
    await act(async () => { release({ body: { sessions: [chat('one', C1), chat('two', C2)] } }) })
    await vi.waitFor(() => expect(el.querySelector('[title="two"]')).not.toBeNull())
    // And back to the project's chat, which also waits for its own reading.
    answer = new Promise((r) => { release = r })
    await act(async () => { navigate('/projects/p/console') })
    await settle()
    expect(opens).toEqual([])
    await act(async () => { release({ body: { sessions: [chat('one', C1), chat('two', C2), chat('desk', null)] } }) })
    await vi.waitFor(() => expect(el.querySelector('[title="desk"]')).not.toBeNull())
    await settle()
    expect(opens).toEqual([])
    done()
  })
})

// AppTown's code, never its sentence, says when the chat can open on the
// AppTown instead of the worker that refused it.
describe('a chat the worker refused', () => {
  const refused = async (code: string) => {
    const opens = forge(() => ({ body: { sessions: [] } }),
      (b) => b.placement === 'forge'
        ? { status: 201, body: { session: chat('local', C1) } }
        : { status: 409, code, body: { error: 'the worker cannot take this chat: it cannot reach the registry npm; open it on AppTown' } })
    const r = await page(`?change=${C1}`)
    await vi.waitFor(() => expect(r.el.textContent).toContain('the worker cannot take this chat'))
    return { ...r, opens }
  }
  it('offers AppTown when the refusal is coded so, and opens it there', async () => {
    const { el, opens, done } = await refused('chat-on-forge')
    const button = [...el.querySelectorAll('button')].find((b) => b.textContent === 'Open it on AppTown instead')!
    expect(button).toBeDefined()
    await act(async () => { button.click() })
    await vi.waitFor(() => expect(opens).toEqual([{ changeId: C1 }, { changeId: C1, placement: 'forge' }]))
    done()
  })
  it('does not offer AppTown on the same sentence without the code', async () => {
    const { el, done } = await refused('')
    expect(el.textContent).not.toContain('Open it on AppTown instead')
    done()
  })
})
