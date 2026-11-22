// @vitest-environment jsdom
// @ts-expect-error -- this package has no Node types; only this read is used.
import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../../data'
import { renderMd } from '../../md'
import type { Message } from '../../wire.gen'
import { chatFeed, chatStreamOf } from './chatStream.fixture'
import { Bubbles, retired, type ChatActivity } from './Bubbles'
import { morphHtml, toThread } from './threadModel'
import { toolSummary } from '../../AgentActions'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// A screen of the given width and pointer, as the page's media queries see it.
function screen(width: number, coarse = false) {
  vi.stubGlobal('matchMedia', (q: string) => {
    const min = q.match(/min-width:\s*(\d+)px/), max = q.match(/max-width:\s*(\d+)px/)
    const matches = q.includes('pointer: coarse') ? coarse
      : (!min || width >= Number(min[1])) && (!max || width <= Number(max[1]))
    return { matches, media: q, addEventListener() {}, removeEventListener() {} }
  })
}

// ResizeObserver whose callbacks a test calls: jsdom lays nothing out, so a
// test says when something grew.
function observers() {
  const all: (() => void)[] = []
  vi.stubGlobal('ResizeObserver', class { constructor(cb: () => void) { all.push(cb) } observe() {} disconnect() {} })
  return { grow: () => act(() => all.forEach((cb) => cb())) }
}

const typeInto = async (el: HTMLElement, words: string) => act(async () => {
  const box = el.querySelector('textarea')!
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(box, words)
  box.dispatchEvent(new Event('input', { bubbles: true }))
})
const button = (el: ParentNode, name: string) => [...el.querySelectorAll('button')].find((b) => b.getAttribute('aria-label') === name || b.textContent === name)

// ---- the thread: what the record reads as ----------------------------------
describe('a record read as a thread', () => {
  const ask = (i: number, text: string): Message => ({ i, role: 'you', text })
  const helper = (i: number, m: Partial<Message>): Message => ({ i, role: 'helper', ...m })
  it('folds every tool call of a run into one, including those of a message with words', () => {
    const turns = toThread([
      ask(0, 'change the heading'),
      helper(1, { text: 'I will find it.', tools: [{ name: 'bash', detail: 'git status', done: true }] }),
      helper(2, { tools: [{ name: 'read', detail: 'index.html', done: true }] }),
      helper(3, { tools: [{ name: 'edit', detail: 'index.html', done: true }, { name: 'bash', detail: 'git commit', done: true }] }),
      helper(4, { text: 'Done.' }),
    ], null, true)
    expect(turns).toHaveLength(1)
    expect(turns[0].parts.map((p) => p.kind)).toEqual(['note', 'run', 'final'])
    const run = turns[0].parts[1]
    expect(run.kind === 'run' && run.steps.length).toBe(4)
    expect(run.kind === 'run' && toolSummary(run.steps)).toBe('ran 2 commands, read 1 file, edited 1 file')
  })
  it('draws the last words as the answer only once the helper handed back', () => {
    const msgs = [ask(0, 'build it'), helper(1, { text: 'Let me create it now:' })]
    expect(toThread(msgs, null, false)[0].parts[0].kind).toBe('note')
    expect(toThread(msgs, null, true)[0].parts[0].kind).toBe('final')
  })
  it.each(['[apptown check]', '[forge check]'])('lets a failed boot check (%s) cancel the claim before it, and the repair be the answer', (mark) => {
    const turns = toThread([
      ask(0, 'make it'),
      helper(1, { text: 'The app is ready.' }),
      ask(2, `${mark} The app did not start: port 3000 refused`),
      helper(3, { text: 'Fixed the port; it starts now.' }),
    ], null, true)
    expect(turns.map((t) => t.ask?.kind)).toEqual(['you', 'check'])
    expect(turns[0].parts[0].kind).toBe('note')
    expect(turns[1].parts[0].kind).toBe('final')
  })
  // The stream leaves done out until a step's result is recorded.
  it('marks a run live while one of its steps is still running, and never once handed back', () => {
    const msgs = [ask(0, 'test it'), helper(1, { tools: [{ name: 'read', detail: 'a', done: true }, { name: 'bash', detail: 'npm test' }] })]
    const live = toThread(msgs, null, false)[0].parts[0]
    expect(live.kind === 'run' && live.live).toBe(true)
    const over = toThread(msgs, null, true)[0].parts[0]
    expect(over.kind === 'run' && over.live).toBe(false)
  })
  it('keeps each part keyed by its message while the draft grows', () => {
    const msgs = [ask(0, 'go'), helper(1, { tools: [{ name: 'read', detail: 'a', done: true }] })]
    const a = toThread(msgs, helper(2, { text: 'Let' }), false)[0].parts.map((p) => p.key)
    const b = toThread(msgs, helper(2, { text: 'Let me look' }), false)[0].parts.map((p) => p.key)
    expect(a).toEqual(['r1', 't2'])
    expect(b).toEqual(a)
  })
})

