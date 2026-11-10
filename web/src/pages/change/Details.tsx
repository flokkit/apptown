import { ListRow } from '../../ListRow'
import './list-records.css'
import { usePageName } from '../../chrome'
import { StateRecord } from './StatePanel'
import { TechnicalWords, useTechnicalDetail } from './TechnicalDetail'
import { ReactNode, useCallback, useEffect, useState } from 'react'
import { Link, useLocation, useParams, useSearchParams } from 'react-router-dom'
import type { Artifact, ChangeView, RunDetail } from '../../api'
import { post } from '../../api'
import type { ReleaseDetail, RunTrace } from '../../wire.gen'
import { Screenshots } from '../../Screenshots'
import { fmtPrice, fmtStamp, fmtTokens } from '../../format'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Attachments, RequestEntry, changeReport, changeTitle } from './Head'
import { StepActions, StepCard } from './Steps'
import { AddArtifact, AdrLinks, ArtifactBody } from './Tabs'
import { stepTitle } from './Evidence'
import { workRecordHref } from './WorkRecord'
import { checkResult, recordLabel, runStory, StoryEntry } from './story'

function TraceRead({ id, receive }: { id: string; receive: (id: string, detail: RunDetail) => void }) {
  const q = useGet<RunTrace>(`/runs/${id}/trace`, { poll: 3000 })
  useEffect(() => {
    const t = q.data
    if (t?.run) receive(id, { checks: t.checks, run: t.run, steps: t.steps.filter(Boolean) as RunDetail['steps'], stages: t.stages, events: t.events as RunDetail['events'], artifacts: t.artifacts, next: [], landingMeans: [] })
  }, [id, q.data, receive])
  return <><ReadStatus q={q} what="earlier work" /><StaleNote q={q} /></>
}
function ReleaseRead({ slug, number, receive }: { slug: string; number: number; receive: (n: number, detail: ReleaseDetail) => void }) {
  const q = useGet<ReleaseDetail>(`/projects/${slug}/releases/${number}`)
  useEffect(() => { if (q.data) receive(number, q.data) }, [number, q.data, receive])
  return <><ReadStatus q={q} what={`release #${number}`} /><StaleNote q={q} /></>
}

// Evidence has its own page so opening a record never expands the list.
function EvidenceLink({ id, runId, children }: { id: string; runId?: string; children: ReactNode }) {
  const { pathname } = useLocation()
  const base = pathname.match(/^\/changes\/[^/]+/)?.[0] ?? pathname
  return <Link to={`${base}/records/${encodeURIComponent(id)}${runId ? `?run=${encodeURIComponent(runId)}` : ''}`}>{children}</Link>
}

function RecordLink({ artifact, runId }: { artifact: Artifact; runId: string }) {
  const label = recordLabel(artifact)
  return <div className="ch-story-record" id={`record-${artifact.id}`}>
    <EvidenceLink id={`record-${artifact.id}`} runId={runId}>{label}</EvidenceLink>

  </div>
}

function RecordBody({ artifact }: { artifact: Artifact }) {
  return <div className="ch-story-evidence">
      <p className="small muted">{artifact.name} · {artifact.kind.replace(/-/g, ' ')} · {fmtStamp(artifact.createdAt)}</p>
      {artifact.kind === 'agent-reported' && <p>Reported by the agent.</p>}
      {artifact.ref && <a href={artifact.ref} target="_blank" rel="noreferrer">Open record</a>}
      {artifact.binary ? <Screenshots shots={{ shots: [{ artifact: artifact.id!, path: artifact.name, view: "record" }] }} /> : artifact.body && <ArtifactBody artifact={artifact} />}
  </div>
}

