import { Link, useSearchParams } from 'react-router-dom'
import { PRODUCT } from '../../brand'
import { useGet } from '../../data'
import { HelpButton, PageHead } from '../../PageHead'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Section } from '../../Section'
import { ListTable, type ListColumn } from '../../ListTable'
import { Tabs } from '../../Tabs'
import { fmtDay } from '../../format'
import './Statistics.css'
import type { JanitorCounts, JanitorKindStatistics, JanitorPatchTimes, JanitorProjectStatistics, JanitorReason, JanitorStatistics } from '../../wire.gen'

export const KIND_LABEL: Record<string, string> = { packs: 'Packs', updates: 'Dependency and image updates', security: 'Security fixes' }
const COLUMN_LABEL: Record<string, string> = { waiting: 'Waiting on a person', failed: 'Failed' }

// Durations from the server are whole seconds; patch times run to days.
export function fmtSpan(seconds: number): string {
  if (seconds < 3600) return `${Math.max(1, Math.round(seconds / 60))} min`
  if (seconds < 48 * 3600) return `${Math.round(seconds / 3600)} h`
  return `${Math.round(seconds / 86400)} days`
}

// Kind and project rows share these columns, so the two tables count alike.
function countColumns<T>(counts: (row: T) => JanitorCounts): ListColumn<T>[] {
  const n = (key: keyof JanitorCounts, label: string): ListColumn<T> => ({ key, label, kind: 'number', render: row => counts(row)[key] })
  return [n('runs', 'Runs'), n('shipped', 'Shipped'), n('nothingToChange', 'Nothing to change'), n('waiting', 'Waiting on a person'), n('failed', 'Failed'), n('findingsLeft', 'Findings left'), n('closed', 'Closed'), n('running', 'Running')]
}

function patchColumns<T>(patch: (row: T) => JanitorPatchTimes): ListColumn<T>[] {
  return [
    { key: 'patched', label: 'Patched', kind: 'number', render: row => patch(row).patched },
    { key: 'median', label: 'Median', kind: 'number', render: row => patch(row).patched ? fmtSpan(patch(row).median) : '—' },
    { key: 'slowest', label: 'Slowest', kind: 'number', render: row => patch(row).patched ? fmtSpan(patch(row).slowest) : '—' },
    { key: 'open', label: 'Still open (seen in period)', kind: 'number', render: row => patch(row).open },
  ]
}

const reasonKey = (r: JanitorReason) => `${r.kind}/${r.column}/${r.step}`

export default function JanitorStatisticsPage() {
  const [params] = useSearchParams()
  const days = params.get('days') === '30' ? 30 : 7
  const q = useGet<JanitorStatistics>(`/janitor/statistics?days=${days}`)
  const data = q.data
  const reasons: ListColumn<JanitorReason>[] = [
    { key: 'kind', label: 'Kind', phone: 'name', render: r => KIND_LABEL[r.kind] ?? r.kind },
    { key: 'column', label: 'Result', className: 'record-words', render: r => COLUMN_LABEL[r.column] ?? r.column },
    { key: 'step', label: 'Stopped at', className: 'record-words', render: r => r.step },
    { key: 'count', label: 'Count', kind: 'number', render: r => r.count },
    { key: 'latest', label: 'Latest reason', className: 'record-words', render: r => r.latest.startsWith(r.step + ': ') ? r.latest.slice(r.step.length + 2) : r.latest === r.step ? '' : r.latest },
    { key: 'examples', label: 'Examples', render: r => <span className="row row-2">{r.examples.map((e, i) => e.changeId
      ? <Link key={i} to={`/changes/${e.changeId}`}>{e.project}, {fmtDay(e.at)}</Link>
      : <span key={i} className="muted">{e.project}, {fmtDay(e.at)}</span>)}</span> },
  ]
  const kinds: ListColumn<JanitorKindStatistics>[] = [
    { key: 'kind', label: 'Kind', phone: 'name', render: k => KIND_LABEL[k.kind] ?? k.kind },
    ...countColumns<JanitorKindStatistics>(k => k.counts),
  ]
  const projects: ListColumn<JanitorProjectStatistics>[] = [
    { key: 'project', label: 'Project', phone: 'name', render: p => <Link to={`/projects/${p.slug}/settings/general#janitor`}>{p.name}</Link> },
    ...countColumns<JanitorProjectStatistics>(p => p.counts),
    ...patchColumns<JanitorProjectStatistics>(p => p.patch),
  ]
  const totals: ListColumn<JanitorPatchTimes>[] = [
    { key: 'all', label: '', phone: 'name', render: () => 'All projects' },
    ...patchColumns<JanitorPatchTimes>(p => p),
    { key: 'oldest', label: 'Oldest open', kind: 'number', render: p => p.open ? fmtSpan(p.oldestOpen) : '—' },
  ]
  return <div className="janitor-statistics">
    <PageHead page="maintenance" sub="Statistics" />
    <Tabs aria-label="Period">
      {[7, 30].map(d => <Link key={d} to={`?days=${d}`} className={days === d ? 'tab active' : 'tab'} aria-current={days === d ? 'page' : undefined}>{d} days</Link>)}
    </Tabs>
    <ReadStatus q={q} what="janitor statistics" /><StaleNote q={q} />
    {data && <>
      <Section title="By kind" action={<HelpButton about="Findings left" guide="janitor#statistics">
        <p>Findings left: the scan worked, but found problems the Janitor cannot fix yet, such as no newer image or no fixed version.</p>
      </HelpButton>}><ListTable label="Runs by kind" columns={kinds} rows={data.kinds} rowKey={k => k.kind} /></Section>
      <Section title="Why not shipped">{data.reasons.length
        ? <ListTable label="Why not shipped" columns={reasons} rows={data.reasons} rowKey={reasonKey} />
        : <p>None in the last {days} days.</p>}</Section>
      <Section title="Time to patch" action={<HelpButton about="Time to patch" guide="janitor#statistics">
        <p>From the first {PRODUCT} scan that found a vulnerability to the first usable running-app scan without it after its latest sighting. When a fix was published is not recorded.</p>
      </HelpButton>}><ListTable label="Time to patch" columns={totals} rows={[data.patch]} rowKey={() => 'all'} /></Section>
      <Section title="Projects">{data.projects.length
        ? <ListTable label="Projects" columns={projects} rows={data.projects} rowKey={p => p.slug} rowTo={p => `/projects/${p.slug}/settings/general#janitor`} />
        : <p>No Janitor runs or scanned vulnerabilities in the last {days} days.</p>}</Section>
    </>}
  </div>
}
