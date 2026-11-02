import { Section } from '../../Section'
import { fmtWhen } from '../../format'
import { PRODUCT } from '../../brand'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { renderMd } from '../../md'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { ChatHistoryEntry, ChatHistoryView } from '../../wire.gen'
import { pastedPictures } from '../../pictures'

// The change's conversations: what was said to the helper and what it said
// back, across every chat session this change has had. Sessions end; the
// words are part of the change's story and read back from the record.
export function ConversationLog({ changeId, chatHref }: { changeId: string; chatHref?: string }) {
  // Read when the tab opens; the tab is where a person asks for it.
  const q = useGet<ChatHistoryView>(`/changes/${changeId}/transcript`)
  const data = q.data
  const sessions = data ? data.sessions ?? [] : null
  const none = sessions?.length === 0 && !data?.unreadable
  // chatHref: where this change's chat opens, given only where a chat can
  // start on it.
  const action = chatHref ? <Link to={chatHref}>{none ? 'Start a chat' : 'Open the chat'}</Link> : undefined
  return (
    <Section title="Conversation" count={sessions?.length || undefined} action={action}>
      <ReadStatus q={q} what="the conversation" className="small" />
      <StaleNote q={q} />
      {data && <UnreadableNote view={data} />}
      {none && <p className="muted">No chat yet.</p>}
      {sessions?.map((se) => (
        <div key={se.id} style={{ margin: '12px 0' }}>
          <div className="muted small" style={{ margin: '6px 0' }}>{chatByline(se)}</div>
          <ChatMessages chat={se} pictures={`/api/v1/changes/${changeId}/transcript/${se.id}/pictures/`} />
        </div>
      ))}
    </Section>
  )
}

// Who opened a chat and when; a chat moved from another AppTown says so.
export function chatByline(se: ChatHistoryEntry): string {
  return `opened ${fmtWhen(se.createdAt)} by ${se.actor}` +
    (se.imported ? ` · moved from another ${PRODUCT}, history only` : '')
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
export function PastChats({ slug, section = false }: { slug: string; section?: boolean }) {
  const [open, setOpen] = useState(false)
  const q = useGet<ChatHistoryView>(open ? `/projects/${slug}/transcript` : null)
  const data = q.data
  const sessions = data ? data.sessions ?? [] : null
  const body = <>
      {open && <ReadStatus q={q} what="the past chats" className="small" />}
      {open && <StaleNote q={q} />}
      {data && <UnreadableNote view={data} />}
      {sessions?.length === 0 && !data?.unreadable && <div className="muted small" style={{ margin: '12px 0' }}>No ended chats on this project.</div>}
      {sessions?.map((se) => (
        <details key={se.id} style={{ margin: '8px 0 0 12px' }}>
          <summary><span>{chatOpening(se)}<span className="muted small" style={{ display: 'block' }}>{chatByline(se)}</span></span></summary>
          <div style={{ paddingLeft: 12 }}><ChatMessages chat={se} pictures={`/api/v1/projects/${slug}/transcript/${se.id}/pictures/`} /></div>
        </details>
      ))}
  </>
  // As a page section the chats load only when asked for, like the fold did.
  if (section) {
    return (
      <Section title="Past chats" note={open ? undefined : 'Chats that have ended, with what was said.'} action={<button className="link" aria-expanded={open} onClick={() => setOpen(o => !o)}>{open ? 'Hide' : 'Show'}</button>}>
        {open ? body : null}
      </Section>
    )
  }
  return (
    <details style={{ marginTop: 18 }} onToggle={(e) => setOpen((e.target as HTMLDetailsElement).open)}>
      {/* One child: the fold's summary is a flex row, which would split the
          words into columns on a phone. */}
      <summary><span><strong>Past chats</strong></span></summary>
      {body}
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
            style={{ display: 'block', maxWidth: '100%', width: 320, maxHeight: 240, objectFit: 'contain', borderRadius: 'var(--radius-sm)', marginTop: display ? 6 : 0 }} />
        </a>
      ))}
    </div>
  )
}

// A chat record on this AppTown that could not be read may belong here: say
// so plainly, never show the list as complete. Administrators see which.
export function UnreadableNote({ view }: { view: ChatHistoryView }) {
  if (!view.unreadable) return null
  const n = view.unreadable
  return (
    <div className="small" style={{ margin: '6px 0', color: 'var(--warn, var(--muted))' }}>
      {n} chat record{n === 1 ? '' : 's'} in {PRODUCT} could not be read; some chats may be missing here.
      {(view.unreadableFolders?.length ?? 0) > 0 && <> Folders: {view.unreadableFolders!.join(', ')}.</>}
    </div>
  )
}
