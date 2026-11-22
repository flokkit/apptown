import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { post } from '../../api'
import { useFollow } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { useAgentActions } from '../../AgentActions'
import { Activity } from './Activity'
import type { ChatUpload, ConsoleStreamUpdate, Message } from '../../wire.gen'
import { Composer, type DraftFile } from './Composer'
import { StepsColumn, StepsSheet, Thread, useStepsColumn } from './Thread'
import { toThread, type RunPart } from './threadModel'
import { useStickToEnd } from './useStickToEnd'

// ---- the conversation ------------------------------------------------------
// A projection of pi's own session record, drawn as a thread (Thread.tsx):
// your words right, the helper's left, each run of steps one pill. The
// terminal (one toggle away) is the same conversation at forensic zoom —
// nothing exists only here.

export type Bubble = Message & { role: 'you' | 'helper' }

// The chat as its stream has told it: the record's messages, the message
// the helper is writing (its draft, until the record holds it), and whose
// turn it is. The cursor is what the reader holds, to resume from.
export type ChatRecord = { messages: Message[]; draft: Message | null; turn: string; cursor: string }

// foldChat applies one update of the chat's stream: the messages from
// `from` on replace what is held from there (0 replaces everything); the
// messages before it keep their identity, so their turns are not drawn
// again.
export function foldChat(held: ChatRecord | null, u: ConsoleStreamUpdate): ChatRecord {
  const kept = (held?.messages ?? []).slice(0, u.from)
  return { messages: kept.concat(u.messages), draft: u.draft ?? null, turn: u.turn, cursor: u.cursor }
}
const chatCursor = (held: ChatRecord) => held.cursor
const NO_MESSAGES: Message[] = []

// retired says the record holds the message just sent: any "you" message at
// or after the count the record had when it was sent. Text is never compared
// (the terminal path may normalise it).
export function retired(sent: { at: number } | null, msgs: Message[]): boolean {
  return !!sent && msgs.some((m, i) => m.role === 'you' && i >= sent.at)
}

// What the bar says about the helper: working, your turn, or that the
// conversation's read is not current (then nobody can say whose turn it is).
export type ChatActivity = { busy: boolean; read: 'loading' | 'current' | 'stale' }

