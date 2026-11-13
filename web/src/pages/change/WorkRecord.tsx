import { setPageContext } from '../../chrome'
import { Screenshots } from '../../Screenshots'
import './work-record.css'
import './deploy-scene.css'
import '../../overview-line.css'
import { dump } from 'js-yaml'
import { useEffect, useState } from 'react'
import { Link, useLocation, useParams, useSearchParams } from 'react-router-dom'
import type { Artifact, ChangeView, RunDetail } from '../../api'
import type { RunTrace } from '../../wire.gen'
import { useGet, useGetText } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { fmtStamp } from '../../format'
import { useSetProjectScope } from '../../projectScope'
import { parseUnifiedDiff } from '../../DiffView'
import { changeTitle, firstLine } from './Head'
import { recordKindName, runStory, StoryEntry } from './story'
import { renderMd } from '../../md'
import { useTechnicalDetail } from './TechnicalDetail'
import { ChangeWorkerChats } from '../WorkerChat'
import { stepTitleOf } from './Evidence'
import type { Spec } from '../../wire.gen'
import './change.css'

export function workRecordHref(change: string, entry: StoryEntry) {
  const q = new URLSearchParams({ run: entry.detail.run.id!, attempt: entry.key })
  return `/changes/${change}/work/${entry.step?.id || 'run'}?${q}`
}

// WorkflowStages leads the workflow's source with one plain line per stage:
// its name and its steps' titles. A definition without stages adds nothing.
export function WorkflowStages({ spec }: { spec: Spec }) {
  const stages = (spec.stages ?? []).filter(st => (st.onEnter?.length ?? 0) + (st.steps?.length ?? 0) > 0)
  if (!stages.length) return null
  return <ul className="work-workflow-stages">{stages.map((st, i) =>
    <li key={`${st.name}-${i}`}><strong>{st.name}:</strong> {[...(st.onEnter ?? []), ...(st.steps ?? [])].map(s => stepTitleOf(s.uses, s.name)).join(', ')}</li>)}</ul>
}

