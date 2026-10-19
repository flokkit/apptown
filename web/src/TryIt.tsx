import { PRODUCT } from './brand'
import type { CSSProperties } from 'react'
import { openAfter, post } from './api'
import { useAction, useGet } from './data'
import { hostOnlyWords } from './runtime'

// TryIt: the one try-it button. It follows the preview's state from the
// server instead of guessing: nothing to try yet (no contract — the first
// change makes the app), Try it (cold), starting…, Open (live, with a quiet
// restart), didn't start (with the reason, and Try again), or not known
// (previews run on a worker the forge could not ask, with the reason). Every
// boot path records that state, so what the rehearsal loop or another tab
// did shows here too.
type PreviewState = { state: 'none' | 'cold' | 'starting' | 'live' | 'broken' | 'unavailable'; url?: string; hostOnly?: boolean; reason?: string; at?: string }

export function TryIt({ changeId, label, style, className }: {
  changeId: string; label?: string; style?: CSSProperties; className?: string
}) {
  const { data: st, reload: load } = useGet<PreviewState>(`/changes/${changeId}/preview`, { poll: 5000 })
  const { run, busy, err } = useAction()

  const boot = async (path: string) => {
    // An address that answers on the forge host only is shown, not opened.
    await run(() => openAfter(async () => { const r = await post<{ url: string; hostOnly?: boolean }>(path, {}); return r.hostOnly ? '' : r.url }))
    // A boot that failed records why on the preview, so read it either way.
    void load()
  }
  const cls = className ?? 'primary'
  const state = busy ? 'starting' : (st?.state ?? 'cold')

  if (state === 'none') {
    return (
      <span className="row" style={{ gap: 8 }}>
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
      <span className="row" style={{ gap: 6 }}>
        <span className="chip pass" title={`${st.url} answers on ${PRODUCT}'s host only`}>preview running</span>
        <span className="muted small">{hostOnlyWords(st.url)}</span>
        <button className="subtle" title="Stop the preview and start it again from the current files"
          onClick={() => boot(`/changes/${changeId}/preview/restart`)}>restart</button>
        {err && <span className="error small">{err}</span>}
      </span>
    )
  }
  if (state === 'live' && st?.url) {
    return (
      <span className="row" style={{ gap: 6 }}>
        <button className={cls} style={style} onClick={() => window.open(st.url, '_blank')}>{label ? label.replace(/try it/i, 'open') : '▶ Open'}</button>
        <button className="subtle" title="Stop the preview and start it again from the current files"
          onClick={() => boot(`/changes/${changeId}/preview/restart`)}>restart</button>
        {err && <span className="error small">{err}</span>}
      </span>
    )
  }
  return (
    <span className="row" style={{ gap: 8, flexWrap: 'wrap', minWidth: 0, maxWidth: '100%' }}>
      {state === 'broken' && (
        <span className="chip fail" title={st?.reason}>didn't start</span>
      )}
      {state === 'unavailable' && (
        <span className="chip pending" title={st?.reason}>preview state not known</span>
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
