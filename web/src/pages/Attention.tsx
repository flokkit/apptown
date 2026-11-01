import { Status } from '../Status'
import { Link } from 'react-router-dom'
import { attentionRows, attentionState, attentionSummary, useAttention } from '../attention'
import { ReadStatus, StaleNote } from '../ReadGate'
import { lastSeen, relTime } from '../format'

export const observed = (at: string, stale = false) => !at || at.startsWith('0001-') ? 'not checked yet' : stale ? lastSeen(at) : `checked ${relTime(at) || 'at an unknown time'}`
const groups = ['Hosts and deployments', 'Backups', 'Integrations', 'App checks'] as const
export const attentionKind = (key: string): typeof groups[number] => key.startsWith('protection/') ? 'Backups' : key.startsWith('integrations') ? 'Integrations' : key.startsWith('checks') ? 'App checks' : 'Hosts and deployments'

export default function AttentionPage() {
  const q = useAttention()
  const state = attentionState(q)
  return <div>
    <h1>Attention</h1>
    <ReadStatus q={q} what="conditions that need action" />
    <StaleNote q={q} />
    {q.data && <>
      <p className="status-strip" role="status">{attentionSummary(state.count, state.unknown)}</p>
      {groups.map(kind => {
        const items = attentionRows(q.data!).items.filter(i => attentionKind(i.key) === kind)
        const unknown = attentionRows(q.data!).sources.filter(s => attentionKind(s.key) === kind)
        if (!items.length && !unknown.length) return null
        return <section key={kind} aria-label={kind}><h2>{kind}</h2>
          {items.map(item => <article key={item.key} className="card attention-item">
            <h3><Link to={item.href}>{item.title}</Link></h3>
            <Status word={item.unknown ? 'unknown' : item.statusLabel === 'low' ? 'low' : item.severity === 'advisory' ? 'not checked yet' : 'failed'} staleAt={item.stale && !item.unknown ? item.observedAt : undefined} help={item.detail} />
            <p>{item.detail}</p>
            <p><Link to={item.href}>Open {kind === 'Integrations' ? 'connection settings' : kind === 'App checks' ? 'app checks' : kind === 'Backups' ? 'backup settings' : 'diagnostics'} to resolve this</Link></p>
            <small className="muted">{observed(item.observedAt, item.stale)}</small>
          </article>)}
          {unknown.map(source => <article key={source.key} className="card attention-item">
            <h3><Link to={source.href}>{source.name}</Link></h3>
            <Status word="unknown" />
            <p>{source.reason || 'No usable reading has been recorded yet.'}</p>
            <p>{source.nextStep}</p>
            <small className="muted">{observed(source.observedAt, source.stale)}</small>
          </article>)}
        </section>
      })}
      <details><summary>All checks</summary>
        <ul>{q.data.sources.map(source => <li key={source.key}>
          <Link to={source.href}>{source.name}</Link>: {source.unknown ? 'unknown' : source.stale ? observed(source.observedAt, source.stale) : 'working'}
        </li>)}</ul>
      </details>
    </>}
  </div>
}
