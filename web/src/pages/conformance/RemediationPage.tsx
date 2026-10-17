import { Link } from 'react-router-dom'
import { post } from '../../api'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { Item, RemediationDetail } from '../../wire.gen'
import { fmtWhen } from '../../format'
import { ITEM, JOB } from './parts'
import { JobBar, OccupancyCard } from './Remediations'

export function RemediationPage({ id }: { id: string }) {
  const q = useGet<RemediationDetail>(`/conformance/remediations/${id}`, { poll: 5000 })
  const data = q.data
  const { run, busy, err } = useAction()
  const act = (path: string, body?: unknown) => run(() => post(path, body ?? {}))
  if (!data) return <ReadStatus q={q} what="the remediation" className="small" />
  const m = data.remediation
  const byProject: Record<string, Item[]> = {}
  for (const it of data.items) (byProject[it.projectSlug] ??= []).push(it)
  return (
    <div>
      <StaleNote q={q} />
      <Link to="/conformance/remediations" className="subtle small">← all remediations</Link>
      <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center', gap: 'var(--space-3)', margin: '8px 0 12px' }}>
        <div>
          <div className="row" style={{ gap: 'var(--space-2)', alignItems: 'center' }}>
            <h2 style={{ margin: 0, textTransform: 'none', letterSpacing: 0 }}>Remediation #{m.number}</h2>
            <span className={`chip ${JOB[m.status]?.tone ?? ''}`}>{JOB[m.status]?.label ?? m.status}</span>
            <JobBar counts={m.counts} />
          </div>
          <div className="muted small mt-1">{m.sentence} · {m.actor} · {fmtWhen(m.createdAt)} · {m.mode}{m.cost ? ` · $${m.cost.toFixed(3)} so far` : ''}</div>
        </div>
        <span className="row" style={{ gap: 'var(--space-2)' }}>
          {m.status === 'running' && <button disabled={busy} onClick={() => act(`/conformance/remediations/${m.id}/pause`)}>Pause</button>}
          {m.status === 'paused' && <button disabled={busy} onClick={() => act(`/conformance/remediations/${m.id}/resume`)}>Resume</button>}
          {(m.status === 'running' || m.status === 'paused') && <button disabled={busy} onClick={() => act(`/conformance/remediations/${m.id}/cancel`)}>Cancel</button>}
        </span>
      </div>
      {err && <div className="error small mb-3">{err}</div>}
      <OccupancyCard occ={data.occupancy} />
      {Object.entries(byProject).map(([slug, items]) => (
        <div key={slug} className="card" style={{ padding: 0, marginBottom: 'var(--space-3)', overflow: 'hidden' }}>
          <div style={{ padding: '8px 14px', borderBottom: '1px solid var(--border)' }}>
            <Link to={`/conformance/projects/${slug}`} style={{ fontWeight: 600, textDecoration: 'none', color: 'inherit' }}>{slug}</Link>
          </div>
          {items.map((it) => {
            const st = ITEM[it.state] ?? { label: it.state, tone: '' }
            const tries = data.attempts[it.id ?? ''] ?? []
            return (
              <div key={it.id} style={{ padding: '8px 14px', borderTop: '1px solid var(--border)' }}>
                <div className="row" style={{ justifyContent: 'space-between', gap: 'var(--space-3)' }}>
                  <span className="row" style={{ gap: 'var(--space-2)', alignItems: 'center', minWidth: 0 }}>
                    <span className={`chip ${st.tone}`}>{st.label}</span>
                    <span className="mono" style={{ fontSize: 'var(--fs-sm)' }}>{it.check}</span>
                    {it.changeId ? <Link to={`/changes/${it.changeId}`} className="subtle small">{it.changeNumber ? `change ${it.projectSlug}#${it.changeNumber}` : 'the change'}</Link> : null}
                    {it.attempt > 1 && <span className="muted small">attempt {it.attempt}</span>}
                    {it.doing && <span className="muted small">· on {it.doing}</span>}
                  </span>
                  <span className="row" style={{ gap: 'var(--space-2)' }}>
                    {it.state === 'stuck' && <button disabled={busy} onClick={() => { const n = window.prompt('Retry with a note for the agent (optional):', ''); if (n !== null) act(`/conformance/remediations/${m.id}/items/${it.id}/retry`, { note: n }) }}>Retry</button>}
                    {(it.state === 'stuck' || it.state === 'queued') && <button className="subtle" disabled={busy} onClick={() => { const r = window.prompt('Skip it — why?', ''); if (r) act(`/conformance/remediations/${m.id}/items/${it.id}/skip`, { reason: r }) }}>Skip</button>}
                  </span>
                </div>
                {it.reason && <div className="small" style={{ marginTop: 'var(--space-1)', color: it.state === 'stuck' ? 'var(--bad)' : 'var(--muted)', whiteSpace: 'pre-wrap' }}>{it.reason}</div>}
                {it.state === 'accepting' && it.changeId ? <div className="small mt-1">The fix is ready: <Link to={`/changes/${it.changeId}`}>accept it on the change page</Link> and it lands itself.</div> : null}
                {tries.length > 1 && <div className="muted small mt-1">{tries.map((t) => `#${t.n}: ${t.outcome || 'in progress'}${t.reason ? ` (${t.reason.slice(0, 80)})` : ''}`).join(' · ')}</div>}
              </div>
            )
          })}
        </div>
      ))}
    </div>
  )
}
