import { Link } from 'react-router-dom'
import { useGet, type Read } from './data'
import { StaleNote } from './ReadGate'
import type { AttentionView, AuthMe } from './wire.gen'

export function useAttention() {
  const me = useGet<AuthMe>('/auth/me').data
  const signedIn = !!me && (me.mode !== 'internal' || !!me.user)
  return useGet<AttentionView>(signedIn ? '/attention' : null, { poll: 30000 })
}

// These are exactly the rows the Attention page renders, shared by every count.
export function attentionRows(data: AttentionView) {
  return { items: data.items, sources: data.sources.filter(s => s.unknown && !data.items.some(i => i.unknown && (i.key === s.key || i.key.startsWith(s.key + '/')))) }
}

export function attentionState(q: Pick<Read<AttentionView>, 'data' | 'refreshFailed' | 'paused'>) {
  const rows = q.data ? attentionRows(q.data) : null
  return {
    count: rows ? rows.items.filter(i => !i.unknown).length : null,
    unknown: rows ? rows.sources.length + rows.items.filter(i => i.unknown).length : 0,
    known: !!rows && !q.refreshFailed && !q.paused && !rows.sources.length && rows.items.every(i => !i.unknown),
  }
}

export function attentionSummary(count: number | null, unknown: number): string {
  if (count === null) return 'Status not known'
  return [count > 0 ? `${count} to fix` : '', unknown > 0 ? `${unknown} not known` : ''].filter(Boolean).join(' · ') || 'All quiet'
}

export function AttentionBadge() {
  const q = useAttention()
  const state = attentionState(q)
  const words = attentionSummary(state.count, state.unknown)
  return <span className="row small">
    <Link className="attention-badge" to="/attention" aria-label={words}>
      <span className="attention-compact" aria-hidden="true">{words}</span><span className="attention-words">{words}</span>
    </Link>
    <StaleNote q={q} inline />
  </span>
}
