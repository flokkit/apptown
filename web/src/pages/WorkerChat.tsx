import { PRODUCT } from '../brand'
import React from 'react'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { ConsoleSessionList, ConsoleSessionView, WorkerChatList } from '../wire.gen'

// What a chat on the worker shows (design/operations/workers.md):
// where its work stands, on its own page and on its change's page, and a
// change's refused last saves. What cannot be read is said to be unknown,
// never shown as saved, stopped or empty.

// when is a time, with its date when it is not today: a copy from yesterday
// must not read as one from this afternoon.
export function when(t?: string | null): string {
  if (!t) return ''
  const d = new Date(t)
  return d.toDateString() === new Date().toDateString() ? d.toLocaleTimeString() : d.toLocaleString()
}

// SaveStatus is one worker chat's save line: its last save, and how fresh
// AppTown's copy of its conversation is.
export function SaveStatus({ s }: { s: ConsoleSessionView }) {
  const saved = s.saved
  let line: React.ReactNode
  if (!saved) {
    line = 'Not saved yet: after each turn its commits are saved to the change.'
  } else if (saved.outcome === 'saved') {
    line = <>Last saved at {when(saved.at)}{saved.commit && <> (<code>{saved.commit.slice(0, 8)}</code>)</>}.</>
  } else if (saved.outcome === 'refused') {
    // A live chat's refused commits wait on the worker; an ended chat's are
    // kept on AppTown, and its change page offers them.
    line = <span style={{ color: 'var(--bad)' }}>Last save refused at {when(saved.at)}: {saved.reason}{' '}
      {s.state === 'dead' ? `Its commits are kept on ${PRODUCT}.` : 'The chat was told; its commits stay on the worker until they are fixed.'}</span>
  } else if (saved.outcome === 'not-confirmed') {
    line = <>Last save not confirmed at {when(saved.at)} ({saved.reason}); it is tried again after the next turn.</>
  } else if (saved.outcome === 'nothing-new') {
    line = <>All its committed work was saved (checked {when(saved.at)}).</>
  } else if (saved.outcome === 'unknown') {
    line = <>Its last save is unknown: {saved.reason}.</>
  } else {
    line = <>Not saving: {saved.reason}</>
  }
  return (
    <>
      {line}{' '}
      {s.state === 'unavailable'
        ? <strong>The worker cannot be reached; the conversation shown is as of {when(s.recordAsOf) || 'its last copy'}.</strong>
        : s.recordAsOf && <>Conversation as of {when(s.recordAsOf)}.</>}
    </>
  )
}

// WorkerSaveLine is the chat page's line for a chat that opened on the worker.
export function WorkerSaveLine({ slug, session }: { slug: string; session: ConsoleSessionView }) {
  const list = useGet<ConsoleSessionList>(`/projects/${slug}/console`, { poll: 5000 })
  if (session.placement !== 'worker') return null
  const s = list.data?.sessions.find((x) => x.id === session.id)
  return (
    <div className="muted small" style={{ marginBottom: 8 }} data-testid="worker-save-line">
      Runs on the worker.{' '}
      {s && <SaveStatus s={s} />}
      <ReadStatus q={list} what="where its work stands" className="small" />
      <StaleNote q={list} />
    </div>
  )
}

// ChangeWorkerChats is the change page's account of its chats on the
// worker, live and ended: where each one's work stands, and an ended chat's
// refused last save to download. A read that failed says so, before any
// empty answer is taken to mean "none".
export function ChangeWorkerChats({ changeId }: { changeId: string }) {
  const q = useGet<WorkerChatList>(`/changes/${changeId}/worker-chats`, { poll: 10000 })
  const data = q.data
  // Quiet while the first answer is on its way; a failure says so.
  if (!data) return q.err ? <ReadStatus q={q} what="this change's chats on the worker" /> : null
  const stale = <StaleNote q={q} />
  if (data.chats.length === 0) return q.refreshFailed ? stale : null
  return (
    <div style={{ margin: '10px 0' }} data-testid="worker-chats">
      {data.chats.map((c) => (
        <div key={c.chat} className={c.refused ? 'card' : undefined} style={{ margin: '6px 0' }}>
          <div className="muted small">
            {c.live ? 'A chat on the worker is working on this change.' : `A chat on the worker (${c.chat.slice(0, 8)}) worked this change and has ended.`}{' '}
            <SaveStatus s={{ saved: c.saved, recordAsOf: c.recordAsOf, state: c.live ? 'detached' : 'dead' } as ConsoleSessionView} />
          </div>
          {c.refused && (
            <div style={{ marginTop: 6 }}>
              <strong>Commits it could not save</strong>
              <div className="muted small" style={{ margin: '4px 0 6px' }}>
                They are kept here, not on the change.
                Download them as a Git bundle, fetch it into a clone (<code>git fetch the.bundle refs/apptown/refused/chat-{c.chat}</code>),
                fix what was refused, and bring the work back.
              </div>
              <div className="row">
                <a href={`/api/v1/changes/${changeId}/worker-chats/${c.chat}/refused`} download>⬇ chat {c.chat.slice(0, 8)}</a>
                <code className="muted small">{c.refused.commit.slice(0, 12)}</code>
              </div>
            </div>
          )}
        </div>
      ))}
      {stale}
    </div>
  )
}
