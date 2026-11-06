import { StateRecord } from './StatePanel'
import { TechnicalWords, useTechnicalDetail } from './TechnicalDetail'
import { ReactNode, createContext, useCallback, useContext, useEffect, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import type { Artifact, ChangeView, RunDetail } from '../../api'
import { post } from '../../api'
import type { ReleaseDetail, RunTrace } from '../../wire.gen'
import { Screenshots } from '../../Screenshots'
import { fmtPrice, fmtStamp, fmtTokens } from '../../format'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Attachments, RequestEntry, changeReport } from './Head'
import { StepActions, StepCard } from './Steps'
import { AddArtifact, AdrLinks, ArtifactBody } from './Tabs'
import { stepTitle } from './Evidence'
import { workRecordHref } from './WorkRecord'
import { checkResult, runStory, StoryEntry } from './story'

function TraceRead({ id, receive }: { id: string; receive: (id: string, detail: RunDetail) => void }) {
  const q = useGet<RunTrace>(`/runs/${id}/trace`, { poll: 3000 })
  useEffect(() => {
    const t = q.data
    if (t?.run) receive(id, { run: t.run, steps: t.steps.filter(Boolean) as RunDetail['steps'], stages: t.stages, events: t.events as RunDetail['events'], artifacts: t.artifacts, next: [], landingMeans: [] })
  }, [id, q.data, receive])
  return <><ReadStatus q={q} what="earlier work" /><StaleNote q={q} /></>
}
function ReleaseRead({ slug, number, receive }: { slug: string; number: number; receive: (n: number, detail: ReleaseDetail) => void }) {
  const q = useGet<ReleaseDetail>(`/projects/${slug}/releases/${number}`)
  useEffect(() => { if (q.data) receive(number, q.data) }, [number, q.data, receive])
  return <><ReadStatus q={q} what={`release #${number}`} /><StaleNote q={q} /></>
}

// A plain link opens evidence in place. It has an address, supports back/forward
// navigation, and never turns the story line into a disclosure control.
// An open piece of evidence offers "hide" in the same place, so it closes where it opened.
const OpenEvidence = createContext<{ opened: Set<string>; close: (id: string) => void }>({ opened: new Set(), close: () => {} })
function EvidenceLink({ id, children }: { id: string; children: ReactNode }) {
  const location = useLocation()
  const { opened, close } = useContext(OpenEvidence)
  if (opened.has(id)) return <button type="button" className="link" onClick={() => close(id)}>hide</button>
  return <Link to={`${location.pathname}?tab=details#${id}`}>{children}</Link>
}
function RecordLink({ artifact, open }: { artifact: Artifact; open: boolean }) {
  const label = artifact.kind === 'agent-log' ? 'agent log' : artifact.kind === 'steering' ? 'agent instructions' : artifact.kind === 'scan-report' ? 'security scan report' : artifact.kind === 'sbom' ? 'software inventory' : artifact.kind === 'approval-brief' ? 'approval brief' : artifact.kind === 'screenshot' ? 'screenshots' : artifact.name.startsWith('step-') ? `${artifact.kind.replace(/-/g, ' ')} report` : artifact.name || artifact.kind.replace(/-/g, ' ')
  return <div className="ch-story-record" id={`record-${artifact.id}`}>
    <EvidenceLink id={`record-${artifact.id}`}>{label}</EvidenceLink>
    {open && <div className="ch-story-evidence">
      <p className="small muted">{artifact.name} · {artifact.kind.replace(/-/g, ' ')} · {fmtStamp(artifact.createdAt)}</p>
      {artifact.kind === 'agent-reported' && <p>Reported by the agent.</p>}
      {artifact.ref && <a href={artifact.ref} target="_blank" rel="noreferrer">Open record</a>}
      {artifact.binary ? <Screenshots unfolded shots={{ shots: [{ artifact: artifact.id!, path: artifact.name, view: "record" }] }} /> : artifact.body && <ArtifactBody artifact={artifact} />}
    </div>}
  </div>
}

