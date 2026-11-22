// A chat's event stream for tests: the server side of
// GET /console/{sid}/stream, which a test feeds update by update.
import type { ConsoleStreamUpdate, Message } from '../../wire.gen'

export type ChatFeed = {
  response: Response
  // push sends one update; by default the whole conversation (from 0).
  push: (u: Partial<ConsoleStreamUpdate>) => void
  // drop ends the stream as a lost connection does.
  drop: () => void
  // refuse ends it as the server does when the reader may no longer look.
  refuse: (status: number, error: string) => void
}

export function chatFeed(first?: Partial<ConsoleStreamUpdate>): ChatFeed {
  let ctl!: ReadableStreamDefaultController<Uint8Array>
  const body = new ReadableStream<Uint8Array>({ start(c) { ctl = c } })
  const enc = new TextEncoder()
  let n = 0
  const feed: ChatFeed = {
    // Made when the page asks for it, as a server answers.
    get response() { return new Response(body, { headers: { 'Content-Type': 'text/event-stream' } }) },
    push: (u) => {
      n++
      ctl.enqueue(enc.encode(`data: ${JSON.stringify({ from: 0, messages: [], draft: null, turn: 'working', cursor: `c${n}`, ...u })}\n\n`))
    },
    drop: () => ctl.error(new Error('the connection dropped')),
    refuse: (status, error) => {
      ctl.enqueue(enc.encode(`event: refused\ndata: ${JSON.stringify({ error, status })}\n\n`))
      ctl.close()
    },
  }
  if (first) feed.push(first)
  return feed
}

// chatStreamOf is a stream that sends a conversation once and stays open.
export const chatStreamOf = (messages: Message[], turn = 'working') => chatFeed({ messages, turn }).response
