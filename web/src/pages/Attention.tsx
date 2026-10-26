import { Link } from 'react-router-dom'
import { attentionState, useAttention } from '../attention'
import { ReadStatus, StaleNote } from '../ReadGate'
import { fmtStamp } from '../format'

const observed = (at: string) => !at || at.startsWith('0001-') ? 'Not yet observed' : `Observed ${fmtStamp(at)}`

export default function AttentionPage() {
  const q = useAttention()
  const state = attentionState(q)
  return <div>
    <h1>Attention</h1>
    <p>Current operational conditions in the resources you can read. Open a condition to inspect its evidence and available actions.</p>
    <ReadStatus q={q} what="operational conditions" />
    <StaleNote q={q} />
    {q.data && <>
      <p role="status">{state.count} {state.count === 1 ? 'condition' : 'conditions'}.
        {!state.known && ' Coverage is incomplete or out of date; this is not an all-clear.'}
        {state.known && state.count === 0 && ' No current conditions need attention.'}
      </p>
      {q.data.items.map((item) => <article key={item.key} className="card" style={{ padding: 16, marginBottom: 10 }}>
        <div className="row">
          <span className={`chip ${item.severity === 'advisory' ? '' : 'warn'}`}>{item.severity === 'advisory' ? 'advisory' : 'needs attention'}</span>
          {item.stale && <span className="chip out-of-date">out of date</span>}
        </div>
        <h2 style={{ margin: '8px 0' }}><Link to={item.href}>{item.title}</Link></h2>
        <p>{item.detail}</p>
        <small className="muted">{observed(item.observedAt)}</small>
      </article>)}
      <details open={!state.known}>
        <summary>Observation coverage</summary>
        <ul>{q.data.sources.map((source) => <li key={source.key}>
          <strong>{source.name}</strong>: {source.unknown ? 'unavailable' : source.stale ? 'out of date' : 'current'} · {observed(source.observedAt)}
        </li>)}</ul>
      </details>
    </>}
  </div>
}
