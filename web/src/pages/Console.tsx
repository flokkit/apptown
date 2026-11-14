import { PreviewBesideChat } from './console/PreviewBesideChat'
import { EarlierChats } from './change/Conversation'
import { AgentActionsSwitch, useAgentActions } from '../AgentActions'
import './Console.css'
import { FormMenu } from '../MoreMenu'
import { PRODUCT } from '../brand'
import { useEffect, useRef, useState } from 'react'
import { Link, Navigate, useParams, useSearchParams } from 'react-router-dom'
import { ChangeView, post } from '../api'
import AgentActivity from '../AgentActivity'
import { useChatViewport, useNarrow } from '../chrome'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { ConsoleSessionView } from '../wire.gen'
import { WorkerSaveLine } from './WorkerChat'
import { Bubbles } from './console/Bubbles'
import { ChangeFacts, StartedChanges, WorkshopActions } from './console/ChangeBar'
import { useConsoleSession } from './console/useConsoleSession'
import { useTerminal } from './console/useTerminal'
import { useTechnicalDetail } from './change/TechnicalDetail'

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
  if (change) {
    const target = new URLSearchParams({ tab: 'conversation' })
    for (const name of ['kickoff', 'session']) if (params.has(name)) target.set(name, params.get(name)!)
    return <Navigate replace to={`/changes/${encodeURIComponent(change)}?${target}`} />
  }
  return <ChatPage key={`${slug}\n${change}\n${forced}`} slug={slug!} changeId={change}
    forcedSession={forced} kickoff={params.get('kickoff') ?? ''} />
}