// While words stream, only the growing text node changes; whatever the
// rendering becomes, the shown words equal a fresh rendering.
describe('streaming words into the shown answer', () => {
  it('extends the same text node as words arrive', () => {
    const el = document.createElement('div')
    morphHtml(el, renderMd('Let me'))
    const text = el.querySelector('p')!.firstChild as Text
    morphHtml(el, renderMd('Let me look first'))
    expect(el.querySelector('p')!.firstChild).toBe(text)
    expect(text.data).toBe('Let me look first')
  })
  it.each([
    ['a link completing', 'See [the do', 'See [the docs](https://example.com) now'],
    ['a fence closing', '```\nnpm te', '```\nnpm test\n```\n\nAll passed.'],
    ['a list becoming a paragraph', '- one', 'one, then two'],
  ])('ends equal to a fresh rendering after %s', (_name, first, then) => {
    const el = document.createElement('div')
    morphHtml(el, renderMd(first))
    morphHtml(el, renderMd(then))
    const fresh = document.createElement('div')
    fresh.innerHTML = renderMd(then)
    expect(el.innerHTML).toBe(fresh.innerHTML)
  })
})

// ---- the live conversation ------------------------------------------------
// user names who reads: the column's choice is kept per person, in memory too.
async function show(reply: (url: string, init?: RequestInit) => Response | Promise<Response>, props: Partial<Parameters<typeof Bubbles>[0]> = {}, user = 'ana') {
  Element.prototype.scrollIntoView = () => {}
  const said: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const u = String(url)
    if (u.endsWith('/auth/me')) return new Response(JSON.stringify({ mode: 'internal', user }))
    if (u.endsWith('/say')) { said.push(JSON.parse(String(init?.body)).text); return new Response('{}') }
    if (u.endsWith('/interrupt')) return new Response('{}')
    return u.includes('/stream') ? reply(u, init) : new Response('{}')
  }))
  const client = newQueryClient()
  const el = document.createElement('div')
  document.body.append(el)
  const root = createRoot(el)
  let activity: ChatActivity | undefined
  await act(async () => {
    root.render(<QueryClientProvider client={client}>
      <Bubbles sessionId="s1" starters={['What does this project do right now?']} onActivity={(a) => { activity = a }} {...props} />
    </QueryClientProvider>)
  })
  return { el, said, client, activity: () => activity, done: () => { act(() => root.unmount()); el.remove(); client.clear() } }
}

describe('a chat the reader may only look at', () => {
  beforeEach(() => screen(390, true))
  afterEach(() => { vi.unstubAllGlobals() })
  const working = () => chatStreamOf([{ i: 0, role: 'you', text: 'make the cat blue' }, { i: 1, role: 'helper', tools: [{ name: 'bash', detail: 'ls', done: false }] }])
  it('shows the conversation and that the helper works, without the ways to act on it', async () => {
    const { el, done } = await show(working, { readOnly: 'bls' })
    await vi.waitFor(() => expect(el.textContent).toContain('make the cat blue'))
    expect(el.querySelector('.chat-readonly .activity')?.textContent).toBe('working')
    expect(el.querySelector('textarea')).toBeNull()
    expect(button(el, 'Stop')).toBeUndefined()
    expect(el.textContent).toContain('Viewing only: this is bls’s chat')
    expect(el.textContent).toContain('Project contributor')
    done()
  })
  it('gives someone who may act the message box and Stop in Send\'s place while the box is empty', async () => {
    const { el, done } = await show(working)
    await vi.waitFor(() => expect(button(el, 'Stop')).toBeDefined())
    expect(el.querySelector('textarea')).not.toBeNull()
    expect(button(el, 'Send')).toBeUndefined()
    await typeInto(el, 'and make it bigger')
    // Typed: a small Stop beside Send, so the box keeps its width.
    expect(button(el, 'Stop')!.classList.contains('small')).toBe(true)
    expect(button(el, 'Send')).toBeDefined()
    done()
  })
})

