import { PreviewBesideChat } from './console/PreviewBesideChat'
import { EarlierChats } from './change/Conversation'
import { useAgentActions } from '../AgentActions'
import { Switch } from '../Switch'
import './Console.css'
import { FormMenu } from '../MoreMenu'
import { PRODUCT } from '../brand'
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { Link, Navigate, useParams, useSearchParams } from 'react-router-dom'
import { ChangeView, post } from '../api'
import { useChatViewport, useNarrow } from '../chrome'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { ChangeStatus } from '../Status'
import { TryItButton, usePreview, usePreviewBoot } from '../TryIt'
import type { ConsoleSessionView } from '../wire.gen'
import { WorkerSaveLine } from './WorkerChat'
import { Bubbles, type ChatActivity } from './console/Bubbles'
import { Activity, type ActivityWord } from './console/Activity'
import { LandConfirm, StartedChanges, changeFacts, landTitle, useLanding, useStartedChanges, type Landing } from './console/ChangeBar'
import { useConsoleSession } from './console/useConsoleSession'
import { useTerminal } from './console/useTerminal'
import { useTechnicalDetail } from './change/TechnicalDetail'

// The chat page (C1.5): a conversation with the project's helper, and the
// same session as a real terminal one toggle away. Which chat it shows
// (useConsoleSession), the terminal (useTerminal), the conversation (Bubbles,
// drawn as a thread) and the change it works on (ChangeBar) are each their
// own part; this page lays them out: one bar, then the conversation.
// Sessions survive disconnects and idle stops (design/subsystems/console.md §5b).

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

// The change page's links, when the chat is its Conversation tab: the bar's
// title and ⋯ menu are the way to the other tabs.
export type ChangeNav = { overview: string; code: string; details: string }

