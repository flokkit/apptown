import { useHolds } from '../../instancePages'
import { ListTable } from '../../ListTable'
import './list-migrations.css'
import { Fragment, useState } from 'react'
import { Section } from '../../Section'
import { Status } from '../../Status'
import { Link, useParams } from 'react-router-dom'
import type { ConformanceView } from '../../wire.gen'
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

// FixEverywhere starts a remediation over one check version's findings.
function FixEverywhere({ fixKey }: { fixKey: string }) {
  const remediation = useStartRemediation()
  if (!useHolds('conformance:remediate')) return null
  return <>
    <button disabled={remediation.busy} title="One change per project, each waiting for approval" onClick={() => remediation.start('check', fixKey)}>Fix everywhere</button>
    {remediation.err && <span className="error small">{remediation.err}</span>}
  </>
}

const checkStatus = (vs: Verdict[]) => {
  const findings = vs.filter((v) => laneOf(v) === 'findings').length
  return findings > 0 ? <Status word="needs review" reason={`${findings} ${findings === 1 ? 'finding' : 'findings'}`} /> : <Status word={worst(vs)} />
}

// Results: one row per project's result for one check version; the row
// opens the project's conformance page.
function Results({ vs, base, view }: { vs: Verdict[]; base: string; view: ConformanceView }) {
  const sorted = [...vs].sort((a, b) => stateOf(a.state).rank - stateOf(b.state).rank || a.projectSlug.localeCompare(b.projectSlug))
  return <ListTable label="Results by project" rows={sorted} rowKey={v => v.projectSlug} rowTo={v => `${base}/projects/${v.projectSlug}`} columns={[
    { key: 'project', label: 'Project', phone: 'name', render: v => <Link to={`${base}/projects/${v.projectSlug}`}>{v.projectSlug}</Link> },
    { key: 'detail', label: 'Result', render: v => <>{v.detail && <span className="muted">{v.detail}</span>}{coveringChange(view, v) && <BeingFixed change={coveringChange(view, v)!} />}</> },
    { key: 'status', label: 'Status', phone: 'state', render: v => <StateChip finding={v.lane === 'findings'} label={v.statusLabel} /> },
  ]} />
}

// One check's page: each version's results, each with its own Fix everywhere,
// then how the check judges.
function CheckPage({ versions, view, base }: { versions: [string, Verdict[]][]; view: ConformanceView; base: string }) {
  const current = view.lens.kind === 'current'
  return <>
    {versions.map(([key, vs]) => {
      const def = view.checkDefs[key]
      return <Fragment key={key}>
        <Section title={versions.length > 1 ? `Version ${def?.version ?? ''}` : 'Results'} count={vs.length}
          action={current && fixableFindings(view, vs) > 0 && def?.key && <FixEverywhere fixKey={def.key} />}>
          <Results vs={vs} base={base} view={view} />
        </Section>
        {def && <Section title={versions.length > 1 ? `How version ${def.version} judges` : 'How this check judges'}><div className="small check-judging"><HowJudged def={def} /></div></Section>}
      </Fragment>
    })}
  </>
}

export function ChecksList({ view, base }: { view: ConformanceView; base: string }) {
  const [limit, setLimit] = useState(10)
  const { checkKey } = useParams()
  const grouped = new Map<string, [string, Verdict[]][]>()
  for (const entry of Object.entries(view.byCheck)) {
    const d = view.checkDefs[entry[0]]
    const identity = d?.block && d.check ? `${d.block}/${d.check}` : entry[0]
    grouped.set(identity, [...(grouped.get(identity) ?? []), entry])
  }
  const checks = [...grouped.entries()]
  const act = (vs: Verdict[]) => vs.filter((v) => ['findings', 'flagged', 'broken'].includes(laneOf(v))).length
  checks.sort((a, b) => act(b[1].flatMap(x => x[1])) - act(a[1].flatMap(x => x[1])) || a[0].localeCompare(b[0]))
  if (checkKey) {
    const one = checks.find(([, versions]) => versions.some(([key]) => key === checkKey))
    return one ? <CheckPage versions={one[1]} view={view} base={base} /> : <p className="muted small">This check has no results here.</p>
  }
  type Row = { id: string; title: string; vs: Verdict[]; fixKey?: string; key: string }
  const rows: Row[] = checks.slice(0, limit).map(([id, versions]) => ({
    id, key: versions[0][0], vs: versions.flatMap(v => v[1]),
    title: view.checkDefs[versions[0][0]]?.title || view.checkDefs[versions[0][0]]?.check || versions[0][1][0]?.check || 'Check',
    fixKey: versions.length === 1 ? view.checkDefs[versions[0][0]]?.key : undefined,
  }))
  return (
    <Section title="Checks" count={checks.length || undefined}>
      {checks.length === 0 && <p className="muted small">No checks recorded.</p>}
      {rows.length > 0 && <ListTable label="Checks" rows={rows} rowKey={r => r.id} rowTo={r => `${base}/checks/${encodeURIComponent(r.key)}`} columns={[
        { key: 'check', label: 'Check', phone: 'name', render: r => <Link to={`${base}/checks/${encodeURIComponent(r.key)}`}>{r.title}</Link> },
        { key: 'results', label: 'Results', render: r => <span className="muted">{tally(r.vs)}</span> },
        { key: 'status', label: 'Status', phone: 'state', render: r => checkStatus(r.vs) },
        { key: 'fix', label: '', phone: 'action', render: r => view.lens.kind === 'current' && r.fixKey && fixableFindings(view, r.vs) > 0 && <FixEverywhere fixKey={r.fixKey} /> },
      ]} />}
      {checks.length > limit && <p><button onClick={() => setLimit(n => n + 10)}>Show more checks</button></p>}
    </Section>
  )
}
