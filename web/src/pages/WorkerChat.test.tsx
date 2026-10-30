import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../data'
import type { ConsoleSessionView, WorkerChatList } from '../wire.gen'
import { ChangeWorkerChats, WorkerSaveLine } from './WorkerChat'

// Record ids are UUIDv7 strings.
const CHANGE = '00000000-0007-7000-8000-000000000000'

const session = (over: Partial<ConsoleSessionView>): ConsoleSessionView => ({
  id: 'abc123', projectSlug: 'game', model: 'm', createdAt: '2026-09-30T00:00:00Z', state: 'detached', changeId: CHANGE,
  placement: 'worker', ...over,
} as ConsoleSessionView)

const line = (s: ConsoleSessionView, seeded = true) => {
  const client = newQueryClient()
  if (seeded) client.setQueryData(getKey('/projects/game/console'), { sessions: [s] })
  return renderToStaticMarkup(<QueryClientProvider client={client}><WorkerSaveLine slug="game" session={s} /></QueryClientProvider>)
}

describe('a chat on the worker says where its work stands', () => {
  it('says it runs on the worker and when it was last saved', () => {
    const html = line(session({ saved: { at: '2026-09-30T01:00:00Z', outcome: 'saved', commit: '0123456789abcdef' }, recordAsOf: '2026-09-30T01:01:00Z' }))
    expect(html).toContain('Runs on the worker.')
    expect(html).toContain('Last saved at')
    expect(html).toContain('01234567')
    expect(html).toContain('Conversation as of')
  })
  it('says a refused save was refused, why, and where its commits are', () => {
    const html = line(session({ saved: { at: '2026-09-30T01:00:00Z', outcome: 'refused', reason: 'a .apptown path', kept: true } }))
    expect(html).toContain('Last save refused')
    expect(html).toContain('a .apptown path')
    expect(html).toContain('stay on the worker')
  })
  it('says a save that was not confirmed was not confirmed, never saved', () => {
    const html = line(session({ saved: { at: '2026-09-30T01:00:00Z', outcome: 'not-confirmed', reason: 'the worker could not be reached' } }))
    expect(html).toContain('not confirmed')
    expect(html).not.toContain('Last saved at')
  })
  it('says a worker that cannot be reached cannot be reached, with how old the conversation shown is', () => {
    const html = line(session({ state: 'unavailable', recordAsOf: '2026-09-30T01:01:00Z' }))
    expect(html).toContain('cannot be reached')
    expect(html).toContain('as of')
    expect(html).toContain('Not saved yet')
  })
  it('shows nothing for a chat on AppTown', () => {
    expect(line(session({ placement: undefined }))).toBe('')
  })
  it('says an unreadable last save is unknown, not absent', () => {
    const html = line(session({ saved: { at: '0001-01-01T00:00:00Z', outcome: 'unknown', reason: 'its last save could not be read: bad json' } }))
    expect(html).toContain('unknown')
    expect(html).not.toContain('Not saved yet')
  })
  it('says it is still checking before the first answer, not that nothing is saved', () => {
    const html = line(session({}), false)
    expect(html).toContain('Loading…')
    expect(html).not.toContain('Not saved yet')
  })
})

const changePage = async (seed: WorkerChatList | null, fail: boolean) => {
  const client = newQueryClient()
  // A static render is one moment: the read has already been asked again
  // and failed, rather than being asked again on mount.
  client.setDefaultOptions({ queries: { retryOnMount: false } })
  const key = getKey(`/changes/${CHANGE}/worker-chats`)
  if (seed) client.setQueryData(key, seed)
  if (fail) await client.fetchQuery({ queryKey: key, queryFn: () => Promise.reject(new Error('AppTown did not answer')), retry: false }).catch(() => {})
  return renderToStaticMarkup(<QueryClientProvider client={client}><ChangeWorkerChats changeId={CHANGE} /></QueryClientProvider>)
}

describe("a change's chats on the worker", () => {
  it("offers an ended chat's refused last save, with why, for download", async () => {
    const html = await changePage({ chats: [{ chat: 'abc123def456', live: false, saved: { at: '2026-09-29T10:00:00Z', outcome: 'refused', reason: 'a file over the size limit', kept: true },
      refused: { commit: 'fedcba9876543210', reason: 'a file over the size limit', at: '2026-09-29T10:00:00Z' } }] }, false)
    expect(html).toContain(`/api/v1/changes/${CHANGE}/worker-chats/abc123def456/refused`)
    expect(html).toContain('a file over the size limit')
    expect(html).toContain('has ended')
    expect(html).toContain('kept on AppTown')
    expect(html).not.toContain('stay on the worker')
  })
  it("shows a live chat's last save and nothing to download", async () => {
    const html = await changePage({ chats: [{ chat: 'abc123def456', live: true, saved: { at: '2026-09-30T01:00:00Z', outcome: 'saved', commit: '0123456789' } }] }, false)
    expect(html).toContain('is working on this change')
    expect(html).toContain('Last saved at')
    expect(html).not.toContain('/refused')
  })
  it('says an ended chat whose work was all saved says so', async () => {
    const html = await changePage({ chats: [{ chat: 'abc123def456', live: false, saved: { at: '2026-09-30T01:00:00Z', outcome: 'nothing-new', commit: '0123456789' } }] }, false)
    expect(html).toContain('All its committed work was saved')
  })
  it('shows nothing for a change with no worker chats', async () => {
    expect(await changePage({ chats: [] }, false)).toBe('')
  })
  it('says a failed first read failed, rather than showing nothing', async () => {
    expect(await changePage(null, true)).toContain('unknown')
  })
  it('marks an empty answer whose refresh failed as the last answer read', async () => {
    expect(await changePage({ chats: [] }, true)).toContain('unknown')
  })
})