function Records({ records, runId }: { records: Artifact[]; runId: string }) {
  // A record written again (an approval brief asked for twice) shows once: its latest copy.
  const latest = [...new Map([...records].sort((a, b) => (a.createdAt ?? '').localeCompare(b.createdAt ?? '')).map(a => [`${a.kind}/${a.name}`, a])).values()]
  return <>{latest.map(a => <RecordLink key={a.id} artifact={a} runId={runId} />)}</>
}

type Phase = { key: string; title: string; entries: StoryEntry[]; landing: boolean }
// Receipts, rather than the current step state, define round boundaries.
export function storyPhases(story: StoryEntry[], summaries: ChangeView['runs']): Phase[] {
  const phases: Phase[] = []
  let round = 0
  const runs = new Map(story.map(e => [e.detail.run.id, e.detail.run]))
  for (const entry of story) {
    const parent = entry.detail.run.parentRunId ? runs.get(entry.detail.run.parentRunId) : undefined
    const landing = summaries.some(r => r.id === (parent || entry.detail.run).id && r.role === 'landing')
    let phase = phases.at(-1)
    const newRound = !landing && entry.step?.type === 'agent/pi' && phase?.entries.some(e => e.step?.type === 'agent/pi')
    if (!phase || phase.landing !== landing || (landing && phase.entries[0]?.detail.run.id !== entry.detail.run.id) || newRound || (!landing && !parent && phase.entries.length > 0 && phase.entries.at(-1)?.detail.run.id !== entry.detail.run.id)) {
      if (!landing) round++
      const sentBack = [...story.flatMap(e => e.events)].filter(e => e.kind === 'step.sendback' && e.at <= entry.at).sort((a, b) => a.at.localeCompare(b.at)).at(-1)
      phase = { key: entry.key, landing, title: landing ? 'Landing' : round === 1 ? 'Work' : `Work, round ${round}${sentBack ? ` — sent back by ${sentBack.payload.by}: ${sentBack.payload.note}` : ''}`, entries: [] }
      phases.push(phase)
    }
    phase.entries.push(entry)
  }
  return phases
}

export function ChangeRecordPage() {
  const { id = '', recordId = '' } = useParams()
  const [params] = useSearchParams()
  const q = useGet<ChangeView>(`/changes/${id}`, { poll: 3000 })
  usePageName(`/changes/${id}`, q.data ? `Change ${q.data.change.number} · ${q.data.change.title}` : undefined)
  return <><ReadStatus q={q} what="the change record" /><StaleNote q={q} />
    {q.data && <ChangeDetails view={q.data} recordId={recordId} current={q.current} reload={q.reload} runId={params.get('run') || undefined} />}
  </>
}

