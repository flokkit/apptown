// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { identityChanged, newQueryClient, useFollow, type Read } from './data'
import { foldChat, type ChatRecord } from './pages/console/Bubbles'
import { chatFeed, type ChatFeed } from './pages/console/chatStream.fixture'
import type { ConsoleStreamUpdate } from './wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// A chat followed over its stream: what the reader holds, its read state,
// and the addresses it asked.
async function follow(answers: (() => Response)[]) {
  const asked: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    asked.push(String(url))
    const next = answers.shift()
    return next ? next() : new Promise<Response>(() => {})
  }))
  const client = newQueryClient()
  const seen: { read: Read<ChatRecord> | null } = { read: null }
  function Reader() {
    seen.read = useFollow<ChatRecord, ConsoleStreamUpdate>('/console/s1/stream', foldChat, (held) => held.cursor)
    return null
  }
  const el = document.createElement('div'), root = createRoot(el)
  await act(async () => root.render(<QueryClientProvider client={client}><Reader /></QueryClientProvider>))
  return { asked, client, read: () => seen.read!, done: () => { act(() => root.unmount()); client.clear() } }
}

const texts = (r: Read<ChatRecord>) => r.data?.messages.map((m) => m.text)

describe('following a chat', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // A dropped stream keeps what was held, says it may be old, and
  // reconnects with its cursor: it is sent only what it lacks and ends up
  // with every message once.
  it('reconnects from its cursor after the stream drops, without gaps or duplicates', async () => {
    const first: ChatFeed = chatFeed({ messages: [{ i: 0, role: 'you', text: 'one' }], cursor: '1.aaaa' })
    const second: ChatFeed = chatFeed({ from: 1, messages: [{ i: 1, role: 'helper', text: 'two' }], cursor: '2.bbbb' })
    const f = await follow([() => first.response, () => second.response])
    try {
      await vi.waitFor(() => expect(texts(f.read())).toEqual(['one']))
      expect(f.read().current).toBe(true)
      act(() => first.drop())
      await vi.waitFor(() => expect(f.read().refreshFailed).toBe(true))
      expect(texts(f.read())).toEqual(['one'])
      await vi.waitFor(() => expect(texts(f.read())).toEqual(['one', 'two']), { timeout: 3000 })
      expect(f.asked).toEqual(['/api/v1/console/s1/stream?after=', '/api/v1/console/s1/stream?after=1.aaaa'])
      expect(f.read().refreshFailed).toBe(false)
    } finally { f.done() }
  })

  // A reader refused at the door, or told mid-stream that it may no longer
  // look, is denied: it is not asked again.
  it('is denied when refused, at the door or mid-stream', async () => {
    const door = await follow([() => new Response('{"error":"not yours"}', { status: 403 })])
    try {
      await vi.waitFor(() => expect(door.read().denied).toBe(true))
      expect(door.read().data).toBeNull()
    } finally { door.done() }
    vi.unstubAllGlobals()

    const feed = chatFeed({ messages: [{ i: 0, role: 'you', text: 'secret' }] })
    const later = await follow([() => feed.response])
    try {
      await vi.waitFor(() => expect(texts(later.read())).toEqual(['secret']))
      // The server ends the stream with its refusal.
      await act(async () => feed.refuse(403, 'Viewing this chat needs the project'))
      await vi.waitFor(() => expect(later.read().denied).toBe(true))
      expect(later.asked).toHaveLength(1)
    } finally { later.done() }
  })

  // Another person signing in drops what was read for the last one and
  // follows again from nothing.
  it('starts again from nothing when the identity changes', async () => {
    const first = chatFeed({ messages: [{ i: 0, role: 'you', text: 'for alice' }], cursor: '1.alice' })
    const second = chatFeed({ messages: [], cursor: '0' })
    const f = await follow([() => first.response, () => second.response])
    try {
      await vi.waitFor(() => expect(texts(f.read())).toEqual(['for alice']))
      await act(async () => identityChanged(f.client))
      await vi.waitFor(() => expect(f.asked).toHaveLength(2))
      expect(f.asked[1]).toBe('/api/v1/console/s1/stream?after=')
      await vi.waitFor(() => expect(texts(f.read())).toEqual([]))
    } finally { f.done() }
  })
})