// The conversation is AppTown's record, read like any other: still on its
// way, unreadable, empty and old each look different, and a failed read is
// never shown as an empty chat with its starters.
describe('the conversation as read', () => {
  beforeEach(() => screen(1440))
  afterEach(() => { vi.unstubAllGlobals() })
  const down = () => new Response(JSON.stringify({ error: 'the worker cannot be reached' }), { status: 400 })
  it('says a conversation that could not be read could not be read, with no starters', async () => {
    const { el, activity, done } = await show(down)
    await vi.waitFor(() => expect(el.querySelector('.error .chip')?.getAttribute('data-help')).toContain('the worker cannot be reached'))
    expect(el.textContent).not.toContain('What does this project do right now?')
    expect(activity()?.read).toBe('stale')
    done()
  })
  it('offers the starters only for a conversation read as empty', async () => {
    const { el, done } = await show(() => chatStreamOf([]))
    await vi.waitFor(() => expect(el.textContent).toContain('What does this project do right now?'))
    done()
  })
  it('keeps an old conversation shown, saying it may be old, when its stream drops and cannot be read again', async () => {
    const feed = chatFeed({ messages: [{ i: 0, role: 'you', text: 'make the cat blue' }] })
    let calls = 0
    const { el, activity, done } = await show(() => calls++ === 0 ? feed.response : down())
    await vi.waitFor(() => expect(el.textContent).toContain('make the cat blue'))
    act(() => feed.drop())
    await vi.waitFor(() => expect(el.querySelector('.chip.unknown')?.getAttribute('aria-label')).toContain('(the worker cannot be reached)'))
    expect(el.textContent).toContain('make the cat blue')
    // An old conversation cannot say whose turn it is.
    expect(activity()?.read).toBe('stale')
    done()
  })
  it('says a stop was the person\'s and stops showing busy', async () => {
    const { el, activity, done } = await show(() => chatStreamOf([
      { i: 0, role: 'you', text: 'build it' },
      { i: 1, role: 'helper', stopped: true, text: 'You stopped me.' },
    ], 'handed-back'))
    await vi.waitFor(() => expect(el.textContent).toContain('Stopped by you'))
    expect(button(el, 'Stop')).toBeUndefined()
    expect(activity()?.busy).toBe(false)
    done()
  })
  // The message just sent shows at once, and gives way to the record's own
  // copy as soon as the record holds a new message from the person.
  it('shows the message just sent until the record holds it', async () => {
    let record: Message[] = [{ i: 0, role: 'helper', text: 'Hello.' }]
    const feed = chatFeed({ messages: record, turn: 'handed-back' })
    const { el, said, done } = await show(() => feed.response)
    await vi.waitFor(() => expect(el.textContent).toContain('Hello.'))
    await typeInto(el, 'make it blue')
    await act(async () => button(el, 'Send')!.click())
    expect(said).toEqual(['make it blue'])
    expect(el.querySelector('.ask.pending')?.textContent).toBe('make it blue')
    record = [...record, { i: 1, role: 'you', text: 'make it blue.' }]
    await act(async () => { feed.push({ messages: record }) })
    await vi.waitFor(() => expect(el.querySelector('.ask.pending')).toBeNull())
    expect(el.querySelectorAll('.ask')).toHaveLength(1)
    done()
  })
  it('retires the message just sent on any later message from the person, never on an earlier one', () => {
    const msgs: Message[] = [{ i: 0, role: 'you', text: 'a' }, { i: 1, role: 'helper', text: 'b' }]
    expect(retired({ at: 2 }, msgs)).toBe(false)
    expect(retired({ at: 1 }, [...msgs, { i: 2, role: 'you', text: 'c' }])).toBe(true)
    expect(retired(null, msgs)).toBe(false)
  })
  // Whose turn it is comes from the server, which reads the record: words
  // followed by a command still running are the helper's turn.
  it.each([['working', true], ['handed-back', false]] as const)('is the helper\'s turn while the server says %s', async (turn, busy) => {
    const { el, activity, done } = await show(() => chatStreamOf([
      { i: 0, role: 'you', text: 'build it' },
      { i: 1, role: 'helper', text: 'Let me create it now:', tools: [{ name: 'bash', detail: 'django-admin startproject app' }] },
    ], turn))
    await vi.waitFor(() => expect(el.textContent).toContain('Let me create it now'))
    expect(activity()).toEqual({ busy, read: 'current' })
    done()
  })
})