export function ChangeDetails({ recordId, view, current, reload, runId }: { recordId?: string; view: ChangeView; current: boolean; reload: () => void; runId?: string }) {
  const [technical] = useTechnicalDetail()
  const [traces, setTraces] = useState<Record<string, RunDetail>>({})
  const [releases, setReleases] = useState<Record<number, ReleaseDetail>>({})
  const receive = useCallback((id: string, detail: RunDetail) => setTraces(old => ({ ...old, [id]: detail })), [])
  const receiveRelease = useCallback((n: number, detail: ReleaseDetail) => setReleases(old => ({ ...old, [n]: detail })), [])
  const [adding, setAdding] = useState(false)
  const location = useLocation()
  const details = [...Object.values(traces).filter(d => d.run.id !== view.detail?.run.id), ...(view.detail ? [view.detail] : [])]
  const ids = new Set([...(view.runs ?? []).map(r => r.id!), ...(runId ? [runId] : []), ...details.flatMap(d => d.steps.flatMap(s => s.type === 'workflow/call' && s.evidence.childRunId ? [s.evidence.childRunId] : []))])
  ids.delete(view.detail?.run.id ?? '')
  const story = details.flatMap(runStory).sort((a, b) => a.at.localeCompare(b.at))
  const phases = storyPhases(story, view.runs ?? [])
  if (!phases.length || phases[0].landing) phases.unshift({ key: 'request', title: 'Work', landing: false, entries: [] })
  const latest = view.detail
  const recordStory = runId ? story.filter(e => e.detail.run.id === runId) : story
  const previewEntry = [...recordStory].reverse().find(e => e.step?.type === 'preview')
  const reviewEntry = [...recordStory].reverse().find(e => e.step?.type === 'agent/review')
  const reportEntry = [...story].reverse().find(e => e.step?.type === 'agent/pi') || story.at(-1)
  const fullReport = changeReport(view)
  const mergedEntry = story.find(e => e.step?.type === 'scm/merged' && e.step.status === 'pass' && !e.step.overrideBy)
  const landingPhase = () => {
    let phase = phases.at(-1)!
    if (!phase.landing) { phase = { key: 'landing', title: 'Landing', landing: true, entries: [] }; phases.push(phase) }
    return phase
  }
  const extraRows: { key: string; at: string; label: string; facts: ReactNode }[] = []
  if (view.evidence?.inAdvance) {
    const a = view.evidence.inAdvance
    extraRows.push({ key: 'advance', at: a.at, label: 'Approval', facts: <>Accepted in advance by {a.approver}: {a.shown.join(' ')}</> })
  }
  if (view.change.landedSha && !mergedEntry) { landingPhase(); extraRows.push({ key: 'merged', at: view.change.closedAt || '', label: 'Merged', facts: <>merged at <Link to="?tab=changes">{view.change.landedSha.slice(0, 12)}</Link></> }) }
  for (const r of view.evidence?.releases ?? []) {
    landingPhase()
    const release = releases[r.number]?.release
    extraRows.push({ key: `release-${r.number}`, at: release?.releasedAt || release?.createdAt || '', label: 'Released', facts: <Link to={`/projects/${view.projectSlug}/releases/${r.number}`}>Released #{r.number} to {r.deployment}{r.state !== 'released' && ` · ${r.statusLabel}`}</Link> })
  }
  if (view.change.state === 'abandoned' && !story.some(e => e.events.some(event => event.kind === 'run.abandoned'))) extraRows.push({ key: 'abandoned', at: view.change.closedAt || '', label: 'Abandoned', facts: <>Abandoned{view.change.abandonedBy && ` by ${view.change.abandonedBy}`}: {view.stateDetail}</> })
  useEffect(() => {
    const target = decodeURIComponent(location.hash.slice(1))
    if (target) document.getElementById(target)?.scrollIntoView({ block: 'start' })
  }, [runId, story.length, location.hash])
  if (recordId) {
    const recordsRead = runId ? view.detail?.run.id === runId || !!traces[runId] : [...ids].every(id => !!traces[id])
    const entry = story.find(e => `entry-${e.key}` === recordId)
      || (recordId === 'preview' ? previewEntry : recordId === 'review' ? reviewEntry : undefined)
    const artifact = story.flatMap(e => e.records).find(a => `record-${a.id}` === recordId)
    return <><h1>{recordId === 'request' ? 'Request' : recordId === 'summary' ? 'Report' : recordId === 'preview' ? 'Preview' : recordId === 'review' ? 'Review' : recordId === 'checks' ? 'Checks' : entry?.step ? stepTitle(entry.step) : artifact?.name || 'Record'}</h1>
      <p className="muted small">From <Link to={`/changes/${view.change.id}`}>change {view.change.number} · {changeTitle(view.change)}</Link></p>
      {[...ids].map(id => <TraceRead key={id} id={id} receive={receive} />)}
      {recordId === 'request' ? <RequestEntry full view={view} current={current} reload={reload} /> : recordId === 'summary' ? <Report text={fullReport || 'No report was recorded.'} changeId={view.change.id!} />
        : recordId === 'checks' ? recordStory.some(e => e.text.startsWith('Checks:')) ? recordStory.filter(e => e.text.startsWith('Checks:')).map(e => <EntryEvidence key={e.key} entry={e} view={view} reload={reload} />) : <p>{recordsRead ? 'No checks were recorded.' : 'This record has not been read.'}</p>
        : entry ? <EntryEvidence entry={entry} view={view} reload={reload} />
        : artifact ? <RecordBody artifact={artifact} /> : recordId === 'preview' && recordsRead ? <p>No preview was recorded.</p> : recordId === 'review' && recordsRead ? <p>No review was recorded.</p> : <p>This record has not been read.</p>}
    </>
  }
  let previousDate = ''
  const time = (at: string) => {
    const stamp = at ? new Date(at) : null
    const date = stamp?.toLocaleDateString(undefined, { day: 'numeric', month: 'short' }) || ''
    const changed = date !== previousDate
    previousDate = date
    return <time dateTime={at}>{changed && <span>{date}</span>}{stamp?.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })}</time>
  }
  const entryRow = (e: StoryEntry) => {
    const key = `entry-${e.key}`, s = e.step
    const agentWork = s?.type === 'agent/pi' || e.key.startsWith('work-')
    const facts = e.key.startsWith('work-') ? (e.detail.run.id === latest?.run.id && view.diffstat?.match(/\d+ files? changed/)?.[0]) || 'no report' : e.text
    const label = e.key.startsWith('event-') && e.events.some(event => event.kind === 'conformance.recording-failed') ? 'Check results' : e.key.startsWith('event-') && e.events.some(event => event.kind === 'main.merged') ? 'Brought up to date with main' : s?.type === 'agent/pi' ? 'Agent worked' : s?.type === 'agent/review' ? 'Reviewer' : s?.type === 'approval' ? 'Approval' : s?.type === 'scm/merged' ? 'Merged' : s?.type === 'build' ? 'Built' : e.text.startsWith('Abandoned') ? 'Abandoned' : e.text.startsWith('Checks:') ? 'Checks' : 'Agent worked'
    return <li key={key} id={key} className="ch-detail-row ch-story-entry">
      {time(e.at)}<div className="ch-event-label">{label}</div>
      <div className="ch-detail-facts"><div className={`ch-story-line${s?.type === 'agent/review' ? ' ch-review-text' : ''}`}>{s?.type === 'agent/review' ? <ResultMark text={facts.replace(/^Reviewer: /, '')} /> : e === mergedEntry && view.change.landedSha ? `merged at ${view.change.landedSha.slice(0, 12)}` : facts.replace(/^(Reviewer|Approval|Landing): |^Agent worked,? ?|^Abandoned ?/, '') || 'Finished'}</div>
        {e === previewEntry && <span id="preview" />}{e === reviewEntry && <span id="review" />}
        <TechnicalWords shown={technical}>{e.detail.run.workflowName} v{e.detail.run.workflowVersion} · run {e.detail.run.number}{s && ` · ${s.name} · ${s.type}`}{s?.type === 'agent/pi' && s.evidence.model && ` · ${s.evidence.model}`}{s?.type === 'approval' && s.config.means && ` · ${s.config.means}`}</TechnicalWords>
      </div>
      <div className="ch-detail-actions">
        {agentWork ? <Link to={workRecordHref(view.change.id!, e)}>session</Link> : <EvidenceLink id={key} runId={e.detail.run.id!}>{s?.type === 'agent/review' ? 'full review' : 'details'}</EvidenceLink>}
        {e.actionStep && <StepActions step={e.actionStep} onChange={reload} />}
        {agentWork && <Link to={`/changes/${view.change.id}?tab=changes`}>code</Link>}
        {e === reportEntry && fullReport && <EvidenceLink id="summary">report</EvidenceLink>}
        <Records records={agentWork ? [] : e.records} runId={e.detail.run.id!} />
      </div>
    </li>
  }
  const u = view.usage
  return <div className="ch-details">
    {[...ids].map(id => <TraceRead key={id} id={id} receive={receive} />)}
    {(view.evidence?.releases ?? []).map(r => <ReleaseRead key={r.number} slug={view.projectSlug} number={r.number} receive={receiveRelease} />)}
    {phases.map((phase, i) => {
      const rows: { at: string; render: () => ReactNode }[] = []
      if (i === 0) rows.push({ at: view.change.createdAt, render: () => <li key="request" className="ch-detail-row ch-story-entry ch-request-row">{time(view.change.createdAt)}<div className="ch-event-label">Asked</div><RequestEntry view={view} current={current} reload={reload} /></li> })
      // Consecutive checks share a sub-table; never combine different phases.
      for (let n = 0; n < phase.entries.length; n++) {
        const e = phase.entries[n]
        if (!e.text.startsWith('Checks:')) { rows.push({ at: e.at, render: () => entryRow(e) }); continue }
        const checks = phase.entries.filter(e => e.text.startsWith('Checks:'))
        if (e !== checks[0]) continue
        rows.push({ at: e.at, render: () => <li key={e.key} className="ch-detail-row ch-story-entry" id={!phases.slice(0, i).some(p => p.entries.some(e => e.text.startsWith('Checks:'))) && e === phase.entries.find(e => e.text.startsWith('Checks:')) ? 'checks' : undefined}>{time(e.at)}<div className="ch-event-label">Checks</div><div className="ch-detail-facts ch-check-facts"><table className="ch-items-table" aria-label="Checks"><tbody>{checks.map(check => <CheckRow key={check.key} entry={check} technical={technical} reload={reload} preview={check === previewEntry} />)}</tbody></table></div></li> })
      }
      for (const row of extraRows) {
        const target = row.key === 'advance' ? (phases.map((p, index) => (p.entries[0]?.at || view.change.createdAt) <= row.at ? index : -1).filter(index => index >= 0).at(-1) ?? 0) : phases.length - 1
        if (i !== target) continue
        rows.push({ at: row.at, render: () => <li key={row.key} id={row.key} className="ch-detail-row ch-story-entry">{time(row.at)}<div className="ch-event-label">{row.label}</div><div className="ch-detail-facts ch-story-line">{row.facts}</div><div /></li> })
      }
      return <section key={phase.key} className="ch-change-card ch-phase"><h2>{phase.title}</h2><ol className="ch-story" aria-label={phase.title}>{rows.sort((a, b) => (a.at || '\uffff').localeCompare(b.at || '\uffff')).map(row => row.render())}</ol>
        {!phase.landing && latest && !phases.slice(i + 1).some(p => !p.landing) && <div className="ch-story-attach"><button className="link" onClick={() => setAdding(!adding)}>{adding ? 'Cancel' : 'Attach a record'}</button>{adding && <AddArtifact runId={latest.run.id!} onChange={() => { setAdding(false); reload() }} />}</div>}
      </section>
    })}
    <section className="ch-change-card"><h2>About this change</h2><StateRecord view={view} /><table className="ch-facts-table"><tbody>
      {view.change.branch && <tr><th scope="row">Branch</th><td><code className="tech-line">{view.change.branch}</code></td></tr>}
      {view.change.baseSha && <tr><th scope="row">Started from</th><td><code className="tech-line">{view.change.baseSha}</code></td></tr>}
      {view.change.type && <tr><th scope="row">Kind</th><td>{view.change.type}</td></tr>}
      {u && (u.total > 0 || u.costMicros > 0 || u.unpriced > 0) && <tr><th scope="row">Cost</th><td>{fmtTokens(u.total)} tokens{` · ${fmtPrice(u.costMicros, u.unpriced)}`}</td></tr>}
      <Attachments changeId={view.change.id!} readOnly={['shipped', 'abandoned'].includes(view.change.state)} />
    </tbody></table></section>
  </div>
}