// ?kickoff=1 is "Build the first version"; ?kickoff=go is any change whose
// brief already says what to do (e.g. wiring in an integration); ?kickoff=stuck
// continues a lane that stopped. ?session=<id> lands on one exact session
// ("take it to chat" made it server-side, continuing the lane agent) instead
// of the remembered one.
// shown is false while the change page shows another tab: the chat stays
// mounted, and coming back follows the newest message like a fresh view.
// history is the change's earlier work records, shown above its earlier chats.
export function ChatPage({ slug, changeId, forcedSession, kickoff, shown = true, nav, history }: { slug: string; changeId: string; forcedSession: string; kickoff: string; shown?: boolean; nav?: ChangeNav; history?: ReactNode }) {
  const { session, list, openErr, appTownInstead, open, startFresh, end } = useConsoleSession({ slug, changeId, forcedSession })
  // The layering principle: the conversation is the story, the terminal is
  // under the hood. Same pi, same conversation, two zooms — remembered per person.
  const [zoom, setZoom] = useState<'chat' | 'terminal'>(
    () => (localStorage.getItem('chat-zoom') === 'terminal' ? 'terminal' : 'chat'))
  // The terminal only shows until the person takes control: a key pressed
  // to look around (Escape) must not stop the helper. Leaving it, or
  // another chat, gives control back.
  const [controlOf, setControlOf] = useState<string | null>(null)
  const setZoomKeep = (z: 'chat' | 'terminal') => { setZoom(z); setControlOf(null); localStorage.setItem('chat-zoom', z) }
  const [activity, setActivity] = useState<ChatActivity>({ busy: false, read: 'loading' })
  const onActivity = useCallback((a: ChatActivity) => setActivity(a), [])
  const [showActions, setShowActions] = useAgentActions()
  const narrow = useNarrow()
  useChatViewport(narrow)
  const [technical] = useTechnicalDetail()
  const introKey = `chat-intro-seen-${slug}`
  const [introSeen, setIntroSeen] = useState(() => localStorage.getItem(introKey) === '1')
  const termRef = useRef<HTMLDivElement>(null)
  const mayType = !!session?.mayAct && controlOf === session.id && zoom === 'terminal'
  const status = useTerminal(session?.id ?? null, !!session?.mayAct, termRef, zoom === 'terminal', mayType)

  // Two kinds of chat: the project chat (questions, setup, starting work)
  // and the change chat (where building happens). The bar says which.
  const shopId = session?.changeId || changeId
  const isWorkshop = shopId !== ''
  const change = useGet<ChangeView>(shopId ? `/changes/${shopId}` : null, { poll: 4000 })
  useKickoff(kickoff, slug, changeId, session, status === 'attached', change.data?.change.body)
  const landing = useLanding(shopId, change.data ?? null, session?.id)
  const started = useStartedChanges(session?.id ?? null)
  const shopName = change.data ? `change ${change.data.change.number}` : 'this change'
  const overview = nav?.overview ?? (shopId ? `/changes/${shopId}?tab=overview` : '')

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

  // What the helper is doing. A conversation whose read is not current
  // cannot say whose turn it is: that reads as connection lost.
  const mayAct = !!session?.mayAct
  const word: ActivityWord = !session ? 'starting'
    : mayAct && status !== 'attached' ? 'connecting'
      : activity.read === 'stale' ? 'connection lost'
        : activity.read === 'loading' ? 'connecting'
          : activity.busy ? (landing.stage === 'handed' ? 'checking and landing' : 'working') : 'your turn'

  const showActivity = zoom === 'chat' && (mayAct || word !== 'your turn')

  const notices = <>
    {/* Which chats exist: until it is read, nothing opens; a chat on the
        worker's save line already marks this same read when it is old. */}
    <ReadStatus q={list} what="this project's chats" className="small" />
    {session?.placement !== 'worker' && <StaleNote q={list} />}
    {isWorkshop && <><ReadStatus q={change} what={shopName} className="small" /><StaleNote q={change} /></>}
    {openErr && <div className="error small">
      {openErr} <Link to={shopId ? `/changes/${shopId}` : `/projects/${slug}`}>{shopId ? `Back to ${change.data ? shopName : 'the change'}` : 'Back to the project'}</Link>
      {appTownInstead && <> <button className="subtle" onClick={() => void open('apptown')}>Open it on {PRODUCT} instead</button></>}
    </div>}
    {session?.cliMissing && <div className="error small">
      This helper is missing its {PRODUCT} tools (a build problem on the server) · it can chat and edit, but can't preview, start work, or file complaints. The server log says why.
    </div>}
    {!shopId && !introSeen && <div className="chat-intro">
      <span>Ask about <strong>{slug}</strong> or describe what to build. The helper opens a change for the work.</span>
      <button type="button" className="subtle" onClick={() => { localStorage.setItem(introKey, '1'); setIntroSeen(true) }}>Dismiss</button>
    </div>}
    {session?.continuedFrom && <div className="chat-notice">This chat continues {session.continuedFrom}: it remembers what it did in the lane. That earlier work is on the change page.</div>}
    {/* An unreachable worker leaves this conversation as the last saved copy. */}
    {session?.changeId ? <WorkerSaveLine slug={slug} session={session} /> : null}
    {session && <StartedChanges q={started} />}
  </>

  return (
    <PreviewBesideChat changeId={changeId}>{(previewToggle, previewShown) =>
    <div className={`console ${isWorkshop ? 'console-workshop' : 'console-desk'}`}>
      <div className="chat-bar">
        <div className="chat-bar-title">
          {isWorkshop
            ? <h1 className="chat-title"><Link to={overview} title={change.data?.change.title}>
              {change.data ? <><span className="num">#{change.data.change.number}</span><span className="name">{change.data.change.title}</span></> : 'This change'}</Link></h1>
            : <h1 className="chat-title">Project chat</h1>}
          {!isWorkshop && <span className="chat-kind" title="This chat answers questions, helps set things up and starts work. Building happens in a change chat; the helper gives you the link.">questions &amp; setup</span>}
          {/* One status: what the helper is doing, with the change's state as
              the dot's colour. "your turn" is the chat owner's: someone looking
              sees where the change stands instead. */}
          {(showActivity || change.data) && <Activity word={showActivity ? word : null} change={change.data?.waitingOn} />}
        </div>
        {zoom === 'chat' && mayAct && isWorkshop && change.data && <BarVerbs changeId={shopId} landing={landing} busy={activity.busy} overview={overview} />}
        {zoom === 'terminal' && mayAct && (mayType
          ? <span className="chat-kind" title="Your keys go to the helper; Escape stops what it is doing.">you have control</span>
          : <button type="button" className="bar-btn" title="Type into the helper's terminal. Until then it only shows." onClick={() => setControlOf(session!.id)}>Take control</button>)}
        <button type="button" disabled={!session} className={'bar-btn' + (zoom === 'chat' ? ' bar-wide' : '')} onClick={() => setZoomKeep(zoom === 'chat' ? 'terminal' : 'chat')}>
          {zoom === 'chat' ? 'Terminal' : '← back to the chat'}
        </button>
        <FormMenu className="chat-menu" title={isWorkshop && change.data ? `Change ${change.data.change.number}` : 'This chat'}
          trigger={<button type="button" className="bar-icon" aria-label="Chat details and actions">⋯</button>}>
          <div className="chat-menu-card">
            <dl className="chat-facts">
              {change.data && <><dt>status</dt><dd><ChangeStatus waiting={change.data.waitingOn} /></dd><dt>so far</dt><dd>{changeFacts(change.data)}</dd></>}
              {isWorkshop && <PreviewFact changeId={shopId} />}
              {session?.openedBy && <><dt>opened by</dt><dd>{session.openedBy}</dd></>}
              <dt>model</dt><dd>{session?.model || 'default model'}</dd>
              {session?.continuedFrom && <><dt>continuing</dt><dd>{session.continuedFrom}</dd></>}
              <dt>connection</dt><dd>{status === 'attached' ? 'connected' : status}</dd>
              {/* The session id is what `apptown wait-chat` takes: for
                  diagnosis, so it shows with the technical switch. */}
              {session && technical && <><dt>session</dt><dd className="mono">{session.id}</dd></>}
            </dl>
            {change.data && (change.data.change.summary || change.data.report) && <Link to={`/changes/${change.data.change.id}/records/summary`}>What was made →</Link>}
            {(started.data?.changes ?? []).map((c) => <Link key={c.changeId} to={`/changes/${c.changeId}`}>{c.title} <span className="muted small">{c.stateLabel}</span></Link>)}
            {nav && <><Link to={nav.overview}>Overview</Link><Link to={nav.code}>Code</Link><Link to={nav.details}>Details</Link></>}
            {zoom === 'chat' && <div className="chat-menu-switch">Show helper actions<Switch label="Show helper actions" checked={showActions} onChange={setShowActions} /></div>}
            {zoom === 'chat' && previewToggle && <div className="chat-menu-switch">Preview beside chat{previewToggle}</div>}
            <button type="button" className="subtle" disabled={!session} onClick={() => setZoomKeep(zoom === 'chat' ? 'terminal' : 'chat')}>{zoom === 'chat' ? 'Open terminal' : '← back to the chat'}</button>
            {mayAct && isWorkshop && <RestartPreview changeId={shopId} />}
            {mayAct && <>
              <button type="button" className="subtle" onClick={startFresh}>↺ Start fresh</button>
              <button type="button" className="subtle danger" onClick={() => void end()}>End chat</button>
            </>}
          </div>
        </FormMenu>
      </div>
      <Bubbles before={shopId ? <>{history}<EarlierChats changeId={shopId} currentOpenedAt={session?.createdAt} /></> : undefined}
        hidden={zoom !== 'chat' || !shown} sessionId={session?.id ?? null} attached={status === 'attached'}
        starting={status !== 'attached' || !session ? <StartingLine opening={!session} /> : undefined} onActivity={onActivity}
        readOnly={!session || session.mayAct ? undefined : session.openedBy}
        notices={notices} noColumn={previewShown}
        dockTop={isWorkshop && mayAct ? <LandConfirm landing={landing} hasChat={!!session} /> : undefined}
        columnFacts={change.data ? changeFacts(change.data) : undefined}
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

// BarVerbs are the change's verbs in the bar: see it (Try it / Open) and
// ship it (Check and land, "Land" on a phone). When the change waits on the
// person the same place is the door to that decision; once the helper has
// the request, the activity says so instead.
function BarVerbs({ changeId, landing, busy, overview }: { changeId: string; landing: Landing; busy: boolean; overview: string }) {
  switch (landing.stage) {
    case 'shipped': return <Link className="bar-btn" to={overview}>See the record</Link>
    case 'review': return <Link className="bar-btn finish" to={overview}><span className="long">Review and approve →</span><span className="short">Approve →</span></Link>
    case 'landing': return <Link className="bar-btn" to={overview} title="Nothing goes live until it is approved."><span className="long">The checks are running →</span><span className="short">Checks →</span></Link>
    case 'handed': return null
    default: return <>
      <TryItButton changeId={changeId} className="bar-btn primary" />
      <button type="button" className="bar-btn finish" disabled={busy} title={landTitle(busy)} onClick={landing.ask}>
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12.5l4.5 4.5L19 7.5" /></svg>
        <span className="long">Check and land</span><span className="short">Land</span>
      </button>
    </>
  }
}

// The preview's state in the ⋯ menu: why it failed, or what it was built from.
function PreviewFact({ changeId }: { changeId: string }) {
  const st = usePreview(changeId).data
  if (!st) return null
  const words = st.state === 'broken' ? `failed: ${st.reason ?? ''}` : st.state === 'unavailable' ? `not known: ${st.reason ?? ''}`
    : st.state === 'live' ? 'running' : st.state === 'none' ? 'nothing to try yet' : st.state === 'starting' ? 'starting' : 'not started'
  return <><dt>preview</dt><dd>{words}</dd></>
}

function RestartPreview({ changeId }: { changeId: string }) {
  const { state, restart } = usePreviewBoot(changeId)
  if (state !== 'live') return null
  return <button type="button" className="subtle" title="Stop the preview and start it again from the current files" onClick={() => void restart()}>Restart preview</button>
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
