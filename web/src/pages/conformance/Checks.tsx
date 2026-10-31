import { Status, STATUS_LIGHT } from '../../Status'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import type { ConformanceView, CheckDefinition } from '../../wire.gen'
import { type Verdict, stateOf, laneOf, StateChip, SevDot, HowJudged, useStartRemediation } from './parts'

// --- Checks (the catalog) -------------------------------------------------------------------------

function CheckCard({ def, vs, view, base }: { def?: CheckDefinition; vs: Verdict[]; view: ConformanceView; base: string }) {
  const remediation = useStartRemediation()
  const findings = vs.filter((v) => laneOf(v, view.adjudications) === 'findings').length
  const act = vs.filter((v) => ['findings', 'flagged', 'broken'].includes(laneOf(v, view.adjudications))).length
  const labels = [...new Set(vs.map(v => v.statusLabel))]
  const [open, setOpen] = useState(false)
  const sorted = [...vs].sort((a, b) => stateOf(a.state).rank - stateOf(b.state).rank || a.projectSlug.localeCompare(b.projectSlug))
  return (
    <div className={`card${act > 0 ? ' tone-bad' : ''}`} style={{ padding: 0, marginBottom: 0, overflow: 'hidden' }}>
      <div style={{ padding: '10px 14px', cursor: 'pointer' }} role="button" tabIndex={0} aria-expanded={open} onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setOpen(v => !v) } }} onClick={() => setOpen(!open)}>
        <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center', gap: 'var(--space-3)' }}>
          <span className="row check-title" style={{ gap: 'var(--space-2)', alignItems: 'center', minWidth: 0 }}>
            <SevDot sev={def?.severity ?? vs[0]?.severity ?? 'note'} />
            <span style={{ fontSize: 'var(--fs-sm)' }}>{def?.title || def?.check || vs[0]?.check}</span>

          </span>
          <span className="small check-counts" style={{ flex: '0 0 auto' }}>
            {labels.map(label => <span key={label} style={{ display: 'inline-flex', alignItems: 'center', gap: 4, marginRight: 8 }}>{vs.filter(v => v.statusLabel === label).length} apps <Status word={label} tone={STATUS_LIGHT[label]} /></span>)}
          </span>
          <span className="check-phone small"><Status word={labels.includes('failed') ? 'failed' : labels.includes('unknown') ? 'unknown' : labels[0]} tone={STATUS_LIGHT[labels.includes('failed') ? 'failed' : labels.includes('unknown') ? 'unknown' : labels[0]]} /> · {vs.length} apps</span>
        </div>

      </div>
          {view.lens.kind === 'current' && findings > 0 && def && (
            <div className="row" style={{ padding: '8px 14px', gap: 'var(--space-3)', borderTop: '1px solid var(--border)' }}>
              <button disabled={remediation.busy} onClick={() => remediation.start('check', def.key)}>Fix everywhere · {findings} project{findings === 1 ? '' : 's'}</button>
              <span className="muted small">Each change needs approval.</span>
              {remediation.err && <span className="error small">{remediation.err}</span>}
            </div>
          )}
      {open && (
        <div style={{ borderTop: '1px solid var(--border)' }}>
          <details><summary>Check details</summary><HowJudged def={def} /></details>
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

// A group still answers whether any version needs attention before it opens.
function CheckVersions({ versions, view, base }: { versions: [string, Verdict[]][]; view: ConformanceView; base: string }) {
  const verdicts = versions.flatMap(v => v[1])
  const labels = new Set(verdicts.map(v => v.statusLabel))
  const label = (['failed', 'unknown', 'excepted', 'passed', 'not applicable'] as const).find(s => labels.has(s)) ?? 'unknown'
  return <details className="card"><summary>
    {view.checkDefs[versions[0][0]]?.title || 'Check'} · <Status word={label} tone={STATUS_LIGHT[label]} /> · {new Set(verdicts.map(v => v.projectSlug)).size} apps
  </summary>{versions.map(([key, vs]) => <div key={key}><p>Version {view.checkDefs[key]?.version}</p><CheckCard def={view.checkDefs[key]} vs={vs} view={view} base={base} /></div>)}</details>
}

export function ChecksList({ view, base }: { view: ConformanceView; base: string }) {
  const [limit, setLimit] = useState(10)
  const grouped = new Map<string, [string, Verdict[]][]>()
  for (const entry of Object.entries(view.byCheck)) {
    const d = view.checkDefs[entry[0]]
    const identity = d?.block && d.check ? `${d.block}/${d.check}` : entry[0]
    grouped.set(identity, [...(grouped.get(identity) ?? []), entry])
  }
  const checks = [...grouped.entries()]
  const act = (vs: Verdict[]) => vs.filter((v) => ['findings', 'flagged', 'broken'].includes(laneOf(v, view.adjudications))).length
  checks.sort((a, b) => act(b[1].flatMap(x => x[1])) - act(a[1].flatMap(x => x[1])) || a[0].localeCompare(b[0]))
  if (checks.length === 0) return <div className="muted" style={{ padding: '40px 0', textAlign: 'center' }}>No checks recorded.</div>
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--space-3)' }}>
      {checks.slice(0, limit).map(([key, versions]) => versions.length === 1
        ? <CheckCard key={key} def={view.checkDefs[versions[0][0]]} vs={versions[0][1]} view={view} base={base} />
        : <CheckVersions key={key} versions={versions} view={view} base={base} />)}
      {checks.length > limit && <button onClick={() => setLimit(n => n + 10)}>Show more checks</button>}
    </div>
  )
}
