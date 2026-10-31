import { useState } from 'react'
import { Link } from 'react-router-dom'
import type { ConformanceView, CheckDefinition } from '../../wire.gen'
import { type Verdict, stateOf, laneOf, StateChip, SevDot, HowJudged, useStartRemediation } from './parts'

// --- Checks (the catalog) -------------------------------------------------------------------------

function CheckCard({ def, vs, view, base }: { def?: CheckDefinition; vs: Verdict[]; view: ConformanceView; base: string }) {
  const remediation = useStartRemediation()
  const findings = vs.filter((v) => laneOf(v, view.adjudications) === 'findings').length
  const act = vs.filter((v) => ['findings', 'flagged', 'broken'].includes(laneOf(v, view.adjudications))).length
  const states = [...new Set(vs.map(v => v.state))]
  const [open, setOpen] = useState(false)
  const sorted = [...vs].sort((a, b) => stateOf(a.state).rank - stateOf(b.state).rank || a.projectSlug.localeCompare(b.projectSlug))
  return (
    <div className={`card${vs.some(v => laneOf(v, view.adjudications) === 'broken') ? ' tone-bad' : act > 0 ? ' tone-warn' : ''}`} style={{ padding: 0, marginBottom: 0, overflow: 'hidden' }}>
      <div className="check-heading"><button className="subtle" aria-expanded={open} onClick={() => setOpen(!open)}>
        <span className="row check-title"><SevDot sev={def?.severity ?? vs[0]?.severity ?? 'note'} /><span>{def?.title || def?.check || vs[0]?.check}</span></span>
        </button><span className="check-states small">{states.map(state => {
          const results = vs.filter(v => v.state === state)
          return <span key={state}><StateChip label={results[0].statusLabel} finding={state === 'does-not-conform'} findings={results.length} /> · {results.length} {results.length === 1 ? 'app' : 'apps'}</span>
        })}</span>
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
              <StateChip finding={v.state === 'does-not-conform'} label={v.statusLabel} help={v.detail} />
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
  const [open, setOpen] = useState(false)
  const findings = verdicts.filter(v => laneOf(v, view.adjudications) === 'findings').length
  return <div className="card" style={{ padding: 0 }}>
    <div className="check-heading"><button className="subtle" aria-expanded={open} onClick={() => setOpen(!open)}>
      <span className="check-title"><SevDot sev={view.checkDefs[versions[0][0]]?.severity ?? 'note'} /><span>{view.checkDefs[versions[0][0]]?.title || 'Check'}</span></span>
      </button><span className="check-states small"><StateChip label={label} finding={label === 'failed' && !verdicts.some(v => v.state === 'error')} findings={verdicts.filter(v => v.state === 'does-not-conform').length} /> · {new Set(verdicts.map(v => v.projectSlug)).size} {new Set(verdicts.map(v => v.projectSlug)).size === 1 ? 'app' : 'apps'}</span>
    </div>
<p className="muted small" style={{ padding: '0 14px' }}>{findings > 0 ? `${findings} problems. Open to choose a version to fix.` : 'Open for results by version.'}</p>
    {open && versions.map(([key, vs]) => <div key={key}><p>Version {view.checkDefs[key]?.version}</p><CheckCard def={view.checkDefs[key]} vs={vs} view={view} base={base} /></div>)}
  </div>
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
