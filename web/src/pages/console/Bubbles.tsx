import './Bubbles.css'
import React, { useEffect, useRef, useState } from 'react'
import { post } from '../../api'
import { useNarrow } from '../../chrome'
import { useGet } from '../../data'
import { renderMd } from '../../md'
import { pastedPictures } from '../../pictures'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { AgentActionsSwitch, ToolFold, foldTools, useAgentActions } from '../../AgentActions'
export { foldTools, toolSummary } from '../../AgentActions'
import type { PastedPicture } from '../../wire.gen'

// ---- the bubble surface ----------------------------------------------------
// A projection of pi's own session record: user words right, helper words
// left, tool use as quiet chips. The terminal (one toggle away) is the same
// conversation at forensic zoom — nothing exists only here.


// The mark on AppTown's own check messages in a chat, either spelling.
const CHECK_MARK = /^\[(?:apptown|forge) check\]/
export type Bubble = { i: number; role: 'you' | 'helper'; text?: string; tools?: { name: string; detail?: string }[]; error?: boolean; stopped?: boolean }

// retired says the record holds the message just sent: any "you" message at
// or after the count the record had when it was sent. Text is never compared
// (the terminal path may normalise it).
export function retired(sent: { at: number } | null, msgs: Bubble[]): boolean {
  return !!sent && msgs.some((m, i) => m.role === 'you' && i >= sent.at)
}