// ---- following the newest words ---------------------------------------------
// A size watch keeps the reader at the end whatever grew; the reader's own
// move up always wins, and growth while up offers "New messages below".
describe('following the conversation', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })
  it.each([390, 1440])('follows growth with no new record, lets a wheel up win, and comes back on the cue (%s px)', async (width) => {
    screen(width)
    const ro = observers()
    const windowScroll = vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
    const intoView = vi.fn()
    const { el, done } = await show(() => chatStreamOf([{ i: 0, role: 'you', text: 'build it' }], 'handed-back'))
    Element.prototype.scrollIntoView = intoView
    try {
      await vi.waitFor(() => expect(el.textContent).toContain('build it'))
      const box = el.querySelector<HTMLDivElement>('.chat-scroll')!
      const feed = el.querySelector<HTMLDivElement>('.chat-feed')!
      let height = 1000
      Object.defineProperty(box, 'scrollHeight', { configurable: true, get: () => height })
      Object.defineProperty(box, 'clientHeight', { configurable: true, value: 200 })
      Object.defineProperty(feed, 'offsetHeight', { configurable: true, get: () => height })
      // A picture loads, or the input grows: no new message, the size changed.
      height = 1200; ro.grow()
      expect(box.scrollTop).toBe(1200)
      // The wheel turned up wins at once, even before the scroll it causes lands.
      act(() => { box.dispatchEvent(new WheelEvent('wheel', { deltaY: -40 })) })
      height = 1400; ro.grow()
      expect(box.scrollTop).toBe(1200)
      expect(el.querySelector('.chat-cue')?.textContent).toContain('New messages below')
      await act(async () => (el.querySelector<HTMLButtonElement>('.chat-cue')!).click())
      expect(box.scrollTop).toBe(1400)
      expect(el.querySelector('.chat-cue')).toBeNull()
      height = 1500; ro.grow()
      expect(box.scrollTop).toBe(1500)
      expect(intoView).not.toHaveBeenCalled()
      expect(windowScroll).not.toHaveBeenCalled()
    } finally { done() }
  })
  // The change page keeps its chat mounted while another tab shows; coming
  // back opens at the newest words.
  it('opens at the newest words when shown again', async () => {
    screen(1440)
    observers()
    const client = newQueryClient()
    vi.stubGlobal('fetch', vi.fn(async (url: string) => String(url).includes('/stream') ? chatStreamOf([{ i: 0, role: 'you', text: 'build it' }], 'handed-back') : new Response('{}')))
    const el = document.createElement('div'), root = createRoot(el)
    const render = (hidden: boolean) => act(async () => root.render(<QueryClientProvider client={client}><Bubbles sessionId="s1" hidden={hidden} /></QueryClientProvider>))
    try {
      await render(true)
      await vi.waitFor(() => expect(el.textContent).toContain('build it'))
      const box = el.querySelector<HTMLDivElement>('.chat-scroll')!
      Object.defineProperty(box, 'scrollHeight', { configurable: true, value: 900 })
      box.scrollTop = 0
      await render(false)
      expect(box.scrollTop).toBe(900)
    } finally { act(() => root.unmount()); client.clear() }
  })
})

