import { PRODUCT } from '../brand'
import { useEffect, useRef, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { ChangeView, post } from '../api'
import AgentActivity from '../AgentActivity'
import { setPageContext, useNarrow } from '../chrome'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { ConsoleSessionView } from '../wire.gen'
import { WorkerSaveLine } from './WorkerChat'
import { Bubbles } from './console/Bubbles'
import { ChangeContext, changeChip, StartedChanges, WorkshopActions } from './console/ChangeBar'
import { useConsoleSession } from './console/useConsoleSession'
import { useTerminal } from './console/useTerminal'

// The chat page (C1.5): a conversation with the project's helper, and the
// same session as a real terminal one toggle away. Which chat it shows
// (useConsoleSession), the terminal (useTerminal), the conversation (Bubbles)
// and the change it works on (ChangeBar) are each their own part; this page
// lays them out. Sessions survive disconnects and idle stops (see
// design/subsystems/console.md §5b).

// Console is keyed by the chat address, so each address gets its own reading
// of which chats exist: moving to another change's chat never acts on what
// was read for the last one.
export default function Console() {
  const { slug } = useParams()
  const [params] = useSearchParams()
  const change = params.get('change') ?? ''
  const forced = params.get('session') ?? ''
  return <ChatPage key={`${slug}\n${change}\n${forced}`} slug={slug!} changeId={change}
    forcedSession={forced} kickoff={params.get('kickoff') ?? ''} />
}

// ?kickoff=1 is "Build the first version"; ?kickoff=go is any change whose
// brief already says what to do (e.g. wiring in an integration); ?kickoff=stuck
// continues a lane that stopped. ?session=<id> lands on one exact session
// ("take it to chat" made it server-side, continuing the lane agent) instead
// of the remembered one.
function ChatPage({ slug, changeId, forcedSession, kickoff }: { slug: string; changeId: string; forcedSession: string; kickoff: string }) {
  const { session, list, openErr, appTownInstead, open, startFresh, end } = useConsoleSession({ slug, changeId, forcedSession })
  // The layering principle: bubbles are the story, the terminal is under
  // the hood. Same pi, same conversation, two zooms — remembered per person.
  const [zoom, setZoom] = useState<'chat' | 'terminal'>(
    () => (localStorage.getItem('chat-zoom') === 'terminal' ? 'terminal' : 'chat'))
  const setZoomKeep = (z: 'chat' | 'terminal') => { setZoom(z); localStorage.setItem('chat-zoom', z) }
  const [robotBusy, setRobotBusy] = useState(false)
  // What the chat is about is one tap down on a phone, open on a desk
  // (styles.css hides the fold's line on a desk). React writes `open` only
  // when this changes, so a tap holds until the screen crosses the breakpoint.
  const narrow = useNarrow()
  const introKey = `chat-intro-seen-${slug}`
  const [introSeen, setIntroSeen] = useState(() => localStorage.getItem(introKey) === '1')
  const termRef = useRef<HTMLDivElement>(null)
  const status = useTerminal(session?.id ?? null, !!session?.mayAct, termRef)
  useKickoff(kickoff, slug, changeId, session, status === 'attached')

  // Two visually distinct kinds of chat:
  // the project chat (questions, setup, starting work) and the change chat
  // (where building happens). People were confusing them; the chat says
  // which it is before a word is typed.
  const shopId = session?.changeId || changeId
  const isWorkshop = shopId !== ''
  // One read of the change feeds the action bar and the status card; its
  // status shows once, above both.
  const change = useGet<ChangeView>(shopId ? `/changes/${shopId}` : null, { poll: 4000 })
  // A change is named by its number, known once the change is read.
  const shopName = change.data ? `change ${change.data.change.number}` : 'this change'
  useEffect(() => {
    setPageContext({ crumbs: [{ label: shopId ? `Chat · ${shopName}` : 'Chat' }] })
    return () => setPageContext({})
  }, [shopId, shopName])
  if (!session && openErr) return <div><h1>Chat is not available here</h1>
    <p><Link to={`/projects/${slug}`}>Open a change from the project</Link></p>
    <details><summary>Details</summary><p>{openErr}</p>{appTownInstead && <button onClick={() => void open('apptown')}>Open it on {PRODUCT} instead</button>}</details>
  </div>
  return (
    <div className={`console ${isWorkshop ? 'console-workshop' : 'console-desk'}`}>
      {/* One row, three zones (layering: story left, escape hatches right):
          where am I → what is this chat about → (only if degraded) connection.
          The robot buddy owns the turn state; plumbing (model, session, reset
          actions) lives behind ⋯ where those who need it can find it. */}
      <div className="row console-head" style={{ marginBottom: 8 }}>
        <h1 style={{ margin: 0 }}>{isWorkshop ? 'Change chat' : 'Project chat'}</h1>
        {/* The address names the change before the session answers: a
            change chat says it is one from its first paint, never the
            project chat while it connects. */}
        {shopId
          ? <Link className="chip here" to={`/changes/${shopId}`}
              title="What you and the helper change here is saved onto this change.">{shopName}</Link>
          : <span className="chip here" title="This chat answers questions, helps set things up and starts work. Building happens in a change chat; the helper gives you the link.">questions &amp; setup</span>}
        {session?.mayAct && status !== 'attached' && (
          <span className="chip pending">{status === 'connecting' ? 'connecting…' : 'reconnecting…'}</span>
        )}
        <span style={{ flex: 1 }} />
        {session?.mayAct && zoom === 'chat' && <AgentActivity busy={robotBusy} connected={status === 'attached'} />}
        <button disabled={!session} className="subtle" onClick={() => setZoomKeep(zoom === 'chat' ? 'terminal' : 'chat')}>
          {zoom === 'chat' ? 'Terminal ▾' : '← back to the chat'}
        </button>
        <details className="head-menu">
          <summary title="session details and resets">⋯</summary>
          <div className="head-menu-card">
            <div className="muted small" style={{ padding: '4px 6px' }}>
              {session?.openedBy && <>opened by {session.openedBy}<br /></>}
              {session?.model || 'default model'}<br />
              {session?.continuedFrom && <>continuing {session.continuedFrom}<br /></>}
              {status === 'attached' ? 'connected' : status}
              {session && <><br /><span title={session.id}>session {session.id.slice(0, 8)}…</span></>}
            </div>
            {session?.mayAct && <>
              <button className="subtle" onClick={startFresh}>↺ start fresh</button>
              <button className="subtle danger" onClick={() => void end()}>end chat</button>
            </>}
          </div>
        </details>
      </div>
      {/* Which chats exist: until it is read, nothing opens; a chat on the
          worker's save line already marks this same read when it is old. */}
      <ReadStatus q={list} what="this project's chats" />
      {session?.placement !== 'worker' && <StaleNote q={list} />}
      {isWorkshop && (
        <div className="console-actions">
          <ReadStatus q={change} what={shopName} />
          <StaleNote q={change} />
        </div>
      )}
      {isWorkshop && changeId !== '' && session?.mayAct && zoom === 'chat' && change.data && (
        <div className="console-actions">
          <WorkshopActions changeId={changeId} view={change.data} robotBusy={robotBusy} sessionId={session?.id} />
        </div>
      )}
      {openErr && (
        <div className="error">
          {openErr}
          {appTownInstead && (
            <> <button className="subtle" onClick={() => void open('apptown')}>Open it on {PRODUCT} instead</button></>
          )}
        </div>
      )}
      {session?.cliMissing && (
        <div className="error">
          This helper is missing its {PRODUCT} tools (a build problem on the
          server) · it can chat and edit, but can't preview, start work, or
          file complaints. The server log says why.
        </div>
      )}
      {/* What the chat is about sits above it, folded to one line on a
          phone, and steps aside while you type, so the conversation keeps
          the room (styles.css .console-extra, .chat-pane). The worker's
          save line and the changes this chat started stay outside the fold:
          one qualifies the conversation, the other may be the next thing to
          do. */}
      <div className="console-extra">
        {(shopId || !introSeen || session?.continuedFrom) && (
          <details className="console-about" open={!narrow}>
            <summary className="small">
              {shopId
                ? <>{shopName}{change.data && <> · {changeChip(change.data)}</>}</>
                : 'About this chat'}
            </summary>
            {!introSeen && <ChatIntro changeName={shopId ? shopName : undefined} slug={slug}
              onDismiss={() => { localStorage.setItem(introKey, '1'); setIntroSeen(true) }} />}
            {session?.continuedFrom && (
              <div className="muted small" style={{ marginBottom: 8 }}>
                This chat continues {session.continuedFrom}: it remembers what it did in the lane. That earlier work is on the change page.
              </div>
            )}
            {session?.changeId && change.data ? <ChangeContext view={change.data} /> : null}
          </details>
        )}
        {/* Outside the fold: when the worker cannot be reached, this line
            says the conversation shown is AppTown's last copy. */}
        {session?.changeId ? <WorkerSaveLine slug={slug} session={session} /> : null}
        {session && <StartedChanges sessionId={session.id} />}
      </div>
      {session && zoom === 'chat' && (
        <Bubbles key={session.id} sessionId={session.id} onBusy={setRobotBusy}
          readOnly={session.mayAct ? undefined : session.openedBy}
          starters={session.changeId ? undefined : [
            // The project chat's three jobs, as things you could say (the
            // empty page's next action, design/subsystems/ui-coherence.md).
            'What does this project do right now?',
            'I want to change something',
            'Set up a database for it',
          ]} />
      )}
      <div className="console-term" ref={termRef}
        style={zoom === 'terminal' ? undefined : { position: 'absolute', left: -9999, width: 600, height: 400 }} />
    </div>
  )
}

// "Build the first version" arrives with ?kickoff=1: send the opening
// message on the operator's behalf (that IS what the button says), once —
// the guard survives refreshes so a reload can't re-kick the helper.
function useKickoff(kickoff: string, slug: string, changeId: string, session: ConsoleSessionView | null, attached: boolean) {
  useEffect(() => {
    if (!kickoff || !session || !session.mayAct || !attached || changeId === '') return
    const guard = kickoff === 'stuck' ? `kickoff-sent-${slug}-${changeId}-${session.id}` : `kickoff-sent-${slug}-${changeId}`
    if (localStorage.getItem(guard) === '1') return
    localStorage.setItem(guard, '1')
    const text = kickoff === 'stuck'
      ? 'The lane stopped — where and why is in your briefing under "Where the lane stopped". Say where things stand and what you think went wrong, in plain words; then we go from there.'
      : kickoff === 'go'
      ? 'Please do what this change asks — it is in your briefing (the Change intent). Tell me what you changed and when I can try it.'
      : 'Please build the first version now. What it should do is in your briefing (the Change intent) — if the briefing says nothing was written, ask me first. Keep it simple, get it working, and tell me as soon as I can try it.'
    post(`/console/${session.id}/say`, { text }).catch(() => localStorage.removeItem(guard))
  }, [kickoff, session, attached, changeId, slug])
}

// A one-card answer to "what do I do here, and what will happen?" —
// dismissible per project, because operators know and first-time users don't yet.
function ChatIntro({ changeName, slug, onDismiss }: { changeName?: string; slug: string; onDismiss: () => void }) {
  return (
    <div className="card row tone-accent" style={{ marginBottom: 8 }}>
      <span className="small" style={{ flex: 1, lineHeight: 1.5 }}>
        {changeName ? (
          <>Say what to change. Checks and an approval come before it lands.</>
        ) : (
          <>Ask about <strong>{slug}</strong> or describe what to build. The helper opens a change for the work.</>
        )}
      </span>
      <button className="subtle" onClick={onDismiss}>Dismiss</button>
    </div>
  )
}