// readOnly names whose chat it is when the reader may look but not act:
// the conversation shows, the ways to act on it do not.
// notices sit at the top of the conversation and scroll away with it;
// dockTop sits over the input (the check-and-land confirmation).
export function Bubbles({ sessionId, attached = true, starting, hidden = false, onActivity, starters, readOnly, before, notices, dockTop, columnFacts, noColumn = false }: {
  noColumn?: boolean
  sessionId: string | null; attached?: boolean; starting?: React.ReactNode; hidden?: boolean
  onActivity?: (a: ChatActivity) => void; starters?: string[]; readOnly?: string
  before?: React.ReactNode; notices?: React.ReactNode; dockTop?: React.ReactNode; columnFacts?: React.ReactNode
}) {
  // The conversation is the record's answer; the only local message is the
  // queued message and the one just sent, until the record holds it.
  const [sent, setSent] = useState<{ text: string; at: number } | null>(null)
  const [text, setText] = useState('')
  const [queued, setQueued] = useState('')
  const queuedRef = useRef('')
  const previousSession = useRef(sessionId)
  const [sendErr, setSendErr] = useState('')
  const [stopErr, setStopErr] = useState('')
  // Pasted pictures waiting to go with the next message: server path + a
  // local thumbnail. Before a session exists, delivery uploads the picture.
  const [images, setImages] = useState<DraftFile[]>([])
  const queuedImages = useRef<DraftFile[]>([])
  // Which run's steps are shown (the sheet, or the column's mark): the
  // displayed chat's, so they reset with it.
  const [picked, setPicked] = useState<{ key: string; sheet: boolean } | null>(null)
  useEffect(() => {
    if (previousSession.current && previousSession.current !== sessionId) {
      queuedRef.current = ''; queuedImages.current = []; setQueued(''); setText(''); setSent(null); setImages([]); setSendErr(''); setPicked(null)
    }
    previousSession.current = sessionId
  }, [sessionId])

  const previewURLs = useRef<string[]>([])
  useEffect(() => () => { previewURLs.current.forEach((url) => URL.revokeObjectURL(url)) }, [])
  const addFiles = (files: File[]) => {
    if (images.length + queuedImages.current.length + files.length > 10) { setSendErr('Too many files in one message — the cap is 10.'); return }
    if (files.some((f) => f.size > 25 * 1024 * 1024)) { setSendErr('File too big — 25 MB is the cap.'); return }
    if (files.some((f) => f.size === 0)) { setSendErr('The file is empty. Choose a file with something in it.'); return }
    setSendErr('')
    const added = files.map((file) => {
      const thumb = /^image\/(png|jpeg|webp|gif)$/.test(file.type) ? URL.createObjectURL(file) : ''
      if (thumb) previewURLs.current.push(thumb)
      return { file, name: file.name, path: '', thumb }
    })
    setImages((cur) => [...cur, ...added])
  }

  // The chat streams: words as the helper writes them, the record's
  // messages as pi records them (console.md, "Following a chat").
  const record = useFollow<ChatRecord, ConsoleStreamUpdate>(sessionId ? `/console/${sessionId}/stream` : null, foldChat, chatCursor)
  const msgs = record.data?.messages ?? NO_MESSAGES
  const draft = record.data?.draft ?? null
  const pending = retired(sent, msgs) ? null : sent?.text ?? null

  // /say waits for pi's input readiness, just as it does for kickoff.
  // Consume the queue before starting the request: effect replays and terminal
  // reconnects must never submit the same words twice.
  const deliver = useCallback(async (t: string, pictures: DraftFile[]) => {
    if (!sessionId) return
    setSendErr(''); setSent({ text: t || '(File)', at: msgs.length })
    try {
      const names: string[] = []
      for (const file of pictures) {
        if (!file.path) {
          const body = new FormData()
          body.append('file', file.file)
          const uploaded = await post<ChatUpload>(`/console/${sessionId}/upload`, body)
          file.path = uploaded.path; file.name = uploaded.name
        }
        names.push(file.name)
      }
      await post(`/console/${sessionId}/say`, { text: t, attachments: names })
    } catch (e) {
      if (previousSession.current !== sessionId) return
      setSendErr((e as Error).message); setSent(null)
      setText((draft) => [t, draft].filter(Boolean).join('\n\n')); setImages((draft) => [...pictures, ...draft])
    }
  }, [sessionId, msgs.length])
  useEffect(() => {
    if (!sessionId || !attached || readOnly !== undefined || (!queuedRef.current && !queuedImages.current.length)) return
    const message = queuedRef.current
    const pictures = queuedImages.current
    queuedRef.current = ''; queuedImages.current = []; setQueued('')
    void deliver(message, pictures)
  }, [sessionId, attached, readOnly, deliver, queued])
  const send = () => {
    const t = text.trim()
    if (!t && !images.length) return
    if (images.length + queuedImages.current.length > 10) { setSendErr('Too many files in one message — the cap is 10.'); return }
    setText(''); setImages([])
    if (!sessionId || !attached) {
      queuedRef.current = [queuedRef.current, t].filter(Boolean).join('\n\n')
      queuedImages.current.push(...images)
      setQueued(queuedRef.current || '(File)')
    } else void deliver(t, images)
  }
  const stop = () => {
    setStopErr('')
    post(`/console/${sessionId}/interrupt`, {}).catch((e) => setStopErr(`It could not be stopped: ${(e as Error).message}`))
  }

  const [showActions] = useAgentActions()
  // Whose turn it is is the server's reading of the record: the helper has
  // handed back only when pi said its turn settled. Read from the messages
  // alone, "Let me create it now:" with its command still running looked
  // finished.
  const handedBack = record.data?.turn === 'handed-back'
  const turns = useMemo(() => toThread(msgs, draft, handedBack), [msgs, draft, handedBack])
  const helperBusy = pending !== null || (msgs.length > 0 && !handedBack)
  const read = record.data ? (record.refreshFailed ? 'stale' : 'current') : record.err ? 'stale' : 'loading'
  useEffect(() => { onActivity?.({ busy: helperBusy, read }) }, [helperBusy, read, onActivity])

  const pictures = useCallback((name: string) => `/api/v1/console/${sessionId}/uploads/${name}`, [sessionId])
  const column = useStepsColumn()
  // The column opens only where there is one: on a phone, or while the
  // preview stands beside the chat, the run opens as a sheet.
  const columnNow = useRef({ ...column, room: column.room && !noColumn })
  columnNow.current = { ...column, room: column.room && !noColumn }
  const onPill = useCallback((run: RunPart) => {
    if (columnNow.current.room) { columnNow.current.open(); setPicked({ key: run.key, sheet: false }) }
    else setPicked({ key: run.key, sheet: true })
  }, [])
  // The sheet follows its run as it grows: it is looked up in the newest turns.
  const sheetTurn = picked?.sheet ? turns.find((t) => t.parts.some((p) => p.key === picked.key)) : undefined
  const sheetRun = sheetTurn?.parts.find((p): p is RunPart => p.key === picked?.key && p.kind === 'run')

  const closeSheet = useCallback(() => setPicked(null), [])
  const scroller = useRef<HTMLDivElement>(null)
  const content = useRef<HTMLDivElement>(null)
  const { cue, toEnd } = useStickToEnd(scroller, content, !hidden)

  const empty = !!record.data && msgs.length === 0 && !draft && !pending && !queued
  return (
    <div className="chat-body" hidden={hidden}>
      <div className="chat-pane">
        <div className="chat-scroll-wrap">
        <div ref={scroller} className="chat-scroll">
          <div ref={content} className="chat-feed">
            {before}
            {notices}
            {starting}
            {(!starting || !record.loading) && <ReadStatus q={record} what="the conversation" className="small" />}
            {empty && readOnly === undefined && starters && starters.length > 0 && <div className="starters">
              {starters.map((st) => <button key={st} type="button" onClick={() => setText(st)}>{st}</button>)}
            </div>}
            <Thread turns={turns} pictures={pictures} showActions={showActions} onPill={onPill}
              selectedRun={column.shown && picked && !picked.sheet ? picked.key : undefined} />
            {queued && <section className="turn"><div className="ask pending"><div className="ask-t">{queued}</div>
              <div className="queued">will send when your helper is ready</div></div></section>}
            {pending && <section className="turn"><div className="ask pending"><div className="ask-t">{pending}</div></div></section>}
          </div>
        </div>
        {cue && <button type="button" className="chat-cue" onClick={toEnd}>
          {cue === 'new' ? 'New messages below' : 'Newest'}
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M6 13l6 6 6-6" /></svg>
        </button>}
        </div>
        <div className="chat-dock">
          <StaleNote q={record} className="chat-stale" />
          {stopErr && <div className="error small chat-err">{stopErr}</div>}
          {sendErr && <div className="error small chat-err">{sendErr}</div>}
          {readOnly !== undefined
            ? <div className="chat-readonly">
              {helperBusy && !starting && <Activity word="working" />}
              <span>Viewing only: this is {readOnly || 'someone else'}’s chat. Saying something to this chat needs the Project contributor role.</span>
            </div>
            : <Composer text={text} onText={setText} files={images} onAddFiles={addFiles}
              onRemoveFile={(i) => setImages(images.filter((_, j) => j !== i))}
              onSend={send} onStop={sessionId ? stop : undefined} busy={helperBusy && !starting} top={dockTop} />}
        </div>
      </div>
      {column.shown && !noColumn && <StepsColumn turns={turns} selected={picked && !picked.sheet ? picked.key : undefined} facts={columnFacts}
        onClose={() => { column.close(); setPicked(null) }} />}
      {sheetRun && sheetTurn && <StepsSheet run={sheetRun} turn={sheetTurn} onClose={closeSheet} />}
    </div>
  )
}
