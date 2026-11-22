import { fmtStamp } from './format'
import { Status } from './Status'
import { PRODUCT } from './brand'
import type { CSSProperties } from 'react'
import { openAfter, post } from './api'
import { useAction, useGet } from './data'
import { hostOnlyWords } from './runtime'

// TryIt: the one try-it button. It follows the preview's state from the
// server instead of guessing: nothing to try yet (no contract — the first
// change makes the app), Try it (cold), starting…, Open (live, with a quiet
// restart), didn't start (with the reason, and Try again), or not known
// (previews run on a worker AppTown could not ask, with the reason). Every
// boot path records that state, so what the rehearsal loop or another tab
// did shows here too.
export type PreviewState = {
  state: 'none' | 'cold' | 'starting' | 'live' | 'broken' | 'unavailable'; url?: string; hostOnly?: boolean; reason?: string; at?: string
  // built: when the running preview was booted from what it serves; commit:
  // the commit, only when it was booted from one (a change's working files
  // are not a commit, so they never claim one).
  built?: string; commit?: string
}

// All preview readers share the same request, answer and polling through useGet.
export const usePreview = (changeId: string) => useGet<PreviewState>(`/changes/${changeId}/preview`, { poll: 5000 })

// builtWords says what the running preview was built from, and when.
export function builtWords(st?: PreviewState): string {
  if (!st?.built) return ''
  const when = fmtStamp(st.built)
  return st.commit ? `built from commit ${st.commit.slice(0, 7)} at ${when}` : `built from the change's working files at ${when}`
}

// hideBuilt leaves out when the preview was built, for the chat's strip:
// Open always serves the change's files as they are now.
// usePreviewBoot is the preview's state and the act of starting or opening
// it, shared by the change page's Try it and the chat bar's.
export function usePreviewBoot(changeId: string) {
  const { data: st, reload: load } = usePreview(changeId)
  const { run, busy, err } = useAction()
  const boot = async (path: string) => {
    // An address that answers on the AppTown host only is shown, not opened.
    await run(() => openAfter(async () => { const r = await post<{ url: string; hostOnly?: boolean }>(path, {}); return r.hostOnly ? '' : r.url }))
    // A boot that failed records why on the preview, so read it either way.
    void load()
  }
  const state = busy ? 'starting' : (st?.state ?? 'cold')
  return { st, state, err, open: () => boot(`/changes/${changeId}/preview`), restart: () => boot(`/changes/${changeId}/preview/restart`) }
}

// TryItButton is Try it as one button, for the chat bar: its state is the
// button's word, and why it failed or what it was built from is its title
// (and in the chat's ⋯ menu).
export function TryItButton({ changeId, className = '' }: { changeId: string; className?: string }) {
  const { st, state, err, open } = usePreviewBoot(changeId)
  if (state === 'none') return <button type="button" className={className} disabled title="the first change makes the app">Nothing to try yet</button>
  if (state === 'starting') return <button type="button" className={className} disabled>starting…</button>
  const failed = state === 'broken' || state === 'unavailable'
  const live = state === 'live' && !!st?.url
  const title = err || (failed ? `${state === 'broken' ? 'failed' : 'not known'}: ${st?.reason ?? ''}` : live ? (st!.hostOnly ? hostOnlyWords(st!.url!) : builtWords(st) || "Opens the preview of the change's files as they are now") : "Starts a preview of the change's files as they are now")
  return <button type="button" className={`${className}${failed ? ' failed' : ''}`} title={title} onClick={() => void open()}>
    <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 5.5v13l10-6.5z" /></svg>
    <span className="tryit-word">{failed ? 'Try again' : live ? 'Open' : 'Try it'}</span>
  </button>
}

export function TryIt({ changeId, label, style, className, hideRestart = false, hideBuilt = false }: {
  changeId: string; label?: string; style?: CSSProperties; className?: string; hideRestart?: boolean; hideBuilt?: boolean
}) {
  const { st, state, err, open, restart } = usePreviewBoot(changeId)
  const boot = (path: string) => path.endsWith('/restart') ? restart() : open()
  const cls = className ?? 'primary'

  if (state === 'none') {
    return (
      <span className="row tryit" style={{ gap: 8 }}>
        <button disabled style={style} title="the app has no runtime contract yet">Nothing to try yet</button>
        <span className="muted small">the first change makes the app</span>
      </span>
    )
  }
  if (state === 'starting') {
    return <button className={cls} disabled style={style}>starting…</button>
  }
  if (state === 'live' && st?.url && st.hostOnly) {
    return (
      <span className="row tryit" style={{ gap: 6 }}>
        <Status observation word={"running"} help={`${st.url} answers on ${PRODUCT}'s host only`} />
        <span className="muted small">{hostOnlyWords(st.url)}</span>
        {builtWords(st) && <span className="muted small">{builtWords(st)}</span>}
        <button hidden={hideRestart} className="subtle tryit-restart" title="Stop the preview and start it again from the current files"
          onClick={() => boot(`/changes/${changeId}/preview/restart`)}>restart</button>
        {err && <span className="error small">{err}</span>}
      </span>
    )
  }
  if (state === 'live' && st?.url) {
    // Open asks AppTown for the preview, as Try it does: it reuses the
    // running one only while it serves the files as they are now, and
    // builds again otherwise, so what opens is never an older version.
    return (
      <span className="row tryit" style={{ gap: 6 }}>
        <button className={cls} style={style} title="Opens the preview of the change's files as they are now (built again if they changed)"
          onClick={() => boot(`/changes/${changeId}/preview`)}>{label ?? '▶ Open'}</button>
        {!hideBuilt && builtWords(st) && <span className="muted small tryit-built">{builtWords(st)}</span>}
        <button hidden={hideRestart} className="subtle tryit-restart" title="Stop the preview and start it again from the current files"
          onClick={() => boot(`/changes/${changeId}/preview/restart`)}>restart</button>
        {err && <span className="error small">{err}</span>}
      </span>
    )
  }
  return (
    <span className="row tryit" style={{ gap: 8, flexWrap: 'wrap', minWidth: 0, maxWidth: '100%' }}>
      {state === 'broken' && (
        <Status observation word={"failed"} help={st?.reason} />
      )}
      {state === 'unavailable' && (
        <Status observation word={"unknown"} help={st?.reason} />
      )}
      <button className={cls} style={style} onClick={() => boot(`/changes/${changeId}/preview`)}>
        {state === 'broken' || state === 'unavailable' ? '▶ Try again' : label ?? '▶ Try it'}
      </button>
      {(state === 'broken' || state === 'unavailable') && st?.reason && (
        <span className="muted small" style={{ maxWidth: 'min(420px, 100%)', minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={st.reason}>{st.reason}</span>
      )}
      {err && <span className="error small">{err}</span>}
    </span>
  )
}
