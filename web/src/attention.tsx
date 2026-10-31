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
      && q.data.sources.every((s) => !s.unknown && !s.stale)
      && q.data.items.every((i) => !i.stale),
  }
}

export function AttentionBadge() {
  const q = useAttention()
  const state = attentionState(q)
  if (state.count === 0 && state.known) return null
  const missing = q.err || q.refreshFailed || state.count === 0 ? 'unknown' : '…'
  return <span className="row small">
    <Link className="attention-badge" to="/attention" aria-label={state.count ? `${state.count} need attention${state.known ? '' : '; coverage incomplete'}` : `Attention ${missing}`}>
      <span className="attention-compact" aria-hidden="true">⚠ {state.count || missing}</span><span className="attention-words">{state.count === null || state.count === 0 ? `Attention ${missing}` : `${state.count} need attention`}</span>
    </Link>
    <StaleNote q={q} inline />
  </span>
}