// readOnly names whose chat it is when the reader may look but not act:
// the conversation shows, the ways to act on it do not.
export function Bubbles({ sessionId, onBusy, onHasMessages, footer, starters, readOnly, actionsSwitch = true }: { sessionId: string; onHasMessages?: (hasMessages: boolean) => void; actionsSwitch?: boolean; onBusy?: (b: boolean) => void; footer?: React.ReactNode; starters?: string[]; readOnly?: string }) {
  // The conversation is the record's answer; the only local message is the
  // one just sent, until the record holds it.
  const [sent, setSent] = useState<{ text: string; at: number } | null>(null)
  const [text, setText] = useState('')
  const narrow = useNarrow()
  const inputRef = useRef<HTMLTextAreaElement>(null)
  // Measure wrapping and pasted text too; the CSS cap leaves long drafts scrollable.
  useEffect(() => {
    const input = inputRef.current
    if (!narrow || !input) return
    const fit = () => { input.style.height = 'auto'; input.style.height = `${input.scrollHeight + 2}px` }
    fit()
    const observer = new ResizeObserver(fit)
    observer.observe(input)
    return () => observer.disconnect()
  }, [narrow, text])
  const [sendErr, setSendErr] = useState('')
  // Pasted pictures waiting to go with the next message: server path + a
  // local thumbnail. The file is already uploaded; send() names it.
  const [images, setImages] = useState<{ path: string; thumb: string }[]>([])
  const scrollRef = useRef<HTMLDivElement>(null)
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
  const hasMessages = msgs.length > 0 || pending !== null
  useEffect(() => { onHasMessages?.(hasMessages) }, [hasMessages, onHasMessages])
  const [stopErr, setStopErr] = useState('')

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

  const [showActions, setShowActions] = useAgentActions()
  const shown = showActions ? msgs : foldTools(msgs)
  // Whose turn it is is the server's reading of the record: the helper has
  // handed back only when its last message is words with no tool call (or
  // failed) and the record has been quiet. Read from the bubbles alone,
  // "Let me create it now:" with its command still running looked finished.
  const helperBusy = pending !== null || (msgs.length > 0 && record.data?.turn !== 'handed-back')
  useEffect(() => { onBusy?.(!!helperBusy) }, [helperBusy, onBusy])
  useEffect(() => {
    const box = scrollRef.current
    // Only the conversation moves. Scrolling a descendant into view also
    // pans Safari's page, leaving the head behind and space below the input.
    if (box && stick.current) box.scrollTop = box.scrollHeight
  }, [record.data?.messages, pending, showActions])

  return (
    <div className="chat-pane">
      {actionsSwitch && <AgentActionsSwitch shown={showActions} onChange={setShowActions} />}
      <div
        ref={scrollRef}
        onScroll={(e) => {
          const el = e.currentTarget
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80
        }}
        className="chat-scroll grow bubbles-chat-scroll">
        <ReadStatus q={record} what="the conversation" className="small" />
        <StaleNote q={record} />
        {record.data && msgs.length === 0 && !pending && (
          <div className="muted bubbles-div">
            {readOnly === undefined && starters && starters.length > 0 && (
              <div className="row bubbles-div-2">
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
                <div className="bubble bubbles-bubble">
                  <div className="small bubbles-div-3">⏹ stopped</div>
                  <div className="bubbles-div-4">{m.text}</div>
                </div>
              </div>
            )
          }
          if (m.error) {
            return (
              <div key={m.i} className="bubble-row">
                <div className="bubble error">
                  <div className="small bubbles-div-3">⚠ the helper stopped</div>
                  <div className="bubbles-div-4">{m.text}</div>
                </div>
              </div>
            )
          }
          if (isCheck(m)) {
            return (
              <div key={m.i} className="bubble-row">
                <div className="bubble check">
                  <div className="small bubbles-div-3">⚙ Boot check: the app did not start</div>
                  <div className="bubbles-div-4">{(m.text ?? '').replace(CHECK_MARK, '').trim()}</div>
                </div>
              </div>
            )
          }
          if (!m.text && m.tools?.length) return <ToolFold key={`${m.i}-${showActions}`} tools={m.tools} show={showActions} />
          return (
            <div key={m.i} className={'bubble-row' + (m.role === 'you' ? ' you' : '')}>
              <div className={'bubble' + (m.role === 'you' ? ' you' : isFinal ? ' final' : '') + (isWorking ? ' working' : '')}>
                {(m.tools?.length ?? 0) > 0 && <ToolFold key={String(showActions)} tools={m.tools!} show={showActions} />}
                {m.text && (m.role === 'helper'
                  ? <div className="md" dangerouslySetInnerHTML={{ __html: renderMd(m.text) }} />
                  : (() => {
                    // A pasted picture travels as "(I pasted a picture —
                    // look at /console/uploads/picture-N.png)"; show the
                    // picture itself, not the plumbing sentence.
                    const { names, display } = pastedPictures(m.text)
                    return (
                      <div>
                        {display && <div className="bubbles-div-4">{display}</div>}
                        {names.map((n) => (
                          <img key={n} src={`/api/v1/console/${sessionId}/uploads/${n}`} alt="pasted picture"
                            style={{ marginTop: display ? 6 : 0 }} className="bubbles-img" />
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
        {stopErr && <div className="error small">{stopErr}</div>}
      </div>
      {/* Keep this slot for the session, including pauses between tool calls.
          Visibility preserves its height without offering an idle stop button. */}
      <div className="row chat-working bubbles-chat-working" style={{ visibility: helperBusy ? 'visible' : 'hidden' }} aria-hidden={!helperBusy}>
        <span className="small robot-busy bubbles-robot-busy">Working…</span>
        {readOnly === undefined && <button className="subtle" title="Stop what it's doing right now · then tell it what you'd rather have"
          onClick={() => { setStopErr(''); post(`/console/${sessionId}/interrupt`, {}).catch((e) => setStopErr(`It could not be stopped: ${(e as Error).message}`)) }}>
          ⏹ stop
        </button>}
      </div>
      {footer}
      {sendErr && <div className="error">{sendErr}</div>}
      {images.length > 0 && (
        <div className="row mt-2 bubbles-div-5">
          {images.map((im, i) => (
            <span key={im.path} className="bubbles-span">
              <img src={im.thumb} alt="picture to send" className="bubbles-img-2" />
              <button onClick={() => setImages(images.filter((_, j) => j !== i))}
                title="don't send this picture" aria-label="don't send this picture"
                 className="bubbles-button">×</button>
            </span>
          ))}
          <span className="muted small">going with your next message</span>
        </div>
      )}
      {readOnly !== undefined ? (
        <div className="muted small chat-readonly mt-2" >
          Viewing only: this is {readOnly || 'someone else'}’s chat. Saying something to this chat needs the
          Project contributor role.
        </div>
      ) : <div className="row chat-input mt-2" >
        <textarea
          ref={inputRef}
          rows={narrow ? 2 : 1}
          style={narrow ? undefined : { height: 'auto' }}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onPaste={onPaste}
          onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() } }}
          placeholder="Say what to change. You can paste images."
          aria-label="your message"
        />
        <button className="primary" onClick={send} disabled={!text.trim() && images.length === 0}>Send</button>
      </div>}
    </div>
  )
}