function Records({ records, opened }: { records: Artifact[]; opened: Set<string> }) {
  // A record written again (an approval brief asked for twice) shows once: its latest copy.
  const latest = [...new Map([...records].sort((a, b) => (a.createdAt ?? '').localeCompare(b.createdAt ?? '')).map(a => [`${a.kind}/${a.name}`, a])).values()]
  return <>{latest.map(a => <RecordLink key={a.id} artifact={a} open={opened.has(`record-${a.id}`)} />)}</>
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

export function ChangeDetails({ view, current, reload, focusStep, runId }: { view: ChangeView; current: boolean; reload: () => void; focusStep?: string; runId?: string }) {
  const [technical] = useTechnicalDetail()
  const [traces, setTraces] = useState<Record<string, RunDetail>>({})
  const [releases, setReleases] = useState<Record<number, ReleaseDetail>>({})
  const receive = useCallback((id: string, detail: RunDetail) => setTraces(old => ({ ...old, [id]: detail })), [])
  const receiveRelease = useCallback((n: number, detail: ReleaseDetail) => setReleases(old => ({ ...old, [n]: detail })), [])
  const [adding, setAdding] = useState(false)
  const location = useLocation()
  const [opened, setOpened] = useState<Set<string>>(new Set())
  useEffect(() => { if (location.hash) setOpened(old => new Set([...old, decodeURIComponent(location.hash.slice(1))])) }, [location.hash])
  const navigate = useNavigate()
  const close = useCallback((id: string) => {
    setOpened(old => { const next = new Set(old); next.delete(id); if (id.startsWith('entry-')) next.delete('checks'); return next })
    // Drop the address's anchor so the same link opens it again.
    if (decodeURIComponent(location.hash.slice(1)) === id) navigate(`${location.pathname}${location.search}`, { replace: true })
  }, [location.hash, location.pathname, location.search, navigate])
  const details = [...Object.values(traces).filter(d => d.run.id !== view.detail?.run.id), ...(view.detail ? [view.detail] : [])]
  const ids = new Set([...(view.runs ?? []).map(r => r.id!), ...(runId ? [runId] : []), ...details.flatMap(d => d.steps.flatMap(s => s.type === 'workflow/call' && s.evidence.childRunId ? [s.evidence.childRunId] : []))])
  ids.delete(view.detail?.run.id ?? '')
  const story = details.flatMap(runStory).sort((a, b) => a.at.localeCompare(b.at))
  const phases = storyPhases(story, view.runs ?? [])
  if (!phases.length || phases[0].landing) phases.unshift({ key: 'request', title: 'Work', landing: false, entries: [] })
  const latest = view.detail
  const previewEntry = [...story].reverse().find(e => e.step?.type === 'preview') || story.find(e => e.step?.type === 'agent/pi')
  const reviewEntry = [...story].reverse().find(e => e.step?.type === 'agent/review')
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
    const target = focusStep ? `step-${focusStep}` : decodeURIComponent(location.hash.slice(1))
    if (target) document.getElementById(target)?.scrollIntoView({ block: 'start' })
  }, [opened, focusStep, runId, story.length, location.hash])
  let previousDate = ''
  const time = (at: string) => {
    const stamp = at ? new Date(at) : null
    const date = stamp?.toLocaleDateString(undefined, { day: 'numeric', month: 'short' }) || ''
    const changed = date !== previousDate
    previousDate = date
    return <time dateTime={at}>{changed && <span>{date}</span>}{stamp?.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })}</time>
  }
  const entryEvidence = (e: StoryEntry, open: boolean) => <>
    {open && e.step?.type !== 'agent/pi' && !e.key.startsWith('work-') && <EntryEvidence entry={e} view={view} reload={reload} report={e === reportEntry ? fullReport : undefined} />}
  </>
  const entryRow = (e: StoryEntry) => {
    const key = `entry-${e.key}`, s = e.step
    const open = opened.has(key) || (!!focusStep && focusStep === s?.id) || (opened.has('summary') && e === reportEntry) || (opened.has('preview') && e === previewEntry) || (opened.has('review') && e === reviewEntry)
    const agentWork = s?.type === 'agent/pi' || e.key.startsWith('work-')
    const facts = e.key.startsWith('work-') ? (e.detail.run.id === latest?.run.id && view.diffstat?.match(/\d+ files? changed/)?.[0]) || 'no report' : e.text
    const label = e.key.startsWith('event-') && e.events.some(event => event.kind === 'main.merged') ? 'Brought up to date with main' : s?.type === 'agent/pi' ? 'Agent worked' : s?.type === 'agent/review' ? 'Reviewer' : s?.type === 'approval' ? 'Approval' : s?.type === 'scm/merged' ? 'Merged' : s?.type === 'build' ? 'Built' : e.text.startsWith('Abandoned') ? 'Abandoned' : e.text.startsWith('Checks:') ? 'Checks' : 'Agent worked'
    return <li key={key} id={key} className="ch-detail-row ch-story-entry">
      {time(e.at)}<div className="ch-event-label">{label}</div>
      <div className="ch-detail-facts"><div className={`ch-story-line${s?.type === 'agent/review' ? ' ch-review-text' : ''}`}>{s?.type === 'agent/review' ? <ResultMark text={facts.replace(/^Reviewer: /, '')} /> : e === mergedEntry && view.change.landedSha ? `merged at ${view.change.landedSha.slice(0, 12)}` : facts.replace(/^(Reviewer|Approval|Landing): |^Agent worked,? ?|^Abandoned ?/, '') || 'Finished'}</div>
        {e === previewEntry && <span id="preview" />}{e === reviewEntry && <span id="review" />}
        <TechnicalWords shown={technical}>{e.detail.run.workflowName} v{e.detail.run.workflowVersion} · run {e.detail.run.number}{s && ` · ${s.name} · ${s.type}`}{s?.type === 'agent/pi' && s.evidence.model && ` · ${s.evidence.model}`}{s?.type === 'approval' && s.config.means && ` · ${s.config.means}`}</TechnicalWords>
        {entryEvidence(e, open)}
        {agentWork && e === reportEntry && fullReport && opened.has('summary') && <Report text={fullReport} changeId={view.change.id!} />}
      </div>
      <div className="ch-detail-actions">
        {agentWork ? <Link to={workRecordHref(view.change.id!, e)}>session</Link> : <EvidenceLink id={key}>{s?.type === 'agent/review' ? 'full review' : 'details'}</EvidenceLink>}
        {e.actionStep && <StepActions step={e.actionStep} onChange={reload} />}
        {agentWork && <Link to={`/changes/${view.change.id}?tab=changes`}>code</Link>}
        {e === reportEntry && fullReport && <EvidenceLink id="summary">report</EvidenceLink>}
        <Records records={agentWork ? [] : e.records} opened={opened} />
      </div>
    </li>
  }
  const u = view.usage
  return <OpenEvidence.Provider value={{ opened, close }}><div className="ch-details">
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
        rows.push({ at: e.at, render: () => <li key={e.key} className="ch-detail-row ch-story-entry" id={!phases.slice(0, i).some(p => p.entries.some(e => e.text.startsWith('Checks:'))) && e === phase.entries.find(e => e.text.startsWith('Checks:')) ? 'checks' : undefined}>{time(e.at)}<div className="ch-event-label">Checks</div><div className="ch-detail-facts ch-check-facts"><table className="ch-items-table" aria-label="Checks"><tbody>{checks.map(check => <CheckRow key={check.key} entry={check} technical={technical} opened={opened} reload={reload} open={opened.has(`entry-${check.key}`) || opened.has('checks') || (!!focusStep && focusStep === check.step?.id) || (opened.has('preview') && check === previewEntry)} evidence={entryEvidence} preview={check === previewEntry} />)}</tbody></table></div></li> })
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
      {view.change.branch && <tr><th scope="row">Branch</th><td><code>{view.change.branch}</code></td></tr>}
      {view.change.baseSha && <tr><th scope="row">Started from</th><td><code>{view.change.baseSha}</code></td></tr>}
      {view.change.type && <tr><th scope="row">Kind</th><td>{view.change.type}</td></tr>}
      {u && (u.total > 0 || u.costMicros > 0 || u.unpriced > 0) && <tr><th scope="row">Cost</th><td>{fmtTokens(u.total)} tokens{` · ${fmtPrice(u.costMicros, u.unpriced)}`}</td></tr>}
      <Attachments changeId={view.change.id!} readOnly={['shipped', 'abandoned'].includes(view.change.state)} />
    </tbody></table></section>
  </div></OpenEvidence.Provider>
}