// The pass/fail mark carries the result colour; the words after it stay plain.
function ResultMark({ text }: { text: string }) {
  const tone = text.startsWith('✓') ? 'ok' : text.startsWith('✗') ? 'bad' : text.startsWith('unknown') ? 'warn' : undefined
  if (!tone) return <>{text}</>
  return <><span className={`ch-mark ${tone}`}>{tone === 'warn' ? 'unknown' : text[0]}</span>{text.slice(tone === 'warn' ? 7 : 1)}</>
}

// A pass with nothing more to say and no report beside it would be a lone
// tick; it says "passed", so the row never reads as empty.
const passedWords = (result: string, records: number) => result === '✓' && records === 0 ? '✓ passed' : result

function CheckRow({ entry, technical, reload, preview }: { entry: StoryEntry; technical: boolean; reload: () => void; preview: boolean }) {
  const s = entry.step!
  const na = s.status === 'not_applicable' && !s.overrideBy
  const duration = s.startedAt && s.finishedAt ? Math.max(0, Date.parse(s.finishedAt) - Date.parse(s.startedAt)) : undefined
  return <><tr id={`entry-${entry.key}`} className={na ? 'muted' : undefined}>
    <th scope="row">{preview && <span id="preview" />}{stepTitle(s)}<TechnicalWords shown={technical}>{s.name} · {s.type} · run {entry.detail.run.number}</TechnicalWords></th>
    <td><div className="ch-check-result">{na ? "doesn't apply: no matching part" : <ResultMark text={passedWords(checkResult(s), entry.records.length)} />}<Records records={entry.records} runId={entry.detail.run.id!} /></div>
      {na && <TechnicalWords shown={technical}>{checkResult(s)}</TechnicalWords>}
      {!na && !s.overrideBy && ['fail', 'error', 'needs_human'].includes(s.status) && !!s.evidence.actions?.length && <p className="ch-story-reason">To satisfy this check: {s.evidence.actions?.join(' ')}</p>}
    </td>
    <td className="ch-check-duration">{duration != null && `${Math.round(duration / 1000)}s`}</td>
    <td className="ch-check-actions"><div className="ch-check-links"><EvidenceLink id={`entry-${entry.key}`} runId={entry.detail.run.id!}>details</EvidenceLink>{entry.actionStep && <StepActions step={entry.actionStep} onChange={reload} />}</div></td>
  </tr>

  </>
}

