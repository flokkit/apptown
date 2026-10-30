import React, { useEffect, useRef, useState } from 'react'
import { post } from '../../api'
import { useGet } from '../../data'
import { renderMd } from '../../md'
import { pastedPictures } from '../../pictures'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { PastedPicture } from '../../wire.gen'

// ---- the bubble surface ----------------------------------------------------
// A projection of pi's own session record: user words right, helper words
// left, tool use as quiet chips. The terminal (one toggle away) is the same
// conversation at forensic zoom — nothing exists only here.


// The mark on AppTown's own check messages in a chat, either spelling.
const CHECK_MARK = /^\[(?:apptown|forge) check\]/
export type Bubble = { i: number; role: 'you' | 'helper'; text?: string; tools?: { name: string; detail?: string }[]; error?: boolean; stopped?: boolean }

const TOOL_WORDS: Record<string, string> = {
  bash: 'ran', read: 'read', edit: 'edited', write: 'wrote',
  grep: 'searched', find: 'looked for', ls: 'listed',
}

// What the helper's tool use amounts to, in words: "ran 4 commands, read
// 1 file, edited 1 file". The commands themselves are one click down.
const TOOL_COUNTS: Record<string, [string, string]> = {
  bash: ['ran', 'command'], read: ['read', 'file'], edit: ['edited', 'file'], write: ['wrote', 'file'],
  grep: ['searched', 'time'], find: ['looked for files', ''], ls: ['listed', 'folder'],
}
export function toolSummary(tools: { name: string }[]): string {
  const counts = new Map<string, number>()
  for (const t of tools) counts.set(t.name, (counts.get(t.name) ?? 0) + 1)
  return [...counts].map(([name, n]) => {
    const [verb, noun] = TOOL_COUNTS[name] ?? [`used ${name}`, 'time']
    return noun ? `${verb} ${n} ${noun}${n === 1 ? '' : 's'}` : verb
  }).join(', ')
}

// ToolFold: the helper's tool use, folded under its summary; the exact
// commands and files are inside.
function ToolFold({ tools }: { tools: { name: string; detail?: string }[] }) {
  return (
    <details className="tool-fold">
      <summary className="small">{toolSummary(tools)}</summary>
      {tools.map((t, i) => (
        <div key={i} className="small tool-line">
          {TOOL_WORDS[t.name] ?? t.name}{t.detail ? <> <code>{t.detail}</code></> : ''}
        </div>
      ))}
    </details>
  )
}

// foldTools: a run of helper messages that are only tool use becomes one
// message carrying all their tools, so a turn reads as words with its
// working folded between them.
// retired says the record holds the message just sent: any "you" message at
// or after the count the record had when it was sent. Text is never compared
// (the terminal path may normalise it).
export function retired(sent: { at: number } | null, msgs: Bubble[]): boolean {
  return !!sent && msgs.some((m, i) => m.role === 'you' && i >= sent.at)
}

export function foldTools(msgs: Bubble[]): Bubble[] {
  const out: Bubble[] = []
  for (const m of msgs) {
    const prev = out[out.length - 1]
    const toolOnly = (b?: Bubble) => !!b && b.role === 'helper' && !b.text && !b.error && !b.stopped && (b.tools?.length ?? 0) > 0
    if (toolOnly(m) && toolOnly(prev)) {
      out[out.length - 1] = { ...prev!, tools: [...prev!.tools!, ...m.tools!] }
    } else {
      out.push(m)
    }
  }
  return out
}

