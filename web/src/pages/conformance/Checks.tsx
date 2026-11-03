import { useId, useState, type ReactNode } from 'react'
import { Section, Group } from '../../Section'
import { Status } from '../../Status'
import { Link } from 'react-router-dom'
import type { ConformanceView, CheckDefinition } from '../../wire.gen'
import { type Verdict, coveringChange, BeingFixed, fixableFindings, stateOf, laneOf, StateChip, HowJudged, useStartRemediation } from './parts'

// --- Checks (the catalog) -------------------------------------------------------------------------

// tally: a check's results in one line, findings first:
// "1 finding · 2 passed · 1 not applicable".
function tally(vs: Verdict[]): string {
  const findings = vs.filter(v => v.lane === 'findings').length
  const rest = new Map<string, number>()
  for (const v of vs) if (v.lane !== 'findings') rest.set(v.statusLabel, (rest.get(v.statusLabel) ?? 0) + 1)
  return [findings > 0 ? `${findings} ${findings === 1 ? 'finding' : 'findings'}` : '', ...[...rest].map(([label, n]) => `${n} ${label}`)].filter(Boolean).join(' · ')
}

// worst: the label a check's row leads with, the most urgent of its results.
const worst = (vs: Verdict[]) => {
  const labels = new Set(vs.map(v => v.statusLabel))
  return (['failed', 'unknown', 'excepted', 'passed', 'not applicable'] as const).find(s => labels.has(s)) ?? 'unknown'
}

// CheckRow: one row per check — status, title, tally, and Fix everywhere
// when it has findings. Selecting the row opens each project's result.
function CheckRow({ title, vs, view, base, fixKey, children }: { title: string; vs: Verdict[]; view: ConformanceView; base: string; fixKey?: string; children?: ReactNode }) {
  const remediation = useStartRemediation()
  const findings = vs.filter((v) => laneOf(v) === 'findings').length
  const [open, setOpen] = useState(false)
  const id = useId()
  return (
    <>
      <div className="card row check-row" style={{ display: 'flex' }}>
        <button className="row subtle check-open" aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)}>
          <span className="row-status">{findings > 0 ? <span className="finding-count">{findings} {findings === 1 ? 'finding' : 'findings'}</span> : <Status word={worst(vs)} />}</span>
          <strong>{title}</strong>
          <span className="muted small">{tally(vs)}</span>
        </button>
        {view.lens.kind === 'current' && fixableFindings(view, vs) > 0 && fixKey && (
          <span className="row-action"><button disabled={remediation.busy} title="One change per project, each waiting for approval" onClick={() => remediation.start('check', fixKey)}>Fix everywhere</button></span>
        )}
      </div>
      {remediation.err && <p className="error small">{remediation.err}</p>}
      {open && <div id={id} className="check-results">{children ?? <Results vs={vs} base={base} view={view} />}</div>}
    </>
  )
}

function Results({ vs, base, def, view }: { vs: Verdict[]; base: string; view: ConformanceView; def?: CheckDefinition }) {
  const sorted = [...vs].sort((a, b) => stateOf(a.state).rank - stateOf(b.state).rank || a.projectSlug.localeCompare(b.projectSlug))
  return <>
    {sorted.map((v) => (
      <div key={v.projectSlug}><Link to={`${base}/projects/${v.projectSlug}`} className="row small" style={{ display: 'flex', gap: 'var(--space-3)', padding: '6px 0 6px 16px' }}>
        <span className="row-status"><StateChip finding={v.lane === 'findings'} label={v.statusLabel} /></span>
        <strong>{v.projectSlug}</strong>
        {v.detail && <span className="muted">{v.detail}</span>}
      </Link>{coveringChange(view, v) && <BeingFixed change={coveringChange(view, v)!} />}</div>
    ))}
    {def && <details className="small" style={{ padding: '4px 0 8px 16px' }}><summary>How this check judges</summary><HowJudged def={def} /></details>}
  </>
}

function CheckCard({ def, vs, view, base }: { def?: CheckDefinition; vs: Verdict[]; view: ConformanceView; base: string }) {
  return <CheckRow title={def?.title || def?.check || vs[0]?.check || 'Check'} vs={vs} view={view} base={base} fixKey={def?.key}>
    <Results vs={vs} base={base} view={view} def={def} />
  </CheckRow>
}

// A check with several versions is one row; opening it lists each version's
// results, each with its own Fix everywhere.
function CheckVersions({ versions, view, base }: { versions: [string, Verdict[]][]; view: ConformanceView; base: string }) {
  const verdicts = versions.flatMap(v => v[1])
  return <CheckRow title={view.checkDefs[versions[0][0]]?.title || 'Check'} vs={verdicts} view={view} base={base}>
    {versions.map(([key, vs]) => <Group key={key} label={`Version ${view.checkDefs[key]?.version ?? ''}`}>
      <CheckCard def={view.checkDefs[key]} vs={vs} view={view} base={base} />
    </Group>)}
  </CheckRow>
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
  const act = (vs: Verdict[]) => vs.filter((v) => ['findings', 'flagged', 'broken'].includes(laneOf(v))).length
  checks.sort((a, b) => act(b[1].flatMap(x => x[1])) - act(a[1].flatMap(x => x[1])) || a[0].localeCompare(b[0]))
  return (
    <Section title="Checks" count={checks.length || undefined}>
      {checks.length === 0 && <p className="muted small">No checks recorded.</p>}
      {checks.slice(0, limit).map(([key, versions]) => versions.length === 1
        ? <CheckCard key={key} def={view.checkDefs[versions[0][0]]} vs={versions[0][1]} view={view} base={base} />
        : <CheckVersions key={key} versions={versions} view={view} base={base} />)}
      {checks.length > limit && <p><button onClick={() => setLimit(n => n + 10)}>Show more checks</button></p>}
    </Section>
  )
}