function EntryEvidence({ entry, view, reload }: { entry: StoryEntry; view: ChangeView; reload: () => void }) {
  const [technical] = useTechnicalDetail()
  return <div className="ch-story-evidence">
    {entry.step && (entry.current ? <StepCard evidenceOnly step={entry.step} head={view.head} onChange={reload} hideApproveForm /> : <pre className="log">{JSON.stringify(entry.step.evidence, null, 2)}</pre>)}
    {entry.session && <p className="small">Session: <code>{entry.session}</code></p>}
    {/* Decision records belong where the workflow asks for one, or beside work that already links some. */}
    {entry.step?.type === 'adr/require' && <AdrLinks view={view} onChange={reload} />}
    {entry.step?.type === 'approval' && !!view.evidence?.targets.length && <div>{view.evidence.targets.map(t => <ListRow className="approval-target-row" key={`${t.block}:${t.check}`} identity={<><strong>{t.title || t.check}</strong><span>{t.expects}</span></>} />)}</div>}
    {technical && !!entry.events.length && <pre className="log small">{JSON.stringify(entry.events, null, 2)}</pre>}
  </div>
}

function Report({ text, changeId }: { text: string; changeId: string }) {
  const { run, busy, err } = useAction()
  return <div id="summary"><div className="ch-request">{text}</div><button className="link" disabled={busy} onClick={() => run(() => post(`/changes/${changeId}/distill`, {}))}>{busy ? 'Writing…' : 'Write it again'}</button>{err && <p className="error">{err}</p>}</div>
}