// ---- the input --------------------------------------------------------------
describe('the input', () => {
  afterEach(() => { vi.unstubAllGlobals() })
  const enter = (el: HTMLElement, shift = false) => act(() => {
    el.querySelector('textarea')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', shiftKey: shift, bubbles: true, cancelable: true }))
  })
  it('sends on Enter with a keyboard, never on Shift+Enter', async () => {
    screen(1440, false)
    const { el, said, done } = await show(() => chatStreamOf([], 'handed-back'))
    await typeInto(el, 'make it blue')
    await enter(el, true)
    expect(said).toEqual([])
    await enter(el)
    await vi.waitFor(() => expect(said).toEqual(['make it blue']))
    done()
  })
  it('leaves Enter to add a line on a touch screen; the button sends', async () => {
    screen(390, true)
    const { el, said, done } = await show(() => chatStreamOf([], 'handed-back'))
    await typeInto(el, 'make it blue')
    await enter(el)
    expect(said).toEqual([])
    expect(el.querySelector('textarea')!.value).toBe('make it blue')
    await act(async () => button(el, 'Send')!.click())
    await vi.waitFor(() => expect(said).toEqual(['make it blue']))
    done()
  })
  it('grows with what is typed and shrinks after sending', async () => {
    screen(390, true)
    vi.stubGlobal('CSS', { supports: () => false })
    const { el, done } = await show(() => chatStreamOf([], 'handed-back'))
    const box = el.querySelector('textarea')!
    expect(box.rows).toBe(1)
    Object.defineProperty(box, 'scrollHeight', { configurable: true, get: () => box.value ? 120 : 42 })
    await typeInto(el, 'A longer draft\nwith several lines')
    expect(box.style.height).toBe('122px')
    await act(async () => button(el, 'Send')!.click())
    expect(box.value).toBe('')
    expect(box.style.height).toBe('44px')
    done()
  })
})

it('does not restore a failed send from a previous session into a fresh chat', async () => {
  screen(1440)
  let refuse!: (r: Response) => void
  vi.stubGlobal('fetch', vi.fn(async (url: string) => String(url).endsWith('/say')
    ? new Promise<Response>(resolve => { refuse = resolve })
    : chatStreamOf([])))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  const render = async (id: string) => act(async () => root.render(<QueryClientProvider client={client}><Bubbles sessionId={id} /></QueryClientProvider>))
  try {
    await render('s1'); await typeInto(el, 'Old message')
    await act(async () => button(el, 'Send')!.click())
    await render('s2'); await typeInto(el, 'New draft')
    await act(async () => refuse(new Response('{"error":"Old send failed"}', { status: 400 })))
    await vi.waitFor(() => expect(el.querySelector('textarea')!.value).toBe('New draft'))
    expect(el.textContent).not.toContain('Old send failed')
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

// Every file entry point sends bytes in multipart form and names the stored
// file in the next message; text never embeds a large base64 data URL.
describe('files in the composer', () => {
  beforeEach(() => screen(1440))
  afterEach(() => { vi.unstubAllGlobals() })
  const mount = async () => {
    const uploads: FormData[] = []
    const said: { text: string; attachments: string[] }[] = []
    const f = await show(() => chatStreamOf([], 'handed-back'))
    const answered = globalThis.fetch
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (String(url).endsWith('/upload')) {
        uploads.push(init!.body as FormData)
        return new Response(JSON.stringify({ name: 'data.csv', path: '/console/uploads/data.csv', size: 8, picture: false }))
      }
      if (String(url).endsWith('/say')) { said.push(JSON.parse(String(init?.body))); return new Response('{}') }
      return answered(url, init)
    }))
    return { ...f, uploads, said }
  }
  it.each(['attach', 'drop', 'paste'])('sends a CSV through %s', async (entry) => {
    const f = await mount()
    const file = new File(['a,b\n1,2\n'], 'data.csv', { type: 'text/csv' })
    await act(async () => {
      if (entry === 'attach') {
        const input = f.el.querySelector('input[type="file"]')!
        Object.defineProperty(input, 'files', { value: [file] })
        input.dispatchEvent(new Event('change', { bubbles: true }))
      } else {
        const event = new Event(entry === 'paste' ? 'paste' : 'drop', { bubbles: true, cancelable: true })
        Object.defineProperty(event, entry === 'paste' ? 'clipboardData' : 'dataTransfer', { value: { files: [file] } })
        f.el.querySelector(entry === 'paste' ? 'textarea' : '.composer')!.dispatchEvent(event)
      }
    })
    expect(f.el.textContent).toContain('data.csv (8 bytes)')
    await act(async () => button(f.el, 'Send')!.click())
    expect(f.uploads).toHaveLength(1)
    expect(f.uploads[0].get('file')).toBeInstanceOf(File)
    expect(f.said).toEqual([{ text: '', attachments: ['data.csv'] }])
    f.done()
  })
  it('refuses empty files, files over 25 MB and more than ten files', async () => {
    const f = await mount()
    for (const [files, reason] of [
      [[new File([], 'empty')], 'empty'],
      [[new File([new Uint8Array(26 * 1024 * 1024)], 'large')], '25 MB is the cap'],
      [Array.from({ length: 11 }, () => new File(['x'], 'one')), 'cap is 10'],
    ] as [File[], string][]) {
      await act(async () => {
        const event = new Event('drop', { bubbles: true, cancelable: true })
        Object.defineProperty(event, 'dataTransfer', { value: { files } })
        f.el.querySelector('.composer')!.dispatchEvent(event)
      })
      expect(f.el.textContent).toContain(reason)
    }
    expect(f.uploads).toHaveLength(0)
    f.done()
  })
})

