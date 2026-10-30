import { Status, STATUS_LIGHT } from '../Status'
import { Link } from 'react-router-dom'
import { attentionState, useAttention } from '../attention'
import { ReadStatus, StaleNote } from '../ReadGate'
import { fmtStamp } from '../format'

const observed = (at: string) => !at || at.startsWith('0001-') ? 'Not checked yet' : `Checked ${fmtStamp(at)}`

export default function AttentionPage() {
  const q = useAttention()
  const state = attentionState(q)
  return <div>
    <h1>Attention</h1>
    <ReadStatus q={q} what="operational conditions" />
    <StaleNote q={q} />
    {q.data && <>
      <p role="status">{state.count} {state.count === 1 ? 'condition' : 'conditions'}.
        {!state.known && ' Coverage incomplete.'}
        {state.known && state.count === 0 && ''}
      </p>
      {q.data.items.map((item) => <article key={item.key} className="card" style={{ padding: 16, marginBottom: 10 }}>
        <div className="row">
          <Status word={item.statusLabel} tone={STATUS_LIGHT[item.statusLabel]} help={item.detail} />
          {item.stale && <Status word={"unknown"} tone={STATUS_LIGHT["unknown"]} />}
        </div>
        <h2 style={{ margin: '8px 0' }}><Link to={item.href}>{item.title}</Link></h2>
        <p>{item.detail}</p>
        <small className="muted">{observed(item.observedAt)}</small>
      </article>)}
      <details open={!state.known}>
        <summary>Observation coverage</summary>
        <ul>{q.data.sources.map((source) => <li key={source.key}>
          <strong>{source.name}</strong>: {source.unknown ? 'unavailable' : source.stale ? 'unknown' : 'current'} · {observed(source.observedAt)}
        </li>)}</ul>
      </details>
    </>}
  </div>
}
