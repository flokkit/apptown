import { ListRow } from '../../ListRow'
import './list-records.css'
import { firstLine } from './Head'
import './Conversation.css'
import { AgentActionsSwitch, ToolFold, foldTools, useAgentActions } from '../../AgentActions'
import { Section } from '../../Section'
import { fmtWhen } from '../../format'
import { PRODUCT } from '../../brand'
import { Link, useParams } from 'react-router-dom'
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
        <div className="bubble-row you"><div className="bubble you conversation-conversation-log-bubble-you">{firstLine(request)} <Link to={`/changes/${changeId}/records/request`}>full request →</Link></div></div>
        {report ? <div className="bubble-row"><div className="bubble"><div className="md" dangerouslySetInnerHTML={{ __html: renderMd(report) }} /></div></div>
          : rounds.length > 0 && <p className="muted">{rounds.length === 1 ? 'The agent left no messages in this round.' : 'The agent left no messages.'}</p>}
      </> : <p className="muted">No chat yet.</p>)}
      {earlierRuns.map(id => <RunRounds key={id} id={id} changeId={changeId} />)}
      {rounds.map((e, i) => <ListRow to={workRecordHref(changeId, e)} className="conversation-record-row" key={e.key} identity={<Link to={workRecordHref(changeId, e)}>Agent round {i + 1} · work record</Link>} />)}
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
  const d: RunDetail | undefined = t?.run ? { checks: t.checks, run: t.run, steps: t.steps.filter(Boolean) as RunDetail['steps'], events: t.events as RunDetail['events'], artifacts: t.artifacts, stages: t.stages, next: [], landingMeans: [] } : undefined
  return <><ReadStatus q={q} what="agent rounds" /><StaleNote q={q} />{d && runStory(d).filter(e => e.step?.type === 'agent/pi' || e.key.startsWith('work-')).map(e => <ListRow to={workRecordHref(changeId, e)} className="conversation-record-row" key={e.key} identity={<Link to={workRecordHref(changeId, e)}>Run {d.run.number} · {e.step?.name || 'work'} · {fmtWhen(e.at)} · work record</Link>} />)}</>
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
        <ToolFold key={`${m.i}-${showActions}`} tools={m.tools} show={showActions} /> : (
        <div key={m.i} className={'bubble-row' + (m.role === 'you' ? ' you' : '')}>
          <div className={'bubble' + (m.role === 'you' ? ' you' : '')}>
            {!!m.tools?.length && <ToolFold key={String(showActions)} tools={m.tools} show={showActions} />}
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
// available on the project's past-chats page.
export function PastChats({ slug, section: _section = false }: { slug: string; section?: boolean }) {
  return <p><Link to={`/projects/${slug}/chats`}>Past chats</Link></p>
}

export function PastChatsPage() {
  const { slug = '', sessionId } = useParams()
  const q = useGet<ChatHistoryView>(`/projects/${slug}/transcript`)
  const sessions = q.data?.sessions ?? []
  const selected = sessions.find(s => s.id === sessionId)
  return <><h1>{sessionId ? selected ? chatOpening(selected) : 'Chat' : 'Past chats'}</h1>
    <ReadStatus q={q} what="the past chats" /><StaleNote q={q} />
    {q.data && <UnreadableNote view={q.data} />}
    {q.data && (sessionId ? selected ? <>
      <p className="muted small">{chatByline(selected)}</p>
      <ChatMessages chat={selected} pictures={`/api/v1/projects/${slug}/transcript/${selected.id}/pictures/`} />
    </> : <p>This chat is not recorded.</p> : <Section title="Chats" count={sessions.length}>
      {sessions.map(se => <ListRow to={`/projects/${slug}/chats/${se.id}`} className="conversation-record-row" key={se.id} identity={<><Link to={`/projects/${slug}/chats/${se.id}`}>{chatOpening(se)}</Link> · {chatByline(se)}</>} />)}
      {sessions.length === 0 && !q.data.unreadable && <p>No ended chats yet. Chats from <Link to={`/projects/${slug}/console`}>Project chat</Link> are kept here when they end.</p>}
    </Section>)}
  </>
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
