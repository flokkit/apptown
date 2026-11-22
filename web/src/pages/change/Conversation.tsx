import { PageBar } from '../../PageBar'
import { ListRow } from '../../ListRow'
import './list-records.css'
import { firstLine } from './Head'
import './Conversation.css'
import { AgentActionsSwitch, useAgentActions } from '../../AgentActions'
import { useCallback, useMemo, useState } from 'react'
import { StepsSheet, Thread } from '../console/Thread'
import { toThread } from '../console/threadModel'
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
export function ConversationLog({ changeId, live = false, selectedChat, request, report, rounds = [], earlierRuns = [] }: { earlierRuns?: string[]; rounds?: StoryEntry[]; changeId: string; live?: boolean; selectedChat?: string; request?: string; report?: string }) {
  // Read when the tab opens; the tab is where a person asks for it.
  const q = useGet<ChatHistoryView>(`/changes/${changeId}/transcript`, { poll: 4000 })
  const data = q.data
  const sessions = data ? data.sessions ?? [] : null
  const none = sessions?.length === 0 && !data?.unreadable
  const selected = sessions?.find(se => se.id === selectedChat)
  if (selectedChat) return <section>
    <Link to={`/changes/${changeId}?tab=conversation`}>Back to the current chat</Link>
    <ReadStatus q={q} what="the conversation" /><StaleNote q={q} />
    {data && <UnreadableNote view={data} />}
    {selected ? <><p className="muted small">{chatByline(selected)}</p><ChatMessages chat={selected} pictures={`/api/v1/changes/${changeId}/transcript/${selected.id}/pictures/`} /></> : data && <p>This chat is not recorded.</p>}
  </section>
  // Live, earlier chats read inside the chat itself (EarlierChats); only work records stay here.
  if (live) return earlierRuns.length || rounds.length ? <>
    {earlierRuns.map(id => <RunRounds key={id} id={id} changeId={changeId} />)}
    {rounds.map((e, i) => <ListRow to={workRecordHref(changeId, e)} className="conversation-record-row" key={e.key} identity={<Link to={workRecordHref(changeId, e)}>Helper round {i + 1} · work record</Link>} />)}
  </> : null
  return (
    <Section title="Conversation" count={sessions?.length || undefined} >
      <ReadStatus q={q} what="the conversation" className="small" />
      <StaleNote q={q} />
      {data && <UnreadableNote view={data} />}
      {none && (request ? <>
        <section className="turn"><div className="ask"><div className="ask-t">{firstLine(request)} <Link to={`/changes/${changeId}/records/request`}>full request →</Link></div></div>
        {report ? <div className="reply"><div className="final md" dangerouslySetInnerHTML={{ __html: renderMd(report) }} /></div>
          : rounds.length > 0 && <p className="muted">{rounds.length === 1 ? 'The helper left no messages in this round.' : 'The helper left no messages.'}</p>}
        </section>
      </> : <p className="muted">No chat yet.</p>)}
      {earlierRuns.map(id => <RunRounds key={id} id={id} changeId={changeId} />)}
      {rounds.map((e, i) => <ListRow to={workRecordHref(changeId, e)} className="conversation-record-row" key={e.key} identity={<Link to={workRecordHref(changeId, e)}>Helper round {i + 1} · work record</Link>} />)}
      {sessions?.map((se, i) => (
        <div key={se.id} className="conversation-div">
          {i > 0 && <ChatDivider se={se} />}
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
  return <><ReadStatus q={q} what="helper rounds" /><StaleNote q={q} />{d && runStory(d).filter(e => e.step?.type === 'agent/pi' || e.key.startsWith('work-')).map(e => <ListRow to={workRecordHref(changeId, e)} className="conversation-record-row" key={e.key} identity={<Link to={workRecordHref(changeId, e)}>Run {d.run.number} · {e.step?.name || 'work'} · {fmtWhen(e.at)} · work record</Link>} />)}</>
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

// ChatMessages shows a recorded chat as the same thread a live chat is
// drawn as, its pasted pictures read from that chat's own record. A run's
// steps open as a sheet.
export function ChatMessages({ chat, pictures, noSwitch = false }: { chat: ChatHistoryEntry; pictures: string; noSwitch?: boolean }) {
  const [showActions, setShowActions] = useAgentActions()
  const turns = useMemo(() => toThread(chat.messages, null, true), [chat.messages])
  const picture = useCallback((name: string) => pictures + name, [pictures])
  const [picked, setPicked] = useState('')
  const turn = picked ? turns.find((t) => t.parts.some((p) => p.key === picked)) : undefined
  const run = turn?.parts.find((p) => p.key === picked && p.kind === 'run')
  const close = useCallback(() => setPicked(''), [])
  return (
    <>
      {!noSwitch && <AgentActionsSwitch shown={showActions} onChange={setShowActions} />}
      <Thread turns={turns} pictures={picture} showActions={showActions} onPill={(r: { key: string }) => setPicked(r.key)} />
      {turn && run?.kind === 'run' && <StepsSheet run={run} turn={turn} onClose={close} />}
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
  return <><PageBar title={sessionId ? selected ? chatOpening(selected) : 'Chat' : 'Past chats'} />
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

// Where one chat session ended and the next began: a change has one conversation,
// so the restart is a quiet line in it, not a section.
export function ChatDivider({ se }: { se: { createdAt: string; imported?: boolean } }) {
  return <div className="chat-divider muted small" role="separator">{se.imported ? `moved from another ${PRODUCT}, history only` : `chat restarted · ${fmtWhen(se.createdAt)}`}</div>
}

// The change's finished or moved chats, shown above the open chat's own messages in
// the same scroll. The open chat is not in this list (ended is false), so nothing repeats.
// The transcript read keeps its status lines: unreadable or failed history is not "no history".
export function EarlierChats({ changeId, currentOpenedAt }: { changeId: string; currentOpenedAt?: string }) {
  const q = useGet<ChatHistoryView>(`/changes/${changeId}/transcript`, { poll: 4000 })
  const earlier = (q.data?.sessions ?? []).filter(se => se.ended || se.imported)
  return <>
    <ReadStatus q={q} what="the earlier conversation" className="small" />
    <StaleNote q={q} />
    {q.data && <UnreadableNote view={q.data} />}
    {earlier.map((se, i) => <div key={se.id}>
      {(i > 0 || se.imported) && <ChatDivider se={se} />}
      <ChatMessages noSwitch chat={se} pictures={`/api/v1/changes/${changeId}/transcript/${se.id}/pictures/`} />
    </div>)}
    {earlier.length > 0 && currentOpenedAt && <ChatDivider se={{ createdAt: currentOpenedAt }} />}
  </>
}
