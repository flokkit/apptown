import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../data'
import type { ChatHistoryEntry } from '../../wire.gen'
import { ConversationLog, chatByline, chatOpening } from './Conversation'

// Record ids are UUIDv7 strings.
const CHANGE = '00000000-0007-7000-8000-000000000000'

const chat = (over: Partial<ChatHistoryEntry>): ChatHistoryEntry => ({
  id: 'abc', createdAt: '2026-10-01T09:30:00Z', actor: 'bls', ended: true, imported: false,
  messages: [{ i: 0, role: 'you', text: 'Make the duck faster\nand bluer' }, { i: 1, role: 'helper', text: 'Done.' }], ...over,
} as ChatHistoryEntry)

describe('a past chat says who opened it, when, and where it came from', () => {
  it('names the opener and the time', () => {
    expect(chatByline(chat({}))).toBe('opened 2026-10-01 09:30 by bls')
  })
  it('says a chat moved from another forge is history only', () => {
    expect(chatByline(chat({ imported: true, actor: 'alice on forge.example.com' }))).toContain('alice on forge.example.com · moved from another AppTown, history only')
  })
  it('lists a chat by its first words', () => {
    expect(chatOpening(chat({}))).toBe('Make the duck faster')
  })
  it('shows the opener on a change even when it has one chat', () => {
    const client = newQueryClient()
    client.setQueryData(getKey(`/changes/${CHANGE}/transcript`), { sessions: [chat({ messages: [{ i: 0, role: 'you', text: 'Make the duck faster' }] } as Partial<ChatHistoryEntry>)] })
    const html = renderToStaticMarkup(<QueryClientProvider client={client}><ConversationLog changeId={CHANGE} /></QueryClientProvider>)
    expect(html).toContain('by bls')
    expect(html).toContain('Make the duck faster')
  })
})

describe('a pasted picture stays in the chat after it ends', () => {
  it('shows the picture from the chat history, opening full size, without the plumbing sentence', () => {
    const client = newQueryClient()
    client.setQueryData(getKey(`/changes/${CHANGE}/transcript`), { sessions: [chat({ id: 'c1', messages: [{ i: 0, role: 'you', text: 'this screen (I pasted a picture — look at /console/uploads/picture-2.png)' }] } as Partial<ChatHistoryEntry>)] })
    const html = renderToStaticMarkup(<QueryClientProvider client={client}><ConversationLog changeId={CHANGE} /></QueryClientProvider>)
    expect(html).toContain(`src="/api/v1/changes/${CHANGE}/transcript/c1/pictures/picture-2.png"`)
    expect(html).toContain(`href="/api/v1/changes/${CHANGE}/transcript/c1/pictures/picture-2.png"`)
    expect(html).not.toContain('I pasted')
  })
  it('lists a chat that opened with a picture by saying so', () => {
    expect(chatOpening(chat({ messages: [{ i: 0, role: 'you', text: '(I pasted a picture — look at /console/uploads/picture-1.png)' }] } as Partial<ChatHistoryEntry>))).toBe('(a pasted picture)')
  })
})

describe('a chat record that cannot be read is said, never hidden', () => {
  it('says how many could not be read and never claims there are none', () => {
    const client = newQueryClient()
    client.setQueryData(getKey(`/changes/${CHANGE}/transcript`), { sessions: [], unreadable: 1 })
    const html = renderToStaticMarkup(<QueryClientProvider client={client}><ConversationLog changeId={CHANGE} /></QueryClientProvider>)
    expect(html).toContain('1 chat record in AppTown could not be read; some chats may be missing here.')
    expect(html).not.toContain('No chat on this change')
  })
  it('names the folders to an administrator', () => {
    const client = newQueryClient()
    client.setQueryData(getKey(`/changes/${CHANGE}/transcript`), { sessions: [], unreadable: 2, unreadableFolders: ['abc', 'def'] })
    const html = renderToStaticMarkup(<QueryClientProvider client={client}><ConversationLog changeId={CHANGE} /></QueryClientProvider>)
    expect(html).toContain('2 chat records in AppTown could not be read')
    expect(html).toContain('Folders: abc, def.')
  })
})
