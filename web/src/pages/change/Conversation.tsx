import './Conversation.css'
import { AgentActionsSwitch, ToolFold, foldTools, useAgentActions } from '../../AgentActions'
import { Section } from '../../Section'
import { fmtWhen } from '../../format'
import { PRODUCT } from '../../brand'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { renderMd } from '../../md'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { ChatHistoryEntry, ChatHistoryView, RunTrace } from '../../wire.gen'
import type { StoryEntry } from './story'
import type { RunDetail } from '../../api'
import { runStory } from './story'
import { workRecordHref } from './WorkRecord'
import { pastedPictures } from '../../pictures'

// The change's conversations: what was said to the helper and what it said
// back, across every chat session this change has had. Sessions end; the
// words are part of the change's story and read back from the record.
export function ConversationLog({ changeId, chatHref, request, report, rounds = [], earlierRuns = [] }: { earlierRuns?: string[]; rounds?: StoryEntry[]; changeId: string; chatHref?: string; request?: string; report?: string }) {
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
      {none && (request ? <>
        <div className="bubble-row you"><div className="bubble you conversation-conversation-log-bubble-you">{request}</div></div>
        {report && <div className="bubble-row"><div className="bubble"><div className="md" dangerouslySetInnerHTML={{ __html: renderMd(report, true) }} /></div></div>}
      </> : <p className="muted">No chat yet.</p>)}
      {earlierRuns.map(id => <RunRounds key={id} id={id} changeId={changeId} />)}
      {rounds.map((e, i) => <p key={e.key}><Link to={workRecordHref(changeId, e)}>Agent round {i + 1} · work record</Link></p>)}
      {sessions?.map((se) => (
        <div key={se.id} className="conversation-div">
          <div className="muted small conversation-div-2">{chatByline(se)}</div>
          <ChatMessages chat={se} pictures={`/api/v1/changes/${changeId}/transcript/${se.id}/pictures/`} />
        </div>
      ))}
    </Section>
  )
}


function RunRounds({ id, changeId }: { id: string; changeId: string }) {
  const q = useGet<RunTrace>(`/runs/${id}/trace`)
  const t = q.data
  const d: RunDetail | undefined = t?.run ? { run: t.run, steps: t.steps.filter(Boolean) as RunDetail['steps'], events: t.events as RunDetail['events'], artifacts: t.artifacts, stages: t.stages, next: [], landingMeans: [] } : undefined
  return <><ReadStatus q={q} what="agent rounds" /><StaleNote q={q} />{d && runStory(d).filter(e => e.step?.type === 'agent/pi' || e.key.startsWith('work-')).map(e => <p key={e.key}><Link to={workRecordHref(changeId, e)}>Run {d.run.number} · {e.step?.name || 'work'} · {fmtWhen(e.at)} · work record</Link></p>)}</>
}

// Who opened a chat and when; a chat moved from another AppTown says so.
export function chatByline(se: ChatHistoryEntry): string {
  return `opened ${fmtWhen(se.createdAt)} by ${se.actor}` +
    (se.imported ? ` · moved from another ${PRODUCT}, history only` : '')
}

// A chat's opening words, for a list of chats.
export function chatOpening(se: ChatHistoryEntry): string {
  const first = se.messages.find((m) => m.role === 'you' && m.text)?.text ?? se.messages[0]?.text ?? ''
  const { display, names, files } = pastedPictures(first)
  const line = display.split('\n')[0] || (files[0]?.name ?? (names.length > 0 ? '(a pasted picture)' : ''))
  return line.length > 90 ? line.slice(0, 89) + '…' : line || '(no words)'
}

// ChatMessages shows a chat's words and its pasted pictures, read from
// pictures + the picture's own name; a picture opens full size on its own.
export function ChatMessages({ chat, pictures }: { chat: ChatHistoryEntry; pictures: string }) {
  const [showActions, setShowActions] = useAgentActions()
  const messages = showActions ? chat.messages : foldTools(chat.messages)
  return (
    <>
      <AgentActionsSwitch shown={showActions} onChange={setShowActions} />
      {messages.map((m) => !m.text && m.tools?.length ?
        <ToolFold inline key={`${m.i}-${showActions}`} tools={m.tools} show={showActions} /> : (
        <div key={m.i} className={'bubble-row' + (m.role === 'you' ? ' you' : '')}>
          <div className={'bubble' + (m.role === 'you' ? ' you' : '')}>
            {!!m.tools?.length && <ToolFold inline key={String(showActions)} tools={m.tools} show={showActions} />}
            {m.text && (m.role === 'helper'
              ? <div className="md" dangerouslySetInnerHTML={{ __html: renderMd(m.text, true) }} />
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
      {sessions?.length === 0 && !data?.unreadable && <div className="muted small conversation-div">No ended chats on this project.</div>}
      {sessions?.map((se) => (
        <details key={se.id} className="conversation-details">
          <summary><span>{chatOpening(se)}<span className="muted small conversation-span">{chatByline(se)}</span></span></summary>
          <div className="conversation-div-3"><ChatMessages chat={se} pictures={`/api/v1/projects/${slug}/transcript/${se.id}/pictures/`} /></div>
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
    <details  onToggle={(e) => setOpen((e.target as HTMLDetailsElement).open)} className="conversation-past-chats-details">
      {/* One child: the fold's summary is a flex row, which would split the
          words into columns on a phone. */}
      <summary><span><strong>Past chats</strong></span></summary>
      {body}
    </details>
  )
}

function YourWords({ text, pictures }: { text: string; pictures: string }) {
  const { display, names, files } = pastedPictures(text)
  return (
    <div>
      {display && <div className="conversation-conversation-log-bubble-you">{display}</div>}
      {files.map((f) => <div key={f.name}><a href={pictures + f.name} download>{f.name}</a> ({f.size} bytes)</div>)}
      {names.map((n) => (
        <a key={n} href={pictures + n} target="_blank" rel="noreferrer" title="open the picture full size">
          <img src={pictures + n} alt="pasted picture" loading="lazy"
            style={{ marginTop: display ? 6 : 0 }} className="conversation-img" />
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
    <div className="small conversation-unreadable-note-div">
      {n} chat record{n === 1 ? '' : 's'} in {PRODUCT} could not be read; some chats may be missing here.
      {(view.unreadableFolders?.length ?? 0) > 0 && <> Folders: {view.unreadableFolders!.join(', ')}.</>}
    </div>
  )
}
