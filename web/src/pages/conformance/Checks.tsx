import { useState } from 'react'
import { Link } from 'react-router-dom'
import type { ConformanceView, CheckDefinition } from '../../wire.gen'
import { type Verdict, stateOf, laneOf, StateChip, SevDot, HowJudged, useStartRemediation } from './parts'

// --- Checks (the catalog) -------------------------------------------------------------------------

function CheckCard({ def, vs, view, base }: { def?: CheckDefinition; vs: Verdict[]; view: ConformanceView; base: string }) {
  const remediation = useStartRemediation()
  const findings = vs.filter((v) => laneOf(v, view.adjudications) === 'findings').length
  const act = vs.filter((v) => ['findings', 'flagged', 'broken'].includes(laneOf(v, view.adjudications))).length
  const unk = vs.filter((v) => laneOf(v, view.adjudications) === 'unassessed').length
  const na = vs.filter((v) => v.state === 'not-applicable').length
  const [open, setOpen] = useState(act > 0)
  const sorted = [...vs].sort((a, b) => stateOf(a.state).rank - stateOf(b.state).rank || a.projectSlug.localeCompare(b.projectSlug))
  return (
    <div className={`card${act > 0 ? ' tone-bad' : ''}`} style={{ padding: 0, marginBottom: 0, overflow: 'hidden' }}>
      <div style={{ padding: '10px 14px', cursor: 'pointer' }} onClick={() => setOpen(!open)}>
        <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center', gap: 'var(--space-3)' }}>
          <span className="row" style={{ gap: 'var(--space-2)', alignItems: 'center', minWidth: 0 }}>
            <SevDot sev={def?.severity ?? vs[0]?.severity ?? 'note'} />
            <span style={{ fontSize: 'var(--fs-sm)' }}>{def?.title || def?.check || vs[0]?.check}</span>
            <span className="muted small">{def?.judgedWhere}</span>
          </span>
          <span className="small" style={{ flex: '0 0 auto' }}>
            {act > 0 && <span style={{ color: 'var(--bad)', fontWeight: 600 }}>{act} to look at</span>}
            {act === 0 && unk === 0 && na === 0 && <span style={{ color: 'var(--ok)', fontWeight: 600 }}>✓ all conform</span>}
            {unk > 0 && <span className="muted">{act > 0 ? ' · ' : ''}{unk} not judged</span>}
            {na > 0 && <span className="muted">{act > 0 || unk > 0 ? ' · ' : ''}{na} n/a here</span>}
          </span>
        </div>
        {def?.expects && <div className="muted" style={{ fontSize: 'var(--fs-xs)', marginTop: 5, maxWidth: 760 }}>{def.expects}</div>}
      </div>
      {open && (
        <div style={{ borderTop: '1px solid var(--border)' }}>
          <HowJudged def={def} />
          {view.lens.kind === 'current' && findings > 0 && def && (
            <div className="row" style={{ padding: '8px 14px', gap: 'var(--space-3)', borderTop: '1px solid var(--border)' }}>
              <button disabled={remediation.busy} onClick={() => remediation.start('check', def.key)}>Fix everywhere · {findings} project{findings === 1 ? '' : 's'}</button>
              <span className="muted small">Opens one change per project at a time; each waits for your approval.</span>
              {remediation.err && <span className="error small">{remediation.err}</span>}
            </div>
          )}
          {sorted.map((v) => (
            <div key={v.projectSlug} className="row" style={{ justifyContent: 'space-between', gap: 'var(--space-3)', padding: '7px 14px', borderTop: '1px solid var(--border)' }}>
              <Link to={`${base}/projects/${v.projectSlug}`} style={{ fontWeight: 600, textDecoration: 'none', color: 'inherit' }}>{v.projectSlug}</Link>
              <StateChip s={v.state} />
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

export function ChecksList({ view, base }: { view: ConformanceView; base: string }) {
  const checks = Object.entries(view.byCheck)
  const act = (vs: Verdict[]) => vs.filter((v) => ['findings', 'flagged', 'broken'].includes(laneOf(v, view.adjudications))).length
  checks.sort((a, b) => act(b[1]) - act(a[1]) || a[0].localeCompare(b[0]))
  if (checks.length === 0) return <div className="muted" style={{ padding: '40px 0', textAlign: 'center' }}>No checks recorded.</div>
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--space-3)' }}>
      <div className="muted small">Every check the fleet has adopted: what it requires, how and where it's judged, and each project's verdict.</div>
      {checks.map(([key, vs]) => <CheckCard key={key} def={view.checkDefs[key]} vs={vs} view={view} base={base} />)}
    </div>
  )
}