it('shows a button request as an AppTown event and leaves ordinary words in their own voice', async () => {
  screen(1440)
  const { el, done } = await show(() => chatStreamOf([
    { i: 0, role: 'helper', text: 'Ready.' },
    { i: 1, role: 'you', text: '[apptown button: Check and land] Please check and land this change now.' },
    { i: 2, role: 'you', text: 'My own words' },
  ], 'handed-back'), { readOnly: 'sam' })
  try {
    await vi.waitFor(() => expect(el.querySelector('.ask-lab')).not.toBeNull())
    expect(el.querySelector('.ask-lab')!.textContent).toBe('Check and land')
    expect(el.querySelector('.ask-more')!.textContent).toBe('Please check and land this change now.')
    expect([...el.querySelectorAll('.ask .ask-t:not(.ask-more)')].map(e => e.textContent)).toEqual(['My own words'])
  } finally { done(); vi.unstubAllGlobals() }
})

// The helper's words show as it writes them, as small working words (never
// the green answer), in one text node that grows; once the record holds the
// message the same place shows it, never twice.
it('shows the helper\'s words as they are written, then the recorded message in their place', async () => {
  screen(1440)
  const feed = chatFeed({ messages: [{ i: 0, role: 'you', text: 'make it blue' }] })
  const { el, done } = await show(() => feed.response)
  try {
    await vi.waitFor(() => expect(el.textContent).toContain('make it blue'))
    let node: Node | null = null
    for (const words of ['Let', 'Let me look', 'Let me look first.']) {
      await act(async () => feed.push({ from: 1, messages: [], draft: { i: 1, role: 'helper', text: words } }))
      await vi.waitFor(() => expect(el.querySelector('.note.streaming')?.textContent?.trim()).toBe(words))
      const text = el.querySelector('.note.streaming p')!.firstChild
      if (node) expect(text).toBe(node)
      node = text
    }
    expect(el.querySelector('.final')).toBeNull()
    await act(async () => feed.push({ from: 1, messages: [{ i: 1, role: 'helper', text: 'Let me look first.' }], draft: null, turn: 'handed-back' }))
    await vi.waitFor(() => expect(el.querySelector('.final')?.textContent?.trim()).toBe('Let me look first.'))
    expect([...el.querySelectorAll('.reply > .md')].filter((b) => b.textContent?.trim() === 'Let me look first.')).toHaveLength(1)
  } finally { done(); vi.unstubAllGlobals() }
})