// The pass/fail mark carries the result colour; the words after it stay plain.
function ResultMark({ text }: { text: string }) {
  const tone = text.startsWith('✓') ? 'ok' : text.startsWith('✗') ? 'bad' : text.startsWith('unknown') ? 'warn' : undefined
  if (!tone) return <>{text}</>
  return <><span className={`ch-mark ${tone}`}>{tone === 'warn' ? 'unknown' : text[0]}</span>{text.slice(tone === 'warn' ? 7 : 1)}</>
}

function CheckRow({ entry, technical, opened, reload, open, evidence, preview }: { entry: StoryEntry; technical: boolean; opened: Set<string>; reload: () => void; open: boolean; evidence: (e: StoryEntry, open: boolean) => ReactNode; preview: boolean }) {
  const s = entry.step!
  const na = s.status === 'not_applicable' && !s.overrideBy
  const duration = s.startedAt && s.finishedAt ? Math.max(0, Date.parse(s.finishedAt) - Date.parse(s.startedAt)) : undefined
  return <><tr id={`entry-${entry.key}`} className={na ? 'muted' : undefined}>
    <th scope="row">{preview && <span id="preview" />}{stepTitle(s)}<TechnicalWords shown={technical}>{s.name} · {s.type} · run {entry.detail.run.number}</TechnicalWords></th>
    <td><div className="ch-check-result">{na ? "doesn't apply: no matching part" : <ResultMark text={checkResult(s)} />}<Records records={entry.records} opened={opened} /></div>
      {na && <TechnicalWords shown={technical}>{checkResult(s)}</TechnicalWords>}
      {!na && !s.overrideBy && ['fail', 'error', 'needs_human'].includes(s.status) && !!s.evidence.actions?.length && <p className="ch-story-reason">To satisfy this check: {s.evidence.actions?.join(' ')}</p>}
    </td>
    <td className="ch-check-duration">{duration != null && `${Math.round(duration / 1000)}s`}</td>
    <td className="ch-check-actions"><div className="ch-check-links"><EvidenceLink id={`entry-${entry.key}`}>details</EvidenceLink>{entry.actionStep && <StepActions step={entry.actionStep} onChange={reload} />}</div></td>
  </tr>
  {/* An opened check's evidence gets the whole width under its row, not the result column. */}
  {open && <tr className="ch-check-evidence-row"><td colSpan={4}>{evidence(entry, open)}</td></tr>}
  </>
}

