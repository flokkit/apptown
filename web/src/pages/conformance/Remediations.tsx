import { ListRow } from '../../ListRow'
import { ListTable } from '../../ListTable'
import './list-migrations.css'
import { Section } from '../../Section'
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
const MOVING = new Set(['working', 'verifying', 'landing', 'accepting'])

export function JobBar({ counts = {} }: { counts?: Record<string, number> }) {
  const order = ['done', 'verifying', 'landing', 'accepting', 'working', 'stuck', 'queued', 'skipped']
  const total = Math.max(counts.total ?? 0, 1)
  return (
    <span style={{ display: 'inline-flex', width: 180, height: 8, borderRadius: 'var(--radius-sm)', overflow: 'hidden', background: 'var(--panel2)', border: '1px solid var(--border)', flex: '0 0 auto' }}
      title={order.filter((k) => counts[k]).map((k) => `${counts[k]} ${ITEM[k]?.label ?? k}`).join(' · ')}>
      {/* Work under way moves; work waiting its turn is striped, so a bar
          with nothing done yet still shows what is happening. */}
      {order.map((k) => (counts[k] ?? 0) > 0 && <span key={k} className={MOVING.has(k) ? 'bar-moving' : k === 'queued' ? 'bar-waiting' : undefined}
        style={{ width: `${(counts[k] / total) * 100}%`, backgroundColor: ITEM_COLOR[k] }} />)}
    </span>
  )
}

// OccupancyCard: what remediation work is under way, said only when there
// is some (or work is paused), and the worker setting as its own section for
// whoever may change it.
export function OccupancyCard({ occ }: { occ: Occupancy }) {
  // The worker count is shown inside the remediation reads, so a change to
  // it refreshes those as well as /workers.
  const { run, busy, err } = useAction(undefined, { affects: ['/conformance'] })
  const setWorkers = (n: number) => run(() => put('/workers', { kind: 'remediation', count: n }))
  const mayConfigure = useHolds('identity:manage-auth')
  const cooling = !!occ.cooldownUntil && new Date(occ.cooldownUntil).getTime() > Date.now()
  const holding = occ.holding ?? []
  return (
    <>
      {/* Only when something is being worked on (or work is paused): a
          queue on its own is said by each item's own "waiting". */}
      {(holding.length > 0 || cooling) && <Section title="In progress" count={occ.busy || undefined} note={occ.queued > 0 ? `${occ.queued} queued` : undefined}>
        {cooling && <p className="small" style={{ color: 'var(--warn)' }}>Paused until {fmtWhen(occ.cooldownUntil!)}: {occ.cooldownWhy}</p>}
        {holding.length > 0 && <ListTable className="small" label="Worker occupancy" rows={holding} rowKey={h => h.id ?? `${h.projectSlug}/${h.check}`} columns={[
          { key: 'project', label: 'Project', phone: 'name', render: h => <strong>{h.projectSlug}</strong> },
          { key: 'state', label: 'Working on', phone: 'state', className: 'muted', render: h => h.doing ? `on ${h.doing}` : (ITEM[h.state]?.label ?? h.state) },
          { key: 'check', label: 'Check', className: 'muted', render: h => h.check },
        ]} />}
      </Section>}
      {mayConfigure && <Section title="Processing" note="How many remediations work at once.">
        <div className="row" style={{ gap: 'var(--space-2)', alignItems: 'center', padding: '10px 0' }}>
          <span className="small">Workers</span>
          <button className="subtle" aria-label="Fewer workers" disabled={busy || occ.workers <= 0} onClick={() => setWorkers(occ.workers - 1)}>−</button>
          <strong>{occ.workers}</strong>
          <button className="subtle" aria-label="More workers" disabled={busy || occ.workers >= 8} onClick={() => setWorkers(occ.workers + 1)}>+</button>
          {err && <span className="error small">{err}</span>}
        </div>
      </Section>}
    </>
  )
}

export function RemediationsList() {
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
      <Section title="Remediations" count={data.remediations.length || undefined}>
        {data.remediations.length === 0 && <p className="muted small">No remediations yet. Start one with Fix on a check or a project.</p>}
        {data.remediations.map((m) => (
          <ListRow key={m.id} to={`/conformance/remediations/${m.id}`} className="card row conformance-list-row" state={<span className="row-status"><Status word={m.statusLabel} /></span>} identity={<>
            <Link to={`/conformance/remediations/${m.id}`} className="run-link"><strong><span className="muted remediation-number">#{m.number} </span>{m.sentence}</strong></Link>
            <span className="muted small grow" >{m.actor} · {fmtWhen(m.createdAt)} · {m.mode}{m.cost ? ` · $${m.cost.toFixed(3)}` : ''}</span>
            </>} action={<span className="row-action phone-secondary"><JobBar counts={m.counts} /></span>} />
        ))}
      </Section>
    </div>
  )
}
