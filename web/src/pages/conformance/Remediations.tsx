import { PhoneDetails } from '../../GlanceRow'
import { useNarrow } from '../../chrome'
import { Status } from '../../Status'
import { Link } from 'react-router-dom'
import { useHolds } from '../../instancePages'
import { put } from '../../api'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { Occupancy, RemediationList } from '../../wire.gen'
import { fmtWhen } from '../../format'
import { ITEM, ITEM_COLOR } from './parts'

// --- Remediations ----------------------------------------------------------------------------------

// The server omits counts when a job has none yet; that reads as an empty bar.
export function JobBar({ counts = {} }: { counts?: Record<string, number> }) {
  const order = ['done', 'verifying', 'landing', 'accepting', 'working', 'stuck', 'queued', 'skipped']
  const total = Math.max(counts.total ?? 0, 1)
  return (
    <span style={{ display: 'inline-flex', width: 180, height: 8, borderRadius: 'var(--radius-sm)', overflow: 'hidden', background: 'var(--panel2)', border: '1px solid var(--border)', flex: '0 0 auto' }}
      title={order.filter((k) => counts[k]).map((k) => `${counts[k]} ${ITEM[k]?.label ?? k}`).join(' · ')}>
      {order.map((k) => (counts[k] ?? 0) > 0 && <span key={k} style={{ width: `${(counts[k] / total) * 100}%`, background: ITEM_COLOR[k] }} />)}
    </span>
  )
}

export function OccupancyCard({ occ }: { occ: Occupancy }) {
  // The worker count is shown inside the remediation reads, so a change to
  // it refreshes those as well as /workers.
  const narrow = useNarrow()
  const { run, busy, err } = useAction(undefined, { affects: ['/conformance'] })
  const setWorkers = (n: number) => run(() => put('/workers', { kind: 'remediation', count: n }))
  const mayConfigure = useHolds('identity:manage-auth')
  const cooling = !!occ.cooldownUntil && new Date(occ.cooldownUntil).getTime() > Date.now()
  return (
    <div className={`card${cooling ? ' tone-warn' : ''}`} style={{ marginBottom: 'var(--space-4)', padding: '10px 14px' }}>
      {narrow && <p>{cooling ? 'Work is paused.' : occ.busy ? `${occ.busy} updates in progress` : 'No updates in progress.'}</p>}
      <PhoneDetails>
      <div className="row" style={{ justifyContent: 'space-between', gap: 'var(--space-3)', alignItems: 'center' }}>
        <div className="small">
          <strong>{occ.busy} updates in progress</strong>
          {occ.queued > 0 && <span className="muted"> · {occ.queued} queued</span>}
          {(occ.holding ?? []).length > 0 && (
            <div className="muted" style={{ marginTop: 2 }}>
              {(occ.holding ?? []).map((h) => <span key={h.id} style={{ marginRight: 'var(--space-3)' }}>{h.projectSlug} <span className="mono">{h.check}</span> · {h.doing ? `on ${h.doing}` : (ITEM[h.state]?.label ?? h.state)}</span>)}
            </div>
          )}
          {cooling && <div style={{ marginTop: 2, color: 'var(--warn)' }}>Paused until {fmtWhen(occ.cooldownUntil!)}: {occ.cooldownWhy}</div>}
        </div>
        {mayConfigure && <details><summary>Processing settings</summary><span className="row" style={{ gap: 'var(--space-2)', alignItems: 'center' }}>
          <span className="muted small">workers</span>
          <button className="subtle" disabled={busy || occ.workers <= 0} onClick={() => setWorkers(occ.workers - 1)}>−</button>
          <strong>{occ.workers}</strong>
          <button className="subtle" disabled={busy || occ.workers >= 8} onClick={() => setWorkers(occ.workers + 1)}>+</button>
        </span></details>}
      </div>
      </PhoneDetails>
      {err && <div className="error small">{err}</div>}
    </div>
  )
}

export function RemediationsList() {
  const narrow = useNarrow()
  const q = useGet<RemediationList>('/conformance/remediations', { poll: 5000 })
  const got = q.data
  if (!got) return <ReadStatus q={q} what="the remediations" className="small" />
  // The list may be empty; it must never be null here (AppTown's own
  // first browser error: "Cannot read properties of null (reading 'length')").
  const data = { remediations: got.remediations ?? [], occupancy: got.occupancy }
  return (
    <div>
      <StaleNote q={q} />
      <OccupancyCard occ={data.occupancy} />
      {data.remediations.length === 0 && <div className="muted" style={{ padding: '30px 0', textAlign: 'center' }}>No remediations. Choose a check, project or Fix all findings.</div>}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--space-2)' }}>
        {data.remediations.map((m) => narrow ? <Link key={m.id} to={`/conformance/remediations/${m.id}`} className="card" style={{ display: 'block', color: 'inherit', textDecoration: 'none' }}>
          <div className="host-row"><strong>Fix #{m.number}</strong><Status word={m.statusLabel} help={m.sentence} /><span className="muted small">{m.counts?.total ?? 0} {(m.counts?.total ?? 0) === 1 ? 'task' : 'tasks'}</span></div>
        </Link> : (
          <Link key={m.id} to={`/conformance/remediations/${m.id}`} className={`card${m.status === 'running' ? ' tone-busy' : m.status === 'completed_with_stuck' ? ' tone-bad' : ''}`} style={{ marginBottom: 0, textDecoration: 'none', color: 'inherit', display: 'block' }}>
            <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center', gap: 'var(--space-3)' }}>
              <div style={{ minWidth: 0 }}>
                <div className="row" style={{ gap: 'var(--space-2)', alignItems: 'center' }}>
                  <strong>#{m.number}</strong>
                  <Status word={m.statusLabel} help={m.status.replaceAll('_', ' ')} />
                  <span className="small">{m.sentence}</span>
                </div>
                <div className="muted small" style={{ marginTop: 2 }}>{m.actor} · {fmtWhen(m.createdAt)} · {m.mode}{m.cost ? ` · $${m.cost.toFixed(3)}` : ''}</div>
              </div>
              <JobBar counts={m.counts} />
            </div>
          </Link>
        ))}
      </div>
    </div>
  )
}
