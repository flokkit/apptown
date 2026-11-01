import { Link } from 'react-router-dom'
import { useGet, type Read } from './data'
import { StaleNote } from './ReadGate'
import type { AttentionView, AuthMe } from './wire.gen'

export function useAttention() {
  const me = useGet<AuthMe>('/auth/me').data
  const signedIn = !!me && (me.mode !== 'internal' || !!me.user)
  return useGet<AttentionView>(signedIn ? '/attention' : null, { poll: 30000 })
}

export function attentionState(q: Pick<Read<AttentionView>, 'data' | 'refreshFailed' | 'paused'>) {
  return {
    count: q.data?.items.length ?? null,
    known: !!q.data && !q.refreshFailed && !q.paused
      && q.data.sources.every((s) => !s.unknown)
      && q.data.items.every((i) => !i.unknown),
  }
}

export function AttentionBadge() {
  const q = useAttention()
  const state = attentionState(q)
  if (state.count === 0 && state.known) return null
  const unknown = q.data ? (q.refreshFailed || q.paused ? Math.max(1, q.data.sources.length) : q.data.sources.filter(s => s.unknown).length) : 0
  const missing = q.err || q.refreshFailed || state.count === 0 ? 'unknown' : '…'
  return <span className="row small">
    <Link className="attention-badge" to="/attention" aria-label={state.count !== null && (state.count > 0 || unknown > 0) ? `${state.count} need attention${unknown ? `; ${unknown} not known` : state.known ? '' : '; coverage incomplete'}` : `Attention ${missing}`}>
      <span className="attention-compact" aria-hidden="true">⚠ {state.count ?? missing}{unknown > 0 ? ` · ${unknown} not known` : ''}</span><span className="attention-words">{state.count === null ? `Attention ${missing}` : unknown > 0 ? `${state.count} · ${unknown} not known` : state.count > 0 ? `${state.count} need attention` : `Attention ${missing}`}</span>
    </Link>
    <StaleNote q={q} inline />
  </span>
}
