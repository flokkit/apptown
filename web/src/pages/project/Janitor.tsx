import { JanitorAttribution } from '../../janitorAttribution'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useGet, useAction } from '../../data'
import { post } from '../../api'
import { useHolds } from '../../instancePages'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Section } from '../../Section'
import { ListTable, type ListColumn } from '../../ListTable'
import { Tech } from '../../Tech'
import { fmtWhen } from '../../format'
import type { ProjectScanView, ScanObservation, Finding, QueueStatus, AutomatedAttempt } from '../../wire.gen'

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

function QueueLine({ queue, slug, reload }: { queue: QueueStatus; slug: string; reload: () => void }) {
  const action = useAction(reload)
  const canSkip = useHolds('change:abandon', slug)
  return <div>
    <p><strong>{queue.title}</strong>: {queue.words}{queue.reason && <> · {queue.reason}</>}{queue.packages.length > 0 && <> · moved: {queue.packages.map(p => `${p.package} ${p.from} → ${p.to}`).join(', ')}</>}</p>
    {queue.changeId && <p>
      <Link to={`/changes/${queue.changeId}`}>Try again</Link> · <Link to={`/changes/${queue.changeId}?tab=conversation`}>Open the chat</Link>
      {canSkip && <> · <button type="button" className="link" disabled={action.busy} onClick={() => action.run(() => post(`/projects/${slug}/janitor/${queue.queue}/skip`, { changeId: queue.changeId }))}>Skip this update</button></>}
    </p>}
    {action.err && <p className="error">{action.err}</p>}
  </div>
}

const tryColumns = (queues: QueueStatus[]): ListColumn<AutomatedAttempt>[] => [
  { key: 'what', label: 'Update', phone: 'name', render: a => a.changeId ? <Link to={`/changes/${a.changeId}`}>{a.what}</Link> : a.what },
  { key: 'queue', label: 'Queue', render: a => queues.find(q => q.queue === a.queue)?.title || a.queue },
  { key: 'result', label: 'Result', phone: 'state', render: a => a.reason || (a.outcome === 'held' ? 'Waiting for a person' : a.outcome === 'shipped' ? 'Landed' : a.outcome === 'skipped' ? 'Skipped' : a.openChange ? 'In progress' : 'Closed') },
  { key: 'packages', label: 'Packages', render: a => a.packages.map(p => `${p.package} ${p.from} → ${p.to}`).join(', ') || '—' },
  { key: 'origin', label: 'From', render: a => a.ruleId ? <JanitorAttribution rule={a.rule!} revision={a.ruleRevision} issuer={a.ruleIssuer!.name} /> : a.schedule ? `Schedule ${a.schedule}` : 'Not recorded' },
  { key: 'when', label: 'When', kind: 'time', render: a => fmtWhen(a.when) },
]

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
      <JanitorChoice slug={slug} data={data!} current={q.current} reload={q.reload} />
      <p>Last scan: {scans.known && scans.lastScan && !scans.lastScan.startsWith('0001-') ? fmtWhen(scans.lastScan) : 'not known'}{(count > 0 || scans.known) && <> · {count ? `${count} ${count === 1 ? 'finding' : 'findings'}` : 'no findings'}</>}{scans.stale && <> · <span className="badge">older than three days</span></>}</p>
      {data!.queues.map(queue => <QueueLine key={queue.queue} queue={queue} slug={slug} reload={q.reload} />)}
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
    {janitor && data && data.recent.some(a => a.attempted) && <><h3>Recent tries</h3><ListTable label="Recent tries" rows={data.recent.filter(a => a.attempted)} columns={tryColumns(data.queues)} rowKey={a => a.id!} rowTo={a => a.changeId ? `/changes/${a.changeId}` : undefined} /></>}
    {janitor && <p><Link to="/janitor">Janitor</Link> · <Link to={`/conformance/projects/${slug}`}>Scan history</Link></p>}
  </Section>
}

function JanitorChoice({ slug, data, current, reload }: { slug: string; data: ProjectScanView; current: boolean; reload: () => Promise<void> }) {
  const canManage = useHolds('project:manage-standing-approval', slug)
  const [reason, setReason] = useState<string | null>(null)
  const action = useAction(reload)
  const [level, setLevel] = useState('routine')
  const off = data.standing?.level === 'off'
  return <>
    {data.heldBack?.map(hold => <div key={hold.deploymentId}>
      <p><strong>Stopped: release {hold.releaseNumber} to {hold.displayLabel} {hold.reason === 'release failed' ? 'failed' : hold.reason}.</strong></p>
      <p><Link to={`/projects/${slug}/deployments/${hold.deployment}?tab=history&rollbackBefore=${hold.releaseNumber}`}>Roll back</Link> · <Link to={`/projects/${slug}/deployments/${hold.deployment}?tab=history`}>Try again</Link></p>
    </div>)}
    {data.standing?.enabled && <p>{off ? 'Turned off for this project' : `Kept updated by the janitor · ${data.standing.level}`}{data.standing.choice && <> · {data.standing.choice.person.name}, {fmtWhen(data.standing.choice.at)}</>}
      {canManage && <> · <button type="button" className="link" disabled={!current || action.busy} onClick={() => { setLevel(off ? 'routine' : 'off'); setReason('') }}>{off ? 'Turn back on' : 'turn off'}</button> · <button type="button" className="link" disabled={!current || action.busy} onClick={() => { setLevel(data.standing.level); setReason('') }}>Change level</button></>}
    </p>}
    {reason !== null && <form className="stack stack-2" onSubmit={async event => { event.preventDefault(); if (await action.run(() => post(`/projects/${slug}/janitor/settings`, { level, reason }))) setReason(null) }}>
      <label>Update level<select aria-label="Janitor update level" value={level} onChange={event => setLevel(event.target.value)} disabled={action.busy}><option value="routine">Routine · patch and minor</option><option value="aggressive">Aggressive · including major</option><option value="off">Off</option></select></label>
      <label>Reason<input aria-label="Reason for janitor setting" value={reason} onChange={event => setReason(event.target.value)} required disabled={action.busy} /></label>
      <div className="row row-2"><button type="submit" disabled={action.busy || !current || !reason.trim()}>{level === 'off' ? 'Turn off for this project' : 'Save update level'}</button><button type="button" onClick={() => setReason(null)} disabled={action.busy}>Cancel</button></div>
    </form>}
    {action.err && <p className="error" role="alert">{action.err}</p>}
  </>
}
