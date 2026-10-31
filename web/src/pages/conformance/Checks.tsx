import { useNarrow } from '../../chrome'
import { Status, STATUS_LIGHT } from '../../Status'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import type { ConformanceView, CheckDefinition } from '../../wire.gen'
import { type Verdict, stateOf, laneOf, StateChip, SevDot, HowJudged, useStartRemediation } from './parts'

// --- Checks (the catalog) -------------------------------------------------------------------------

function CheckCard({ def, vs, view, base }: { def?: CheckDefinition; vs: Verdict[]; view: ConformanceView; base: string }) {
  const narrow = useNarrow()
  const remediation = useStartRemediation()
  const findings = vs.filter((v) => laneOf(v, view.adjudications) === 'findings').length
  const act = vs.filter((v) => ['findings', 'flagged', 'broken'].includes(laneOf(v, view.adjudications))).length
  const labels = [...new Set(vs.map(v => v.statusLabel))]
  const [open, setOpen] = useState(!narrow && act > 0)
  const sorted = [...vs].sort((a, b) => stateOf(a.state).rank - stateOf(b.state).rank || a.projectSlug.localeCompare(b.projectSlug))
  return (
    <div className={`card${act > 0 ? ' tone-bad' : ''}`} style={{ padding: 0, marginBottom: 0, overflow: 'hidden' }}>
      <div style={{ padding: '10px 14px', cursor: 'pointer' }} onClick={() => setOpen(!open)}>
        <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center', gap: 'var(--space-3)' }}>
          <span className="row check-title" style={{ gap: 'var(--space-2)', alignItems: 'center', minWidth: 0 }}>
            <SevDot sev={def?.severity ?? vs[0]?.severity ?? 'note'} />
            <span style={{ fontSize: 'var(--fs-sm)' }}>{def?.title || def?.check || vs[0]?.check}</span>

          </span>
          <span className="small" style={{ flex: '0 0 auto' }}>
            {(narrow ? [sorted[0].statusLabel] : labels).map(label => <span key={label} style={{ display: 'inline-flex', alignItems: 'center', gap: 4, marginRight: 8 }}>{vs.filter(v => v.statusLabel === label).length} <Status word={label} tone={STATUS_LIGHT[label]} /></span>)}
          </span>
        </div>

      </div>
      {open && (
        <div style={{ borderTop: '1px solid var(--border)' }}>
          <details><summary>Check details</summary><HowJudged def={def} /></details>
          {view.lens.kind === 'current' && findings > 0 && def && (
            <div className="row" style={{ padding: '8px 14px', gap: 'var(--space-3)', borderTop: '1px solid var(--border)' }}>
              <button disabled={remediation.busy} onClick={() => remediation.start('check', def.key)}>Fix everywhere · {findings} project{findings === 1 ? '' : 's'}</button>
              <span className="muted small">Each change needs approval.</span>
              {remediation.err && <span className="error small">{remediation.err}</span>}
            </div>
          )}
          {sorted.map((v) => (
            <div key={v.projectSlug} className="row" style={{ justifyContent: 'space-between', gap: 'var(--space-3)', padding: '7px 14px', borderTop: '1px solid var(--border)' }}>
              <Link to={`${base}/projects/${v.projectSlug}`} style={{ fontWeight: 600, textDecoration: 'none', color: 'inherit' }}>{v.projectSlug}</Link>
              <StateChip label={v.statusLabel} help={v.detail} />
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
      {checks.map(([key, vs]) => <CheckCard key={key} def={view.checkDefs[key]} vs={vs} view={view} base={base} />)}
    </div>
  )
}