// ?kickoff=1 is "Build the first version"; ?kickoff=go is any change whose
// brief already says what to do (e.g. wiring in an integration); ?kickoff=stuck
// continues a lane that stopped. ?session=<id> lands on one exact session
// ("take it to chat" made it server-side, continuing the lane agent) instead
// of the remembered one.
// shown is false while the change page shows another tab: the chat stays
// mounted, and coming back follows the newest message like a fresh view.
export function ChatPage({ slug, changeId, forcedSession, kickoff, shown = true }: { slug: string; changeId: string; forcedSession: string; kickoff: string; shown?: boolean }) {
  const { session, list, openErr, appTownInstead, open, startFresh, end } = useConsoleSession({ slug, changeId, forcedSession })
  // The layering principle: bubbles are the story, the terminal is under
  // the hood. Same pi, same conversation, two zooms — remembered per person.
  const [zoom, setZoom] = useState<'chat' | 'terminal'>(
    () => (localStorage.getItem('chat-zoom') === 'terminal' ? 'terminal' : 'chat'))
  // The terminal only shows until the person takes control: a key pressed
  // to look around (Escape) must not stop the helper. Leaving it, or
  // another chat, gives control back.
  const [controlOf, setControlOf] = useState<string | null>(null)
  const setZoomKeep = (z: 'chat' | 'terminal') => { setZoom(z); setControlOf(null); localStorage.setItem('chat-zoom', z) }
  const [robotBusy, setRobotBusy] = useState(false)
  const [showActions, setShowActions] = useAgentActions()
  // Context stays inline at both widths.
  const narrow = useNarrow()
  useChatViewport(narrow)
  const [technical] = useTechnicalDetail()
  const introKey = `chat-intro-seen-${slug}`
  const [introSeen, setIntroSeen] = useState(() => localStorage.getItem(introKey) === '1')
  const termRef = useRef<HTMLDivElement>(null)
  const mayType = !!session?.mayAct && controlOf === session.id && zoom === 'terminal'
  const status = useTerminal(session?.id ?? null, !!session?.mayAct, termRef, zoom === 'terminal', mayType)

  // Two visually distinct kinds of chat:
  // the project chat (questions, setup, starting work) and the change chat
  // (where building happens). People were confusing them; the chat says
  // which it is before a word is typed.
  const shopId = session?.changeId || changeId
  const isWorkshop = shopId !== ''
  // The action bar shares the change reading with the surrounding page.
  const change = useGet<ChangeView>(shopId ? `/changes/${shopId}` : null, { poll: 4000 })
  useKickoff(kickoff, slug, changeId, session, status === 'attached', change.data?.change.body)
  // A change is named by its number, known once the change is read.
  const shopName = change.data ? `change ${change.data.change.number}` : 'this change'
  // A chat that cannot open says why in one line and goes back where the
  // person came from: the change, or the project for its own chat.
  if (!session && openErr) return <div>
    {!isWorkshop && <h1>Project chat</h1>}
    <p className="row console-chat-page-p">
      <span>Chat can't open here: {openErr}</span>
      <Link to={shopId ? `/changes/${shopId}` : `/projects/${slug}`}>{shopId ? `Back to ${change.data ? shopName : 'the change'}` : 'Back to the project'}</Link>
      {appTownInstead && <button onClick={() => void open('apptown')}>Open it on {PRODUCT} instead</button>}
    </p>
  </div>
  return (
    <PreviewBesideChat changeId={changeId}>{previewToggle =>
    <div className={`console ${isWorkshop ? 'console-workshop' : 'console-desk'}`}>
      <div className="console-controls">
      {/* One row, three zones (layering: story left, escape hatches right):
          where am I → what is this chat about → (only if degraded) connection.
          The robot buddy owns the turn state; plumbing (model, session, reset
          actions) lives behind ⋯ where those who need it can find it. */}
      <div className="row console-head mb-2" >
        {!isWorkshop && <>
          <h1 className="console-chat-page-h1">Project chat</h1>
          <span className="badge here" title="This chat answers questions, helps set things up and starts work. Building happens in a change chat; the helper gives you the link.">questions &amp; setup</span>
        </>}
        {/* The change's actions share the head's line: the page is the conversation's. */}
        {isWorkshop && change.data && zoom === 'chat' && (change.data.change.summary || change.data.report) && <Link className="small" to={`/changes/${change.data.change.id}/records/summary`}>What was made →</Link>}
        <span className="console-head-space grow" />
        {previewToggle}
        {isWorkshop && changeId !== '' && session?.mayAct && zoom === 'chat' && change.data && <span className="console-strip"><WorkshopActions compact changeId={changeId} view={change.data} robotBusy={robotBusy} sessionId={session?.id} /></span>}
        {session?.mayAct && zoom === 'chat' && (!shopId || status !== 'attached') && <AgentActivity busy={robotBusy} connected={status === 'attached'} />}
        {zoom === 'terminal' && session?.mayAct && (mayType
          ? <span className="badge here" title="Your keys go to the helper; Escape stops what it is doing.">you have control</span>
          : <button className="subtle" title="Type into the helper's terminal. Until then it only shows." onClick={() => setControlOf(session.id)}>Take control</button>)}
        {(!narrow || zoom !== 'chat') && <button disabled={!session} className="subtle" onClick={() => setZoomKeep(zoom === 'chat' ? 'terminal' : 'chat')}>
          {zoom === 'chat' ? 'Terminal ▾' : '← back to the chat'}
        </button>}
        <FormMenu className="head-menu" trigger={<button className="subtle" aria-label="Session details and resets">⋯</button>}>
          <div className="head-menu-card">
            {change.data && <ChangeFacts view={change.data} />}
            <div className="muted small console-chat-page-div">
              {session?.openedBy && <>opened by {session.openedBy}<br /></>}
              {session?.model || 'default model'}<br />
              {session?.continuedFrom && <>continuing {session.continuedFrom}<br /></>}
              {status === 'attached' ? 'connected' : status}
              {/* The session id is what `apptown wait-chat` takes: for
                  diagnosis, so it shows with the technical switch. */}
              {session && technical && <><br /><span className="mono">session {session.id}</span></>}
            </div>
            {/* A remembered way of reading the chat, not something to flip
                mid-conversation: it lives here, not above the input. */}
            {zoom === 'chat' && <AgentActionsSwitch shown={showActions} onChange={setShowActions} />}
            {narrow && zoom === 'chat' && <button className="subtle" disabled={!session} onClick={() => setZoomKeep('terminal')}>Open terminal</button>}
            {session?.mayAct && <>
              <button className="subtle" onClick={startFresh}>↺ Start fresh</button>
              <button className="subtle danger" onClick={() => void end()}>End chat</button>
            </>}
          </div>
        </FormMenu>
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
      {openErr && (
        <div className="error">
          {openErr}
          <Link to={shopId ? `/changes/${shopId}` : `/projects/${slug}`}>{shopId ? `Back to ${change.data ? shopName : 'the change'}` : 'Back to the project'}</Link>
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
      {/* Keep context, the worker's saved-copy warning and started changes visible. */}
      <div className="console-extra console-secondary">
        {((!shopId && !introSeen) || session?.continuedFrom) && (
          <div>
            <section className="console-about">
              {!shopId && <h3 className="small">About this chat</h3>}
              {!shopId && !introSeen && <ChatIntro slug={slug}
                onDismiss={() => { localStorage.setItem(introKey, '1'); setIntroSeen(true) }} />}
              {session?.continuedFrom && (
                <div className="muted small mb-2" >
                  This chat continues {session.continuedFrom}: it remembers what it did in the lane. That earlier work is on the change page.
                </div>
              )}
            </section>
          </div>
        )}
      </div>
      </div>
      <div className="console-extra">
        {/* An unreachable worker leaves this conversation as the last saved copy. */}
        {session?.changeId ? <WorkerSaveLine slug={slug} session={session} /> : null}
        {session && <StartedChanges sessionId={session.id} />}
      </div>
      <Bubbles before={shopId ? <EarlierChats changeId={shopId} currentOpenedAt={session?.createdAt} /> : undefined} hidden={zoom !== 'chat' || !shown} sessionId={session?.id ?? null} attached={status === 'attached'}
          starting={status !== 'attached' || !session ? <StartingLine opening={!session} /> : undefined} onBusy={setRobotBusy}
          readOnly={!session || session.mayAct ? undefined : session.openedBy}
          starters={shopId ? undefined : [
            // The project chat's three jobs, as things you could say (the
            // empty page's next action, design/subsystems/ui-coherence.md).
            'What does this project do right now?',
            'I want to change something',
            'Set up a database for it',
          ]} />
      <div className={`console-term${zoom === 'terminal' ? '' : ' console-term-offscreen'}`} ref={termRef} />
    </div>
    }</PreviewBesideChat>
  )
}

// "Build the first version" arrives with ?kickoff=1: send the opening
// message on the operator's behalf (that IS what the button says), once —
// the guard survives refreshes so a reload can't re-kick the helper.
// kickoffText is the opening message for a change chat. It carries the
// change's request in the person's own words.
export function kickoffText(kickoff: string, request: string): string {
  const asked = request.trim()
  if (kickoff === 'stuck') return '[apptown button: Open the conversation] The lane stopped — where and why is in your briefing under "Where the lane stopped". Say where things stand and what you think went wrong, in plain words; then we go from there.'
  if (kickoff === 'go') return '[apptown button: Do it in a chat] ' + (asked ? `Please do this:\n\n${asked}\n\nTell me what you changed and when I can try it.` : 'Please do what this change asks. Tell me what you changed and when I can try it.')
  return '[apptown button: Build the first version] ' + (asked ? `Please build the first version:\n\n${asked}\n\nKeep it simple, get it working, and tell me as soon as I can try it.` : 'Please build the first version. Nothing was written about what it should do: ask me first.')
}

// The opening message carries the change's request in the person's own
// words, so the chat shows what the agent was asked, not a pointer to a
// briefing nobody can see. It waits until the change is read.
function useKickoff(kickoff: string, slug: string, changeId: string, session: ConsoleSessionView | null, attached: boolean, request: string | undefined) {
  useEffect(() => {
    if (!kickoff || !session || !session.mayAct || !attached || changeId === '' || request === undefined) return
    const guard = kickoff === 'stuck' ? `kickoff-sent-${slug}-${changeId}-${session.id}` : `kickoff-sent-${slug}-${changeId}`
    if (localStorage.getItem(guard) === '1') return
    localStorage.setItem(guard, '1')
    const text = kickoffText(kickoff, request)
    post(`/console/${session.id}/say`, { text }).catch(() => localStorage.removeItem(guard))
  }, [kickoff, session, attached, changeId, slug, request])
}

// The project chat's one-card answer to "what happens here?": nothing is
// built in it; the helper opens a change. Dismissible per project. A change
// chat has none: its box says what to do, and the change shows the rest.
function ChatIntro({ slug, onDismiss }: { slug: string; onDismiss: () => void }) {
  return (
    <div className="card row tone-accent mb-2" >
      <span className="small grow console-chat-intro-span">
        Ask about <strong>{slug}</strong> or describe what to build. The helper opens a change for the work.
      </span>
      <button className="subtle" onClick={onDismiss}>Dismiss</button>
    </div>
  )
}

// Count real elapsed seconds without motion or guessed progress.
function StartingLine({ opening }: { opening: boolean }) {
  const [since] = useState(() => Date.now())
  const [seconds, setSeconds] = useState(0)
  useEffect(() => {
    const timer = setInterval(() => setSeconds(Math.floor((Date.now() - since) / 1000)), 1000)
    return () => clearInterval(timer)
  }, [since])
  return <div className="chat-starting" role="status">
    {opening ? 'Starting your helper…' : 'Connecting…'} <span className="muted small" aria-hidden="true">{seconds}s</span>
  </div>
}
