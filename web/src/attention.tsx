import { Link } from 'react-router-dom'
import { useGet, type Read } from './data'
import { ReadStatus, StaleNote } from './ReadGate'
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
  return <span className="row small">
    <Link to="/attention" aria-label={`Attention: ${state.count ?? 'unknown'} conditions${state.known ? '' : ', coverage incomplete'}`}>
      Attention {state.count ?? '…'}{!state.known && ' · ?'}
    </Link>
    <StaleNote q={q} inline />
  </span>
}

export function OperationsSummary({ q }: { q: Read<AttentionView> }) {
  const state = attentionState(q)
  return <section className="card" style={{ padding: 16, marginBottom: 16 }}>
    <h2 style={{ marginTop: 0 }}>Operations</h2>
    <ReadStatus q={q} what="operational conditions" />
    <StaleNote q={q} />
    {q.data && <p>
      <Link to="/attention">{state.count} {state.count === 1 ? 'condition needs' : 'conditions need'} attention</Link>.
      {!state.known && ' Coverage incomplete.'}
      {state.known && state.count === 0 && ''}
    </p>}
  </section>
}