function EntryEvidence({ entry, view, reload, report }: { entry: StoryEntry; view: ChangeView; reload: () => void; report?: string }) {
  const [technical] = useTechnicalDetail()
  return <div className="ch-story-evidence">
    {entry.step && (entry.current ? <StepCard evidenceOnly step={entry.step} head={view.head} onChange={reload} hideApproveForm /> : <pre className="log">{JSON.stringify(entry.step.evidence, null, 2)}</pre>)}
    {entry.session && <p className="small">Session: <code>{entry.session}</code></p>}
    {report && <Report text={report} changeId={view.change.id!} />}
    {/* Decision records belong where the workflow asks for one, or beside work that already links some. */}
    {entry.step?.type === 'adr/require' && <AdrLinks view={view} onChange={reload} />}
    {entry.step?.type === 'approval' && !!view.evidence?.targets.length && <table className="ch-items-table"><tbody>{view.evidence.targets.map(t => <tr key={`${t.block}:${t.check}`}><th scope="row">{t.title || t.check}</th><td>{t.expects}</td></tr>)}</tbody></table>}
    {technical && !!entry.events.length && <pre className="log small">{JSON.stringify(entry.events, null, 2)}</pre>}
  </div>
}

function Report({ text, changeId }: { text: string; changeId: string }) {
  const { run, busy, err } = useAction()
  return <div id="summary"><div className="ch-request">{text}</div><button className="link" disabled={busy} onClick={() => run(() => post(`/changes/${changeId}/distill`, {}))}>{busy ? 'Writing…' : 'Write it again'}</button>{err && <p className="error">{err}</p>}</div>
}