type LogRow = { kind: string; text: string }
// agent-log contains the server's bounded progress summary, not Pi's raw stream.
// Keep its order and words; never infer a tool's success from the next line.
export function logRows(body: string): LogRow[] {
  return body.split('\n').filter(line => line.trim()).map(text => {
    const tool = text.match(/^→ (\S+) /)?.[1]
    const kind = tool ? ({ read: 'read', bash: 'ran', edit: 'edited', write: 'edited', grep: 'searched', find: 'searched', ls: 'listed' }[tool] || tool)
      : /^[✔■✗⟳⏸]/.test(text) ? 'status' : 'said'
    return { kind, text }
  })
}
function RecordText({ artifact, session = false, code, fullHref }: { artifact: Artifact; session?: boolean; code: string; fullHref: string }) {
  const truncated = (artifact.bodyBytes ?? 0) > new TextEncoder().encode(artifact.body).length
  const q = useGet<Artifact>(truncated ? `/artifacts/${artifact.id}` : null)
  const body = q.data?.body ?? artifact.body
  const [messages, setMessages] = useState(false)
  const rows = logRows(body)
  const counts = new Map<string, number>()
  rows.forEach(r => counts.set(r.kind, (counts.get(r.kind) ?? 0) + 1))
  return <div id={`record-${artifact.id}`}>
    <ReadStatus q={q} what={artifact.name} /><StaleNote q={q} />{truncated && !q.data && <p className="small muted">Showing the first part of this record.</p>}
    {session ? <>
      <div className="work-mini"><span>{[...counts].map(([kind, count]) => `${count} ${kind}`).join(' · ')}</span><div role="group" aria-label="Session view"><button aria-pressed={messages} onClick={() => setMessages(true)}>messages only</button><button aria-pressed={!messages} onClick={() => setMessages(false)}>everything</button></div></div>
      <ol className="work-events" aria-label="Session events">{rows.map((r, i) => (!messages || r.kind === 'said') && <li key={i} className="work-event">
        <span className="muted" title="The helper log does not expose event times">—</span><strong>{r.kind}</strong><div><div className="work-line">{firstLine(r.text)}</div>{r.text.length > 180 && <><pre>{r.text.slice(0, 400).split('\n').slice(0, 3).join('\n')}</pre><Link to={fullHref}>full text</Link></>}{r.kind === 'edited' && (() => { const path = r.text.match(/"path"\s*:\s*"([^"]+)"/)?.[1]; return path && <Link to={`${code}#diff-${path}`}>{path} in Code</Link> })()}</div><span className="muted">{r.kind === 'said' ? 'recorded' : '—'}</span>
      </li>)}</ol>
    </> : <pre className="work-full">{body || 'No text was recorded.'}</pre>}
    {artifact.ref && <a href={artifact.ref}>Open record</a>}
  </div>
}

type RecordSection = 'request' | 'workflow' | 'report' | 'record'

export default function WorkRecordPage({ section }: { section?: RecordSection }) {
  const { id, stepId, artifactId } = useParams()
  const [params] = useSearchParams()
  const run = params.get('run')
  const change = useGet<ChangeView>(`/changes/${encodeURIComponent(id ?? '')}`, { poll: 3000 })
  const changeName = change.data ? `Change ${change.data.change.number} · ${changeTitle(change.data.change)}` : id || 'Change'
  useEffect(() => {
    setPageContext({ names: { [`/changes/${id}`]: changeName, [`/changes/${id}/work/${stepId}`]: 'Work record' } })
    return () => setPageContext({})
  }, [id, stepId, changeName])
  const trace = useGet<RunTrace>(run ? `/runs/${encodeURIComponent(run)}/trace` : null, { poll: 3000 })
  const [technical] = useTechnicalDetail()
  const location = useLocation()
  useSetProjectScope(change.current ? change.data?.projectSlug : null)
  useEffect(() => { if (location.hash) document.getElementById(decodeURIComponent(location.hash.slice(1)))?.scrollIntoView({ block: 'start' }) }, [location.hash, trace.data])
  const v = change.data, t = trace.data
  const detail: RunDetail | undefined = t?.run ? { checks: t.checks, run: t.run, steps: t.steps.filter(Boolean) as RunDetail['steps'], events: t.events as RunDetail['events'], artifacts: t.artifacts, stages: t.stages, next: [], landingMeans: [] } : undefined
  const entry = detail && runStory(detail).find(e => e.key === params.get('attempt') && (e.step?.id || 'run') === stepId)
  if (!v || !t) return <div className="ch-page"><ReadStatus q={change} what="the change" /><ReadStatus q={trace} what="the work record" />{!run && <p>This address does not name a run.</p>}</div>
  if (t.change?.id !== v.change.id || !entry) return <div className="ch-page"><p>This session is not in this change’s work record.</p><Link to={`/changes/${v.change.id}?tab=details`}>Back to the change</Link></div>
  return <WorkRecord section={section} artifactId={artifactId} key={entry.key} view={v} trace={t} entry={entry} technical={technical} notes={<><StaleNote q={change} /><StaleNote q={trace} /></>} />
}

function WorkRecord({ view: v, trace, entry, technical, notes, section, artifactId }: { view: ChangeView; trace: RunTrace; entry: StoryEntry; technical: boolean; notes: React.ReactNode; section?: RecordSection; artifactId?: string }) {
  const code = `/changes/${v.change.id}?tab=changes`
  const diff = useGetText(`/changes/${v.change.id}/diff`)
  const files = diff.data ? parseUnifiedDiff(diff.data) : []
  const evidence = entry.step?.evidence
  const changed = evidence && 'files' in evidence ? evidence.files : undefined
  const logs = entry.records.filter(a => a.kind === 'agent-log')
  const instructions = entry.records.filter(a => a.kind === 'steering')
  const records = entry.records.filter(a => !['agent-log', 'steering'].includes(a.kind))
  const guidance = trace.guidance as { imports?: { name: string; version: number }[] } | null
  const location = useLocation()
  const base = `/changes/${v.change.id}/work/${entry.step?.id || 'run'}`
  const link = (part: string) => `${base}/${part}${location.search}`
  const recordLink = (a: Artifact) => link(`records/${a.id}`)
  if (section) {
    const artifact = entry.records.find(a => a.id === artifactId)
    const kind = artifact && recordKindName(artifact.kind)
    const title = section === 'request' ? 'Change request' : section === 'workflow' ? 'Workflow' : section === 'report' ? 'Report' : kind ? kind[0].toUpperCase() + kind.slice(1) : 'Record'
    const report = evidence && 'output' in evidence ? evidence.output : ''
    // An empty record says so in plain text, outside the box its content would fill.
    const empty = section === 'report' ? !report && 'No report was recorded.'
      : section === 'record' && artifact && !artifact.binary && !artifact.body && !artifact.bodyBytes && 'No text was recorded.'
    return <div className="ch-page work-page">{notes}<h1>{title}</h1>
      {artifact && artifact.name !== kind && <p className="work-byline">{artifact.name}</p>}
      {empty ? <><p className="muted">{empty}</p>{artifact?.ref && <a href={artifact.ref}>Open record</a>}</> : <section className="ch-change-card work-full">
      {section === 'request' ? <div className="md" dangerouslySetInnerHTML={{ __html: renderMd(v.change.body || v.change.title) }} />
        : section === 'workflow' ? (trace.workflow.definition ? <><WorkflowStages spec={trace.workflow.definition} /><pre className="source">{dump(trace.workflow.definition)}</pre></> : <p className="muted">not recorded</p>)
        : section === 'report' ? <pre>{report}</pre>
        : !artifact ? <p>This record is not in this work session.</p>
        : artifact.binary ? <Screenshots shots={{shots:[{artifact:artifact.id!,path:artifact.name,view:'record'}]}} />
        : <RecordText artifact={artifact} code={code} fullHref={recordLink(artifact)} />}
    </section>}</div>
  }
  return <div className="ch-page work-page">
    {notes}
    <header><h1>Work record</h1>
      <p className="work-byline">{[
        // A run-level entry (a run with no agent step, such as landing) is the
        // run's record, not an agent's.
        !entry.step ? 'Run' : entry.step.type === 'agent/review' ? 'Reviewer' : 'Helper',
        evidence && 'provider' in evidence && evidence.provider,
        evidence && 'model' in evidence && evidence.model,
        entry.at && fmtStamp(entry.at),
        evidence?.durationMs != null && `${Math.round(evidence.durationMs / 1000)}s`,
        changed && `${changed.length} file${changed.length === 1 ? '' : 's'} changed`,
        entry.step?.status || entry.detail.run.status,
      ].filter(Boolean).join(' · ')}</p>
    </header>
    <section className="ch-change-card"><h2>What the helper was given</h2><table className="ch-facts-table"><tbody>
      <tr><th>Request</th><td>{firstLine(v.change.body || v.change.title)} <Link to={link('request')}>full request</Link><span className="small muted"> · Change request</span></td></tr>
      <tr><th>Instructions</th><td>{instructions.length ? instructions.map(a => <div key={a.id}><Link to={recordLink(a)}>{a.name}</Link></div>) : <span className="muted">not recorded</span>}</td></tr>
      <tr><th>Pinned guidance</th><td>{guidance?.imports?.length ? guidance.imports.map(g => <div key={g.name}><Link to={`/guidance/${encodeURIComponent(g.name)}?version=${g.version}`}>{g.name}{'\u00a0'}v{g.version}</Link></div>) : <span className="muted">not recorded</span>}</td></tr>
      <tr><th>Workflow step</th><td>{[`${entry.detail.run.workflowName} v${entry.detail.run.workflowVersion}`, entry.step?.stage, entry.step?.name || 'run'].filter(Boolean).join(' · ')} <Link to={link('workflow')}>workflow text</Link></td></tr>
    </tbody></table></section>
    <section className="ch-change-card"><h2>The session</h2><p className="small muted">{fmtStamp(entry.at)}{entry.step?.finishedAt && ` → ${fmtStamp(entry.step.finishedAt)}`} · {entry.session || '—'}{!logs.length && ' · log not recorded'}</p>
      {logs.length ? logs.map(a => <RecordText key={a.id} artifact={a} session code={code} fullHref={recordLink(a)} />) : null}
    </section>
    <section className="ch-change-card"><h2>What it produced</h2><table className="ch-facts-table"><tbody>
      {!entry.step ? <tr><th>Code</th><td><Link to={code}>code for the whole change →</Link></td></tr>
      : <tr><th>Files changed</th><td>{changed ? changed.length ? changed.map(path => { const f = files.find(f => f.path === path); return <div key={path}><Link to={`${code}#diff-${path}`}>{path}</Link> <span className="mono small">{f ? `+${f.adds} −${f.dels} (whole change)` : '—'}</span></div> }) : 'No files changed.' : <span className="muted">—</span>}<ReadStatus q={diff} what="file counts" /><StaleNote q={diff} /> · <Link to={code}>code for the whole change →</Link></td></tr>}
      <tr><th>Records</th><td>{[...logs, ...records].map(a => <div key={a.id}><Link to={recordLink(a)}>{a.kind === 'agent-log' ? 'helper log' : a.name || a.kind}</Link></div>)}{!logs.length && !records.length && <span className="muted">not recorded</span>}</td></tr>
      {evidence && 'output' in evidence && evidence.output && <tr><th>Report</th><td>{firstLine(evidence.output)} <Link to={link('report')}>full report</Link></td></tr>}
      <tr><th>Blockers reported</th><td>{evidence?.reason || evidence?.blockedBy || <span className="muted">not recorded</span>}</td></tr>
    </tbody></table><h3>Chats on this change</h3><Link to={`/changes/${v.change.id}?tab=conversation`}>Read the conversation</Link><ChangeWorkerChats changeId={v.change.id!} /></section>
    {technical && <section className="ch-change-card work-full"><h2>Technical detail</h2><h3>Trace JSON</h3><pre>{JSON.stringify(trace, null, 2)}</pre></section>}
  </div>
}
