// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../data'
import { Bubbles, foldTools, retired, toolSummary, type Bubble } from './Bubbles'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// A turn reads as the helper's words with its working folded between them:
// a run of tool-only messages becomes one fold that says what it amounts to.
describe('the helper\'s tool use', () => {
  it('folds a run of tool-only messages into one, keeping words apart', () => {
    const folded = foldTools([
      { i: 0, role: 'you', text: 'change the heading' },
      { i: 1, role: 'helper', text: 'I will find it.' },
      { i: 2, role: 'helper', tools: [{ name: 'bash', detail: 'git diff --check && git status' }] },
      { i: 3, role: 'helper', tools: [{ name: 'read', detail: '/work/index.html' }] },
      { i: 4, role: 'helper', tools: [{ name: 'edit', detail: '/work/index.html' }, { name: 'bash', detail: 'git commit' }] },
      { i: 5, role: 'helper', text: 'Done.' },
    ])
    expect(folded.map((m) => m.i)).toEqual([0, 1, 2, 5])
    expect(folded[2].tools?.length).toBe(4)
    expect(toolSummary(folded[2].tools!)).toBe('ran 2 commands, read 1 file, edited 1 file')
  })
})

// Anyone who can read the project looks at a chat; only someone who may act
// on it gets the ways to: the message box, the starters, the stop button.
// The reader is told whose chat it is and what acting on it needs.
describe('a chat the reader may only look at', () => {
  afterEach(() => { vi.unstubAllGlobals() })
  const show = async (readOnly?: string) => {
    ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    Element.prototype.scrollIntoView = () => {} // jsdom lays nothing out
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ messages: [
      { i: 0, role: 'you', text: 'make the cat blue' },
      { i: 1, role: 'helper', tools: [{ name: 'bash', detail: 'ls' }] },
    ] }))))
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={newQueryClient()}>
        <Bubbles sessionId="s1" readOnly={readOnly} starters={['What does this project do right now?']} />
      </QueryClientProvider>)
    })
    await vi.waitFor(() => expect(el.textContent).toContain('make the cat blue'))
    return { el, done: () => act(() => root.unmount()) }
  }
  it('shows the conversation without the ways to act on it', async () => {
    const { el, done } = await show('bls')
    expect(el.textContent).toContain('Working…')
    expect(el.querySelector('textarea')).toBeNull()
    expect([...el.querySelectorAll('button')].map((b) => b.textContent)).not.toContain('⏹ stop')
    expect(el.textContent).toContain('Viewing only: this is bls’s chat')
    expect(el.textContent).toContain('Project contributor')
    done()
  })
  it('gives someone who may act the message box and the stop button', async () => {
    const { el, done } = await show(undefined)
    expect(el.querySelector('textarea')).not.toBeNull()
    expect([...el.querySelectorAll('button')].some((b) => b.textContent?.includes('stop'))).toBe(true)
    expect(el.textContent).not.toContain('Viewing only')
    done()
  })
})