// readOnly names whose chat it is when the reader may look but not act:
// the conversation shows, the ways to act on it do not.
export function Bubbles({ sessionId, onBusy, footer, starters, readOnly }: { sessionId: string; onBusy?: (b: boolean) => void; footer?: React.ReactNode; starters?: string[]; readOnly?: string }) {
  // The conversation is the record's answer; the only local message is the
  // one just sent, until the record holds it.
  const [sent, setSent] = useState<{ text: string; at: number } | null>(null)
  const [text, setText] = useState('')
  const [sendErr, setSendErr] = useState('')
  // Pasted pictures waiting to go with the next message: server path + a
  // local thumbnail. The file is already uploaded; send() names it.
  const [images, setImages] = useState<{ path: string; thumb: string }[]>([])
  const bottomRef = useRef<HTMLDivElement>(null)
  const stick = useRef(true) // autoscroll unless the reader scrolled up

  const addImage = (file: File) => {
    const reader = new FileReader()
    reader.onload = async () => {
      const dataUrl = reader.result as string
      try {
        const r = await post<PastedPicture>(`/console/${sessionId}/upload`, { data: dataUrl })
        setImages((cur) => [...cur, { path: r.path, thumb: dataUrl }])
      } catch (e) { setSendErr((e as Error).message) }
    }
    reader.readAsDataURL(file)
  }
  const onPaste = (e: React.ClipboardEvent) => {
    for (const item of Array.from(e.clipboardData.items)) {
      if (item.type.startsWith('image/')) {
        const f = item.getAsFile()
        if (f) { addImage(f); e.preventDefault() }
      }
    }
  }

  const record = useGet<{ messages: Bubble[]; turn?: string }>(`/console/${sessionId}/messages?from=0`, { poll: 2000 })
  const msgs = record.data?.messages ?? []
  const pending = retired(sent, msgs) ? null : sent?.text ?? null
  const [stopErr, setStopErr] = useState('')

  useEffect(() => {
    if (stick.current) bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [msgs.length, pending])

  const send = async () => {
    let t = text.trim()
    if (!t && images.length === 0) return
    if (images.length > 0) {
      const files = images.map((im) => im.path).join(' and ')
      t = (t ? t + ' ' : '') +
        `(I pasted ${images.length === 1 ? 'a picture' : images.length + ' pictures'} — look at ${files})`
    }
    const held = images
    setText(''); setImages([]); setSendErr(''); setSent({ text: t, at: msgs.length })
    try { await post(`/console/${sessionId}/say`, { text: t }) }
    catch (e) { setSendErr((e as Error).message); setSent(null); setText(text); setImages(held) }
  }

  const shown = foldTools(msgs)
  // Whose turn it is is the server's reading of the record: the helper has
  // handed back only when its last message is words with no tool call (or
  // failed) and the record has been quiet. Read from the bubbles alone,
  // "Let me create it now:" with its command still running looked finished.
  const helperBusy = pending !== null || (msgs.length > 0 && record.data?.turn !== 'handed-back')
  useEffect(() => { onBusy?.(!!helperBusy) }, [helperBusy, onBusy])

  return (
    <div className="chat-pane">
      <div
        onScroll={(e) => {
          const el = e.currentTarget
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80
        }}
        className="chat-scroll" style={{ flex: 1, minHeight: 0, overflowY: 'auto' }}>
        <ReadStatus q={record} what="the conversation" className="small" />
        <StaleNote q={record} />
        {record.data && msgs.length === 0 && !pending && (
          <div className="muted" style={{ padding: 20, textAlign: 'center' }}>
            {readOnly === undefined && starters && starters.length > 0 && (
              <div className="row" style={{ justifyContent: 'center', gap: 8, marginTop: 14, flexWrap: 'wrap' }}>
                {starters.map((st) => (
                  <button key={st} className="subtle chat-starter"
                    onClick={() => setText(st)}>{st}</button>
                ))}
              </div>
            )}
          </div>
        )}
        {shown.map((m, idx) => {
          // Levels of detail from STRUCTURE, not model steering: within a
          // helper turn, a text message followed by more helper activity is
          // working-it-out (dim); the message where the helper stops and
          // hands back is the answer (green). Position in the record decides.
          // A [apptown check] message is AppTown itself reporting a failed
          // cold boot into the chat — it renders as AppTown, not the
          // operator, and it CANCELS the green on the claim before it.
          // Chats from before the AppTown rename keep their "[forge check]"
          // messages as written; both read as AppTown's own.
          const isCheck = (x?: Bubble) => !!x && x.role === 'you' && !!x.text && CHECK_MARK.test(x.text)
          const next = shown[idx + 1]
          const isFinal = m.role === 'helper' && !!m.text && (!next || (next.role === 'you' && !isCheck(next)))
          const isWorking = m.role === 'helper' && !isFinal && !isCheck(m)
          if (m.stopped) {
            return (
              <div key={m.i} className="bubble-row">
                <div className="bubble" style={{ opacity: 0.8 }}>
                  <div className="small" style={{ opacity: 0.8, marginBottom: 2 }}>⏹ stopped</div>
                  <div style={{ whiteSpace: 'pre-wrap' }}>{m.text}</div>
                </div>
              </div>
            )
          }
          if (m.error) {
            return (
              <div key={m.i} className="bubble-row">
                <div className="bubble error">
                  <div className="small" style={{ opacity: 0.8, marginBottom: 2 }}>⚠ the helper stopped</div>
                  <div style={{ whiteSpace: 'pre-wrap' }}>{m.text}</div>
                </div>
              </div>
            )
          }
          if (isCheck(m)) {
            return (
              <div key={m.i} className="bubble-row">
                <div className="bubble check">
                  <div className="small" style={{ opacity: 0.8, marginBottom: 2 }}>⚙ Boot check: the app did not start</div>
                  <div style={{ whiteSpace: 'pre-wrap' }}>{(m.text ?? '').replace(CHECK_MARK, '').trim()}</div>
                </div>
              </div>
            )
          }
          return (
            <div key={m.i} className={'bubble-row' + (m.role === 'you' ? ' you' : '')}>
              <div className={'bubble' + (m.role === 'you' ? ' you' : isFinal ? ' final' : '') + (isWorking ? ' working' : '')}>
                {(m.tools?.length ?? 0) > 0 && <ToolFold tools={m.tools!} />}
                {m.text && (m.role === 'helper'
                  ? <div className="md" dangerouslySetInnerHTML={{ __html: renderMd(m.text) }} />
                  : (() => {
                    // A pasted picture travels as "(I pasted a picture —
                    // look at /console/uploads/picture-N.png)"; show the
                    // picture itself, not the plumbing sentence.
                    const { names, display } = pastedPictures(m.text)
                    return (
                      <div>
                        {display && <div style={{ whiteSpace: 'pre-wrap' }}>{display}</div>}
                        {names.map((n) => (
                          <img key={n} src={`/api/v1/console/${sessionId}/uploads/${n}`} alt="pasted picture"
                            style={{ maxWidth: 260, maxHeight: 200, borderRadius: 8, marginTop: display ? 6 : 0, display: 'block' }} />
                        ))}
                      </div>
                    )
                  })())}
              </div>
            </div>
          )
        })}
        {pending && (
          <div className="bubble-row you">
            <div className="bubble you pending">{pending}</div>
          </div>
        )}
        {helperBusy && (
          <div className="row" style={{ margin: '6px 0', gap: 10 }}>
            <span className="small robot-busy" style={{ border: 'none', padding: 0 }}>Working…</span>
            {readOnly === undefined && <button className="subtle" title="Stop what it's doing right now · then tell it what you'd rather have"
              onClick={() => { setStopErr(''); post(`/console/${sessionId}/interrupt`, {}).catch((e) => setStopErr(`It could not be stopped: ${(e as Error).message}`)) }}>
              ⏹ stop
            </button>}
          </div>
        )}
        {stopErr && <div className="error small">{stopErr}</div>}
        <div ref={bottomRef} />
      </div>
      {footer}
      {sendErr && <div className="error">{sendErr}</div>}
      {images.length > 0 && (
        <div className="row" style={{ marginTop: 8, gap: 8, flexWrap: 'wrap' }}>
          {images.map((im, i) => (
            <span key={im.path} style={{ position: 'relative', display: 'inline-block' }}>
              <img src={im.thumb} alt="picture to send" style={{ height: 64, borderRadius: 8, display: 'block' }} />
              <button onClick={() => setImages(images.filter((_, j) => j !== i))}
                title="don't send this picture" aria-label="don't send this picture"
                style={{
                  position: 'absolute', top: -6, right: -6, width: 20, height: 20, lineHeight: '16px',
                  borderRadius: 10, padding: 0, fontSize: 'var(--fs-xs)', cursor: 'pointer',
                }}>×</button>
            </span>
          ))}
          <span className="muted small">going with your next message</span>
        </div>
      )}
      {readOnly !== undefined ? (
        <div className="muted small chat-readonly" style={{ marginTop: 8 }}>
          Viewing only: this is {readOnly || 'someone else'}’s chat. Saying something to this chat needs the
          Project contributor role.
        </div>
      ) : <div className="row chat-input" style={{ marginTop: 8 }}>
        <textarea
          rows={1}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onPaste={onPaste}
          onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() } }}
          placeholder="Say what you want (you can paste images)"
          aria-label="your message"
        />
        <button className="primary" onClick={send} disabled={!text.trim() && images.length === 0}>Send</button>
      </div>}
    </div>
  )
}
