import { useCallback, useEffect, useRef, useState } from 'react'
import { del, HttpError, post } from '../../api'
import { useAction, useGet } from '../../data'
import type { ConsoleOpened, ConsoleSessionList, ConsoleSessionView } from '../../wire.gen'

// Which chat the page shows: the remembered one (or the one the address
// names) if AppTown still has it, else the newest for this change, else a
// new one. A new chat is opened only on an answer this reader received
// itself saying there is none: a failed read, or an answer held from before
// (a chat may have opened since), opens nothing. The page mounts one of these
// per chat address (Console keys it), so an answer read for another change's
// chat never counts as this one's.
//
// The choice is made once per address. After that, chats change only by an
// explicit act: start fresh, end chat, open it on AppTown. A refused open
// is shown and not tried again.

// CHAT_ON_APPTOWN is conversation.CodeChatOnAppTown: the worker refused a
// change's chat for a reason the same chat on AppTown would not meet.
export const CHAT_ON_APPTOWN = 'chat-on-apptown'

// changeId is '' for the project chat.
export type ConsoleAddress = { slug: string; changeId: string; forcedSession: string }

export function useConsoleSession({ slug, changeId, forcedSession }: ConsoleAddress) {
  const storageKey = changeId ? `console-${slug}-change-${changeId}` : `console-${slug}`
  const list = useGet<ConsoleSessionList>(`/projects/${slug}/console`)
  const [session, setSession] = useState<ConsoleSessionView | null>(null)
  const { run, err, failure } = useAction()
  const chosen = useRef(false)

  const choose = useCallback((s: ConsoleSessionView) => {
    sessionStorage.setItem(storageKey, s.id)
    setSession(s)
  }, [storageKey])

  // placement 'apptown' opens a change's chat on AppTown when worker chats
  // are on (after the worker refused it, say); by default AppTown chooses.
  const open = useCallback(async (placement?: 'apptown') => {
    chosen.current = true
    const r = await run(() => post<ConsoleOpened>(`/projects/${slug}/console`, changeId ? { changeId, ...(placement ? { placement } : {}) } : {}))
    if (r) choose(r.session)
  }, [slug, changeId, run, choose])

  const answer = list.current ? list.data : null
  useEffect(() => {
    if (chosen.current || !answer) return
    chosen.current = true
    const remembered = forcedSession || sessionStorage.getItem(storageKey)
    const forChange = answer.sessions.filter((s) => (s.changeId ?? '') === changeId)
    const match = forChange.find((s) => s.id === remembered) ?? forChange[0]
    if (match) choose(match)
    else void open()
  }, [answer, forcedSession, storageKey, changeId, choose, open])

  const startFresh = useCallback(() => {
    sessionStorage.removeItem(storageKey)
    setSession(null)
    void open()
  }, [storageKey, open])

  // Ending starts a fresh chat only once the chat is ended, or AppTown says
  // it no longer exists (404); any other failure is shown and the chat stays.
  const end = useCallback(async () => {
    if (session) {
      const ended = await run(async () => {
        try {
          await del(`/console/${session.id}`)
        } catch (e) {
          if (!(e instanceof HttpError && e.status === 404)) throw e
        }
        return true
      })
      if (!ended) return
    }
    startFresh()
  }, [session, startFresh, run])

  // AppTown's own classification says when a change's chat the worker
  // refused can open on AppTown instead; its sentence is never read.
  const appTownInstead = changeId !== '' && failure instanceof HttpError && failure.code === CHAT_ON_APPTOWN
  return { session, list, openErr: err, appTownInstead, open, startFresh, end }
}
