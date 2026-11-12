import { Link } from 'react-router-dom'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Section } from '../../Section'
import { ListTable, type ListColumn } from '../../ListTable'
import { Tech } from '../../Tech'
import { fmtWhen } from '../../format'
import type { ProjectScanView, ScanObservation, Finding } from '../../wire.gen'

function FindingList({ title, findings }: { title: string; findings: Finding[] }) {
  if (!findings.length) return null
  return <><h3>{title}</h3>{findings.map((f, i) => <p key={`${f.fingerprint}-${i}`}>
    {f.package?.name && <><Tech>{f.package.name}</Tech> · </>}
    {f.identifiers?.join(', ') || f.title} · {f.severity}
    {f.disposition && <span className="muted"> · exception recorded</span>}
  </p>)}</>
}

function ShortHash({ value }: { value: string }) {
  return <code title={value}>{value.replace(/^.*sha256:/, '').slice(0, 12)}</code>
}

function Subject({ subject }: { subject: ScanObservation['subject'] }) {
  return subject.on === 'source'
    ? <>Code{subject.commit && <> at <ShortHash value={subject.commit} /></>}</>
    : <>{subject.deployment || 'Running'}{subject.part ? ` · ${subject.part}` : ''} image{subject.digest && <> <ShortHash value={subject.digest} /></>}</>
}

type ScanRow = Pick<ScanObservation, 'id' | 'subject' | 'scanner' | 'verdict' | 'reason' | 'observedAt' | 'findings'>

function Result({ scan }: { scan: ScanRow }) {
  const result = scan.verdict === 'error' ? 'Not known' : scan.verdict === 'not_applicable' ? 'Not applicable'
    : scan.findings.length ? `${scan.findings.length} ${scan.findings.length === 1 ? 'finding' : 'findings'}`
      : scan.verdict === 'fail' ? 'Not known' : 'No findings'
  return <><span className="badge">{result}</span>{scan.reason && <> · {scan.reason}</>}</>
}

const columns: ListColumn<ScanRow>[] = [
  { key: 'subject', label: 'Subject', phone: 'name', render: scan => <Subject subject={scan.subject} /> },
  { key: 'scanner', label: 'Scanner', render: scan => scan.scanner || '—' },
  { key: 'result', label: 'Result', render: scan => <Result scan={scan} /> },
  { key: 'when', label: 'When', kind: 'time', render: scan => fmtWhen(scan.observedAt) || '—' },
]

const unique = (findings: Finding[]) => [...new Map(findings.map(f => [f.fingerprint, f])).values()]

export function LastScan({ slug, janitor = false }: { slug: string; janitor?: boolean }) {
  const q = useGet<ProjectScanView>(`/projects/${slug}/scans`, { poll: 10000 })
  const data = q.data
  const scans = data?.scans
  const project = scans ? unique([...scans.source.flatMap(s => s.findings), ...scans.running.flatMap(s => s.projectFindings)]) : []
  const imageOnly = scans ? unique(scans.running.flatMap(s => s.imageOnlyFindings)) : []
  const unclassified = scans ? unique(scans.running.flatMap(s => s.unclassifiedFindings)) : []
  const count = unique([...project, ...imageOnly, ...unclassified]).length
  return <Section title={janitor ? 'Janitor' : 'Last scan'}>
    <ReadStatus q={q} what="the last scan" />
    <StaleNote q={q} />
    {scans && (janitor ? <>
      <p>Last scan: {scans.known && scans.lastScan && !scans.lastScan.startsWith('0001-') ? fmtWhen(scans.lastScan) : 'not known'}{(count > 0 || scans.known) && <> · {count ? `${count} ${count === 1 ? 'finding' : 'findings'}` : 'no findings'}</>}{scans.stale && <> · <span className="badge">older than three days</span></>}</p>
      <FindingList title="Project’s findings" findings={project} />
      <FindingList title="Running image only" findings={imageOnly} />
      <FindingList title="Not compared with project code" findings={unclassified} />
    </> : <>
      {!scans.known && <p><span className="badge">Not known</span> · {scans.reason}</p>}
      <ListTable label="Last scan" columns={columns} rows={[
        ...scans.source,
        ...(scans.source.length ? [] : [{ id: 'unknown-source', subject: { on: 'source', commit: scans.main }, scanner: '', verdict: 'error', reason: 'No scan of the current project code', observedAt: '', findings: [] }]),
        ...scans.running,
      ]} rowKey={scan => scan.id!} />
      {scans.stale && <p><span className="badge">older than three days</span></p>}
      {scans.historical.length > 0 && <><h3>Earlier</h3><ListTable label="Earlier scans" columns={columns} rows={[...scans.historical].sort((a, b) => b.observedAt.localeCompare(a.observedAt)).slice(0, 10)} rowKey={scan => scan.id!} /></>}
    </>)}
    {janitor && <p><Link to={`/conformance/projects/${slug}`}>Scan history</Link></p>}
  </Section>
}