// ---- where a run's steps open -----------------------------------------------
describe('a run of steps', () => {
  afterEach(() => { vi.unstubAllGlobals(); localStorage.clear() })
  const steps = () => chatStreamOf([
    { i: 0, role: 'you', text: 'first ask' },
    { i: 1, role: 'helper', tools: [{ name: 'read', detail: 'one.py', done: true }, { name: 'bash', detail: 'npm test', done: true }] },
    { i: 2, role: 'helper', text: 'Done.' },
    { i: 3, role: 'you', text: 'second ask' },
    { i: 4, role: 'helper', tools: [{ name: 'edit', detail: 'two.py', done: true }] },
    { i: 5, role: 'helper', text: 'Done again.' },
  ], 'handed-back')
  const pills = (el: HTMLElement) => [...el.querySelectorAll<HTMLButtonElement>('.steps-pill')]
  it('is one pill that says how many steps it holds', async () => {
    screen(390, true)
    const { el, done } = await show(steps)
    await vi.waitFor(() => expect(pills(el)).toHaveLength(2))
    expect(pills(el)[0].textContent).toContain('2 steps')
    expect(pills(el)[0].textContent).toContain('read 1 file, ran 1 command')
    done()
  })
  it('opens on a phone as a sheet with that run\'s steps and a Close button', async () => {
    screen(390, true)
    const { el, done } = await show(steps)
    await vi.waitFor(() => expect(pills(el)).toHaveLength(2))
    expect(el.querySelector('.steps-column')).toBeNull()
    await act(async () => pills(el)[0].click())
    const sheet = document.querySelector('.steps-sheet')!
    expect([...sheet.querySelectorAll('code')].map((c) => c.textContent)).toEqual(['one.py', 'npm test'])
    await act(async () => button(sheet, 'Close')!.click())
    expect(document.querySelector('.steps-sheet')).toBeNull()
    done()
  })
  it('opens on a wide screen in the column, at that run, marked', async () => {
    screen(1440)
    const { el, done } = await show(steps)
    await vi.waitFor(() => expect(el.querySelector('.steps-column')).not.toBeNull())
    await act(async () => pills(el)[1].click())
    const marked = el.querySelector('.steps-column .steps-group.selected')!
    expect(marked.textContent).toContain('two.py')
    expect(pills(el)[1].classList.contains('selected')).toBe(true)
    expect(document.querySelector('.steps-sheet')).toBeNull()
    done()
  })
  it('keeps the column closed on a laptop until a pill is tapped', async () => {
    screen(1000)
    const { el, done } = await show(steps, {}, 'lee')
    await vi.waitFor(() => expect(pills(el)).toHaveLength(2))
    expect(el.querySelector('.steps-column')).toBeNull()
    await act(async () => pills(el)[0].click())
    expect(el.querySelector('.steps-column .steps-group.selected')?.textContent).toContain('one.py')
    done()
  })
  it('opens a run when the preview has taken the steps column space', async () => {
    screen(1440)
    const { el, done } = await show(steps, { noColumn: true }, 'preview-reader')
    try {
      await vi.waitFor(() => expect(pills(el)).toHaveLength(2))
      await act(async () => pills(el)[0].click())
      const details = el.querySelector('.steps-column') ?? document.querySelector('.steps-sheet')
      expect(details, 'a pill must expose its steps while preview is beside chat').not.toBeNull()
      expect(details!.textContent).toContain('one.py')
    } finally { done() }
  })
  it('remembers that the person closed the column, and that a pill opened it again', async () => {
    screen(1440)
    const first = await show(steps, {}, 'kim')
    await vi.waitFor(() => expect(first.el.querySelector('.steps-column')).not.toBeNull())
    await act(async () => button(first.el, 'Close the steps')!.click())
    expect(first.el.querySelector('.steps-column')).toBeNull()
    first.done()
    expect(localStorage.getItem('apptown.steps-column.kim')).toBe('closed')
    const again = await show(steps, {}, 'kim')
    await vi.waitFor(() => expect(pills(again.el)).toHaveLength(2))
    expect(again.el.querySelector('.steps-column')).toBeNull()
    await act(async () => pills(again.el)[0].click())
    expect(again.el.querySelector('.steps-column')).not.toBeNull()
    expect(localStorage.getItem('apptown.steps-column.kim')).toBe('open')
    again.done()
  })
  it('keeps the choice for the page when storage is refused', async () => {
    screen(1440)
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('refused') })
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('refused') })
    try {
      const { el, done } = await show(steps, {}, 'max')
      await vi.waitFor(() => expect(el.querySelector('.steps-column')).not.toBeNull())
      await act(async () => button(el, 'Close the steps')!.click())
      expect(el.querySelector('.steps-column')).toBeNull()
      done()
    } finally { vi.restoreAllMocks() }
  })
})

// A picture keeps its box before it loads, so the newest words are not
// pushed out of sight when it arrives.
it('reserves a picture\'s box before it loads', () => {
  const css = (readFileSync('src/pages/console/thread.css', 'utf8') as string).replace(/\/\*[\s\S]*?\*\//g, '')
  expect(css).toMatch(/\.pic \{[^}]*aspect-ratio:\s*3 \/ 2/)
  expect(css).toMatch(/\.pic \{[^}]*object-fit:\s*contain/)
})
