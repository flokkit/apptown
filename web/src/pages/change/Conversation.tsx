import { useState } from 'react'
import { renderMd } from '../../md'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { ChatHistoryEntry, ChatHistoryView } from '../../wire.gen'
import { pastedPictures } from '../../pictures'

// The change's conversations: what was said to the helper and what it said
// back, across every chat session this change has had. Sessions end; the
// words are part of the change's story and read back from the record.
export function ConversationLog({ changeId }: { changeId: string }) {
  // Read when the tab opens; the tab is where a person asks for it.
  const q = useGet<ChatHistoryView>(`/changes/${changeId}/transcript`)
  const data = q.data
  const sessions = data ? data.sessions ?? [] : null
  return (
    <div>
      <ReadStatus q={q} what="the conversation" className="small" />
      <StaleNote q={q} />
      {data && <UnreadableNote view={data} />}
      {sessions?.length === 0 && !data?.unreadable && <div className="muted small">No chat on this change.</div>}
      {sessions?.map((se) => (
        <div key={se.id} style={{ marginBottom: 14 }}>
          <div className="muted small" style={{ margin: '6px 0' }}>{chatByline(se)}</div>
          <ChatMessages chat={se} pictures={`/api/v1/changes/${changeId}/transcript/${se.id}/pictures/`} />
        </div>
      ))}
    </div>
  )
}

// Who opened a chat and when; a chat moved from another forge says so.
export function chatByline(se: ChatHistoryEntry): string {
  return `opened ${se.createdAt.slice(0, 16).replace('T', ' ')} by ${se.actor}` +
    (se.imported ? ' · moved from another forge, history only' : '')
}

// A chat's opening words, for a list of chats.
export function chatOpening(se: ChatHistoryEntry): string {
  const first = se.messages.find((m) => m.role === 'you' && m.text)?.text ?? se.messages[0]?.text ?? ''
  const { display, names } = pastedPictures(first)
  const line = display.split('\n')[0] || (names.length > 0 ? '(a pasted picture)' : '')
  return line.length > 90 ? line.slice(0, 89) + '…' : line || '(no words)'
}

// ChatMessages shows a chat's words and its pasted pictures, read from
// pictures + the picture's own name; a picture opens full size on its own.
export function ChatMessages({ chat, pictures }: { chat: ChatHistoryEntry; pictures: string }) {
  return (
    <>
      {chat.messages.map((m) => (
        <div key={m.i} className={'bubble-row' + (m.role === 'you' ? ' you' : '')}>
          <div className={'bubble' + (m.role === 'you' ? ' you' : '')}>
            {(m.tools ?? []).map((t, i) => (
              <div key={i} className="small" style={{ opacity: 0.7 }}>🔧 {t.name}{t.detail ? <> <code style={{ fontSize: 'var(--fs-xs)' }}>{t.detail}</code></> : ''}</div>
            ))}
            {m.text && (m.role === 'helper'
              ? <div className="md" dangerouslySetInnerHTML={{ __html: renderMd(m.text) }} />
              : <YourWords text={m.text} pictures={pictures} />)}
          </div>
        </div>
      ))}
    </>
  )
}

// The ended chats opened on the project itself rather than on a change,
// folded: read only when someone opens the fold.
export function PastChats({ slug }: { slug: string }) {
  const [open, setOpen] = useState(false)
  const q = useGet<ChatHistoryView>(open ? `/projects/${slug}/transcript` : null)
  const data = q.data
  const sessions = data ? data.sessions ?? [] : null
  return (
    <details style={{ marginTop: 18 }} onToggle={(e) => setOpen((e.target as HTMLDetailsElement).open)}>
      {/* One child: the fold's summary is a flex row, which would split the
          words into columns on a phone. */}
      <summary><span><strong>Past chats</strong> <span className="muted small">ended chats opened on the project; a change's chats are on its page</span></span></summary>
      {open && <ReadStatus q={q} what="the past chats" className="small" />}
      {open && <StaleNote q={q} />}
      {data && <UnreadableNote view={data} />}
      {sessions?.length === 0 && !data?.unreadable && <div className="muted small">No ended chats on this project.</div>}
      {sessions?.map((se) => (
        <details key={se.id} style={{ margin: '8px 0 0 12px' }}>
          <summary><span>{chatOpening(se)}<span className="muted small" style={{ display: 'block' }}>{chatByline(se)}</span></span></summary>
          <div style={{ paddingLeft: 12 }}><ChatMessages chat={se} pictures={`/api/v1/projects/${slug}/transcript/${se.id}/pictures/`} /></div>
        </details>
      ))}
    </details>
  )
}

function YourWords({ text, pictures }: { text: string; pictures: string }) {
  const { display, names } = pastedPictures(text)
  return (
    <div>
      {display && <div style={{ whiteSpace: 'pre-wrap' }}>{display}</div>}
      {names.map((n) => (
        <a key={n} href={pictures + n} target="_blank" rel="noreferrer" title="open the picture full size">
          <img src={pictures + n} alt="pasted picture" loading="lazy"
            style={{ display: 'block', maxWidth: '100%', width: 320, maxHeight: 240, objectFit: 'contain', borderRadius: 8, marginTop: display ? 6 : 0 }} />
        </a>
      ))}
    </div>
  )
}

// A chat record on this forge that could not be read may belong here: say
// so plainly, never show the list as complete. Administrators see which.
export function UnreadableNote({ view }: { view: ChatHistoryView }) {
  if (!view.unreadable) return null
  const n = view.unreadable
  return (
    <div className="small" style={{ margin: '6px 0', color: 'var(--warn, var(--muted))' }}>
      {n} chat record{n === 1 ? '' : 's'} on this forge could not be read; some chats may be missing here.
      {(view.unreadableFolders?.length ?? 0) > 0 && <> Folders: {view.unreadableFolders!.join(', ')}.</>}
    </div>
  )
}