// The conversation is AppTown's record, read like any other: still on its
// way, unreadable, empty and old each look different, and a failed read is
// never shown as an empty chat with its starters.
describe('the conversation as read', () => {
  afterEach(() => { vi.unstubAllGlobals() })
  const messages = '/console/s1/messages?from=0'
  const mount = async (reply: () => Response, seed?: Bubble[]) => {
    Element.prototype.scrollIntoView = () => {}
    const said: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (String(url).endsWith('/say')) { said.push(JSON.parse(String(init?.body)).text); return new Response('{}') }
      return reply()
    }))
    const client = newQueryClient()
    if (seed) client.setQueryData(getKey(messages), { messages: seed })
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={client}>
        <Bubbles sessionId="s1" starters={['What does this project do right now?']} />
      </QueryClientProvider>)
    })
    return { el, client, said, done: () => { act(() => root.unmount()); client.clear() } }
  }
  const down = () => new Response(JSON.stringify({ error: 'the worker cannot be reached' }), { status: 400 })

  it('says a conversation that could not be read could not be read, with no starters', async () => {
    const { el, done } = await mount(down)
    await vi.waitFor(() => expect(el.querySelector('.error .chip')?.getAttribute('title')).toContain('the worker cannot be reached'))
    expect(el.textContent).not.toContain('What does this project do right now?')
    done()
  })

  it('offers the starters only for a conversation read as empty', async () => {
    const { el, done } = await mount(() => new Response(JSON.stringify({ messages: [] })))
    await vi.waitFor(() => expect(el.textContent).toContain('What does this project do right now?'))
    done()
  })

  it('keeps an old conversation shown, saying it may be old, when its refresh fails', async () => {
    const { el, done } = await mount(down, [{ i: 0, role: 'you', text: 'make the cat blue' }])
    await vi.waitFor(() => expect(el.querySelector('.chip.unknown')?.getAttribute('aria-label')).toContain('(the worker cannot be reached)'))
    expect(el.textContent).toContain('make the cat blue')
    done()
  })

  // The message just sent shows at once, and gives way to the record's own
  // copy as soon as the record holds a new message from the person.
  it('shows the message just sent until the record holds it', async () => {
    let record: Bubble[] = [{ i: 0, role: 'helper', text: 'Hello.' }]
    const { el, client, said, done } = await mount(() => new Response(JSON.stringify({ messages: record })))
    await vi.waitFor(() => expect(el.textContent).toContain('Hello.'))
    const box = el.querySelector('textarea')!
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!
      set.call(box, 'make it blue')
      box.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => { [...el.querySelectorAll('button')].find((b) => b.textContent === 'Send')!.click() })
    expect(said).toEqual(['make it blue'])
    expect(el.querySelector('.bubble.you.pending')?.textContent).toBe('make it blue')
    record = [...record, { i: 1, role: 'you', text: 'make it blue.' }]
    await act(async () => { await client.refetchQueries({ queryKey: getKey(messages) }) })
    await vi.waitFor(() => expect(el.querySelector('.bubble.you.pending')).toBeNull())
    expect(el.querySelectorAll('.bubble.you')).toHaveLength(1)
    done()
  })

  it('retires the message just sent on any later message from the person, never on an earlier one', () => {
    const msgs: Bubble[] = [{ i: 0, role: 'you', text: 'a' }, { i: 1, role: 'helper', text: 'b' }]
    expect(retired({ at: 2 }, msgs)).toBe(false)
    expect(retired({ at: 1 }, [...msgs, { i: 2, role: 'you', text: 'c' }])).toBe(true)
    expect(retired(null, msgs)).toBe(false)
  })
})

// Whose turn it is comes from the server, which reads the record: words
// followed by a command still running are the helper's turn, not the
// person's. The page once judged it from the last bubble alone, and showed
// "your turn" while "Let me create it now:" ran its command.
describe('whose turn it is', () => {
  afterEach(() => { vi.unstubAllGlobals() })
  const busyFor = async (turn: string) => {
    Element.prototype.scrollIntoView = () => {}
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ turn, messages: [
      { i: 0, role: 'you', text: 'build it' },
      { i: 1, role: 'helper', text: 'Let me create it now:', tools: [{ name: 'bash', detail: 'django-admin startproject app' }] },
    ] }))))
    let busy: boolean | undefined
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={newQueryClient()}>
        <Bubbles sessionId="s1" onBusy={(b) => { busy = b }} />
      </QueryClientProvider>)
    })
    await vi.waitFor(() => expect(el.textContent).toContain('Let me create it now'))
    act(() => root.unmount())
    return busy
  }
  it('is the helper\'s while the server says it is working', async () => {
    expect(await busyFor('working')).toBe(true)
  })
  it('is the person\'s once the server says the helper handed back', async () => {
    expect(await busyFor('handed-back')).toBe(false)
  })
})
