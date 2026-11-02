import { Section } from '../../Section'
import { Status } from '../../Status'
import { PRODUCT } from '../../brand'
import { FormEvent, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { renderMd } from '../../md'
import { TryIt } from '../../TryIt'
import { Adr, Artifact, ChangeView, RunDetail, Step, isStep, patch, post } from '../../api'
import { sameCommitWords } from '../../Screenshots'
import { landingMeansFor } from '../../DecisionForm'
import { checkTypes } from './Evidence'
import { useAction, useGet } from '../../data'
import { fmtStamp } from '../../format'
import { checkedWords } from '../../runtime'
import type { RunTrace } from '../../wire.gen'
import { StepCard } from './Steps'
import { eventSentence, eventTitle, routineEvent } from './events'

// The tabs under a change's evidence: everything a person reads to dig in,
// none of it needed to know what the change is doing or what to do next.

// ---- Work ------------------------------------------------------------------

// The run a tab shows: the page's latest run as it polls, or another root
// run of this change read from its trace.
export function useRunDetail(view: ChangeView, runId?: string): { detail: RunDetail | null; err: string } {
  const latest = view.detail
  const other = runId != null && runId !== latest?.run.id ? runId : null
  const trace = useGet<RunTrace>(other != null ? `/runs/${other}/trace` : null)
  if (other == null) return { detail: latest, err: '' }
  const t = trace.data
  if (!t || !t.run) return { detail: null, err: trace.err }
  return {
    // A trace is the record, not the run's current offer: it carries no acts.
    detail: { run: t.run, stages: t.stages, steps: t.steps.filter(Boolean) as Step[], events: t.events as RunDetail['events'], artifacts: t.artifacts, landingMeans: [], next: [] },
    err: trace.err,
  }
}

export function WorkTab({ view, reload, runId, focusStep, deciding, runHref }: {
  view: ChangeView; reload: () => void
  runId?: string; focusStep?: string
  // deciding: the approval the state panel already shows a decision for.
  deciding?: string
  runHref: (runId: string) => string
}) {
  const { detail, err } = useRunDetail(view, runId)
  useEffect(() => {
    if (focusStep == null || !detail) return
    requestAnimationFrame(() => document.getElementById(`step-${focusStep}`)?.scrollIntoView({ block: 'center', behavior: 'smooth' }))
  }, [focusStep, detail?.run.id]) // eslint-disable-line react-hooks/exhaustive-deps
  const [runCount, setRunCount] = useState(10)
  const runs = view.runs ?? []
  return (
    <div>
      {runs.length > 1 && (
        <div className="row ch-runpick" role="navigation" aria-label="runs of this change" style={{ marginTop: 24 }}>
          <span className="muted small">Runs:</span>
          {runs.slice(0, runCount).map((r) => (
            <Link key={r.id} to={runHref(r.id!)} className={'ch-runchip' + (r.id === (detail?.run.id ?? view.detail?.run.id) ? ' on' : '')}>
              {r.role === 'landing' ? 'Landing' : 'Work'} · {r.statusLabel}
            </Link>
          ))}
        {runs.length > runCount && <button onClick={() => setRunCount(n => n + 10)}>Show 10 more runs</button>}
        </div>
      )}
      {err && <div className="error">The run could not be read: {err}</div>}
      {!detail && !err && (view.detail ? <div className="muted">Reading the run…</div> : <div className="muted">No run has started on this change yet.</div>)}
      {detail && <RunWork view={view} detail={detail} reload={reload} focusStep={focusStep} deciding={detail.run.id === view.detail?.run.id ? deciding : undefined} />}
    </div>
  )
}

function RunWork({ view, detail, reload, focusStep, deciding }: { view: ChangeView; detail: RunDetail; reload: () => void; focusStep?: string; deciding?: string }) {
  const run = detail.run
  const currentIdx = detail.stages.indexOf(run.currentStage)
  const running = run.status === 'running'
  const currentSteps = detail.steps.filter((s) => s.stage === run.currentStage)
  // A check in this stage that failed and was not accepted anyway holds the approval.
  const failedCheck = currentSteps.find((s) => checkTypes.has(s.type) && (s.status === 'fail' || s.status === 'error') && !s.overrideBy)?.name
  const earlierSteps = detail.steps.filter((s) => s.stage !== run.currentStage)
  const hasAdrCheck = currentSteps.some((s) => s.type === 'adr/require')
  const isLatest = run.id === view.detail?.run.id
  const stageNote = `${run.workflowName} · version ${run.workflowVersion} · run ${run.number} · started ${fmtStamp(run.createdAt)}`
  const stages = detail.stages.length > 1 && (
    <div className="stagebar" aria-label="stages">
      {detail.stages.map((s, i) => (
        <span key={s} className={'stage' + (i === currentIdx && running ? ' current' : i < currentIdx || run.status === 'done' ? ' past' : '')}>{s}</span>
      ))}
      <span className={'stage' + (run.status === 'done' ? ' past' : '')}>done</span>
    </div>
  )
  return (
    <>
      <Section title={running ? 'Current work' : 'Finished work'} count={detail.steps.length} note={<>{stageNote}{stages}</>}
        action={<Status word={running && isLatest ? view.statusLabel : run.statusLabel} />}>
        {running ? (
          <>
            {currentSteps.map((s) => <StepCard key={s.id} step={s} head={view.head} onChange={reload} expanded={focusStep === s.id} awaitingDecision={isLatest && view.approval?.viewer === 'yes' && view.approval.stepId === s.id} hideApproveForm={s.id === deciding} landingMeans={landingMeansFor(detail, s)} failedCheck={failedCheck} />)}
            {earlierSteps.length > 0 && (
              <details open={earlierSteps.some((s) => s.id === focusStep)} style={{ padding: '10px 0' }}>
                <summary>Earlier stages ({earlierSteps.length} steps)</summary>
                {earlierSteps.map((s) => <StepCard key={s.id} step={s} head={view.head} onChange={reload} expanded={focusStep === s.id} />)}
              </details>
            )}
          </>
        ) : detail.steps.map((s) => <StepCard key={s.id} step={s} head={view.head} onChange={reload} expanded={focusStep === s.id} />)}
      </Section>

      {isLatest && (hasAdrCheck || view.adrs.length > 0) && (
        <Section title="Linked decisions" count={view.adrs.length}>
          <AdrLinks view={view} onChange={reload} />
        </Section>
      )}

      <Artifacts detail={detail} reload={reload} />
    </>
  )
}

// The run's records: one row each, a long body folded until asked for, and
// attaching another record one quiet action away.
function Artifacts({ detail, reload }: { detail: RunDetail; reload: () => void }) {
  const [adding, setAdding] = useState(false)
  const action = <button className="link" aria-expanded={adding} onClick={() => setAdding(!adding)}>{adding ? 'Cancel' : 'Attach a record'}</button>
  // A finished run with no records has no records section; a running one
  // keeps the door to attach one.
  if (detail.artifacts.length === 0 && !adding) return detail.run.status === 'running' ? <Section title="Records" action={action}><p className="muted">No records attached to this run.</p></Section> : null
  return (
    <Section title="Records" count={detail.artifacts.length || undefined} action={action}>
      {adding && <AddArtifact runId={detail.run.id!} onChange={() => { setAdding(false); reload() }} />}
      {detail.artifacts.map((a) => (
        <div className="card" key={a.id}>
          <div className="row" style={{ flexWrap: 'wrap' }}>
            <strong>{a.name || '(unnamed)'}</strong>
            <span className="muted small">{a.kind === 'agent-reported' ? `reported by the agent (not checked by ${PRODUCT})` : a.kind}</span>
            {a.ref && <a href={a.ref} target="_blank" rel="noreferrer" style={{ overflowWrap: 'anywhere' }}>{a.ref}</a>}
          </div>
          {a.body && <ArtifactBody artifact={a} />}
        </div>
      ))}
    </Section>
  )
}

// Artifact bodies over the embed cap load in full on expand only — the
// change page polls its payload, and multi-MB scan reports must not ride it.
function ArtifactBody({ artifact }: { artifact: Artifact }) {
  const truncated = (artifact.bodyBytes ?? 0) > artifact.body.length
  const [opened, setOpened] = useState(false)
  const fullQ = useGet<Artifact>(opened && truncated ? `/artifacts/${artifact.id}` : null)
  const full = fullQ.data?.body ?? null
  const body = full ?? artifact.body
  const isJson = body.trimStart().startsWith('{') || body.trimStart().startsWith('[')
  return (
    <details onToggle={(e) => { if ((e.target as HTMLDetailsElement).open) setOpened(true) }}>
      <summary className="small">Show the record ({truncated ? `${((artifact.bodyBytes ?? 0) / 1024).toFixed(0)} KB, loads on open` : `${artifact.body.length.toLocaleString()} characters`})</summary>
      {fullQ.err && <div className="error small">Showing the first part only; the rest could not be read: {fullQ.err}</div>}
      {truncated && full === null
        ? <div className="muted small">Reading the full body…</div>
        : isJson
          ? <pre className="log" style={{ maxHeight: 400 }}>{body}</pre>
          : <div className="md" dangerouslySetInnerHTML={{ __html: renderMd(body) }} />}
    </details>
  )
}

function AdrLinks({ view, onChange }: { view: ChangeView; onChange: () => void }) {
  const { data: all } = useGet<Adr[]>(`/projects/${view.projectSlug}/adrs`)
  const [pick, setPick] = useState('')
  const [title, setTitle] = useState('')
  const { run, err } = useAction(onChange)
  const unlinked = (all ?? []).filter((a) => a.changeId !== view.change.id)
  const link = () => { if (pick) run(() => patch(`/adrs/${pick}`, { changeId: view.change.id })) }
  const createLinked = async (e: FormEvent) => {
    e.preventDefault()
    await run(() => post(`/projects/${view.projectSlug}/adrs`, { title, changeId: view.change.id }))
    setTitle('')
  }
  return (
    <div>
      {view.adrs.map((a) => (
        <div className="card row" key={a.id}>
          <Status word={a.statusLabel} />
          <Link to={`/adrs/${a.id}`}>ADR-{String(a.number).padStart(3, '0')} {a.title}</Link>
          <span className="muted small">{a.tags}</span>
        </div>
      ))}
      <div className="row" style={{ marginTop: 4 }}>
        <select value={pick} onChange={(e) => setPick(e.target.value)}>
          <option value="">link existing ADR…</option>
          {unlinked.map((a) => <option key={a.id} value={a.id ?? ''}>ADR-{String(a.number).padStart(3, '0')} {a.title} ({a.status})</option>)}
        </select>
        <button onClick={link} disabled={!pick}>Link</button>
        <form onSubmit={createLinked} className="row">
          <input placeholder="…or new ADR title" value={title} onChange={(e) => setTitle(e.target.value)} />
          <button type="submit" disabled={!title.trim()}>Create draft</button>
        </form>
      </div>
      {err && <div className="error">{err}</div>}
    </div>
  )
}

function AddArtifact({ runId, onChange }: { runId: string; onChange: () => void }) {
  const [kind, setKind] = useState('')
  const [name, setName] = useState('')
  const [ref, setRef] = useState('')
  const { run, err } = useAction(onChange)
  const add = async (e: FormEvent) => {
    e.preventDefault()
    if (await run(() => post(`/runs/${runId}/artifacts`, { kind, name, ref })) !== undefined) { setKind(''); setName(''); setRef('') }
  }
  return (
 <form onSubmit={add} className="row" style={{ padding: '12px 0', flexWrap: 'wrap' }}>
      <input placeholder="Kind (for example notes)" value={kind} onChange={(e) => setKind(e.target.value)} required />
      <input placeholder="Name" value={name} onChange={(e) => setName(e.target.value)} />
      <input placeholder="Link (optional)" value={ref} onChange={(e) => setRef(e.target.value)} style={{ width: 220, maxWidth: '100%' }} />
      <button type="submit">Attach</button>
      {err && <span className="error">{err}</span>}
    </form>
  )
}

// ---- Previews and releases -------------------------------------------------

// What a person can open: the change's own preview (booted on demand), the
// latest run's preview while it runs, the merge request, and the releases
// this change shipped in.
export function ReleasesTab({ view }: { view: ChangeView }) {
  const ch = view.change
  const d = view.detail
  const arts = d?.artifacts ?? []
  const of = (kind: string) => arts.filter((a) => a.kind === kind && a.ref)
  const previews = d?.run.status === 'running' ? of('preview') : []
  const boot = d ? d.steps.filter(isStep('preview')).filter((s) => s.evidence?.url).at(-1) : undefined
  const mrs = of('mr')
  const releaseLinks = of('release')
  const releases = view.evidence?.releases ?? []
  return (
    <>
      <Section title="Preview">
        <div className="card row" style={{ display: 'flex' }}><TryIt changeId={ch.id!} label={ch.state === 'shipped' ? 'View as of this change' : undefined} className="" /></div>
        {previews.map((p) => {
          const commit = boot?.evidence.url === p.ref ? boot?.evidence.commit : undefined
          return (
            <div key={p.id} className="card">This run's preview: <a href={p.ref} target="_blank" rel="noreferrer" style={{ overflowWrap: 'anywhere' }}>{p.ref}</a>
              {commit && <span className="muted small"> · built from commit <code>{commit.slice(0, 7)}</code> ({sameCommitWords(commit, view.head).text})</span>}
            </div>
          )
        })}
      </Section>
      {mrs.length > 0 && (
        <Section title="Merge requests" count={mrs.length}>
          {mrs.map((m) => <div key={m.id} className="card"><a href={m.ref} target="_blank" rel="noreferrer">{m.name || m.ref} ↗</a></div>)}
        </Section>
      )}
      <Section title="Releases" count={releases.length + releaseLinks.length || undefined}>
        {releases.length === 0 && releaseLinks.length === 0 && <p className="muted">No release is recorded for this change.</p>}
        {releases.map((r) => (
          <Link key={r.number} className="card row" style={{ display: 'flex', flexWrap: 'wrap' }} to={`/projects/${view.projectSlug}/releases/${r.number}`}>
            <span className="row-status"><Status word={r.statusLabel} /></span>
            <span style={{ flex: 1, minWidth: 0 }}>Release #{r.number} <span className="muted small">to {r.deployment}</span></span>
            {r.serving && <Status word="running" observation />}
            {!r.serving && r.observed && <span className="muted small" title={r.reason}>
              {r.observed === 'stopped' ? 'stopped' : r.observed === 'missing' ? 'its container is missing' : `running: ${r.observed}`}
            </span>}
            {r.observed && <span className="muted small">{checkedWords(r)}</span>}
          </Link>
        ))}
        {releaseLinks.map((a) => (
          <div key={a.id} className="card"><a href={a.ref} target="_blank" rel="noreferrer">Open the recorded release ({a.name}) ↗</a></div>
        ))}
      </Section>
    </>
  )
}

// ---- History ---------------------------------------------------------------

// Every root run this change had, and the latest run's event log: the
// order things happened in, as recorded. Routine plumbing (connections
// checked, steps starting and finishing, kinds without a plain name) is
// kept apart, folded at the end with its exact record.
export function HistoryTab({ view, runHref }: { view: ChangeView; runHref: (runId: string) => string }) {
  const [count, setCount] = useState(10)
  const [eventCount, setEventCount] = useState(20)
  const runs = view.runs ?? []
  const events = [...(view.detail?.events ?? [])].reverse()
  const shown = events.filter((e) => !routineEvent(e.kind))
  const routine = events.filter((e) => routineEvent(e.kind))
  // A run still going on the latest attempt is in whatever state the change
  // is in (failed, waiting for you), not merely "in progress".
  const result = (r: typeof runs[number]) => r.status === 'running' && r.id === view.detail?.run.id ? view.statusLabel : r.statusLabel
  return (
    <>
      <Section title="Runs" count={runs.length}>
        {runs.length === 0 && <p className="muted">No run has started on this change.</p>}
        {runs.slice(0, count).map((r) => (
          <Link key={r.id} to={runHref(r.id!)} className="card row" style={{ display: 'flex' }} title={`started ${fmtStamp(r.createdAt)}`}>
            <span className="row-status"><Status word={result(r)} /></span>
            <span style={{ flex: 1, minWidth: 0 }}>{r.role === 'landing' ? 'Landing' : 'Work'} <span className="muted small">{r.workflowName} · run {r.number}</span></span>
            <span className="muted small">{fmtStamp(r.updatedAt)}</span>
          </Link>
        ))}
        {runs.length > count && <p><button className="link" onClick={() => setCount(n => n + 10)}>Show 10 more runs</button></p>}
      </Section>
      <Section title="Activity" count={shown.length}>
        {shown.length === 0 && <p className="muted">No events recorded.</p>}
        <div className="event-log">
          {shown.slice(0, eventCount).map((e) => <EventRow key={e.id} e={e} />)}
        </div>
        {shown.length > eventCount && <p><button className="link" onClick={() => setEventCount(n => n + 20)}>Show 20 more</button></p>}
        {routine.length > 0 && (
          <details className="ch-routine" style={{ padding: '10px 0' }}>
            <summary className="small">{routine.length} routine {routine.length === 1 ? 'event' : 'events'}</summary>
            <div className="event-log">{routine.map((e) => <EventRow key={e.id} e={e} raw />)}</div>
          </details>
        )}
      </Section>
    </>
  )
}

type RunEvent = NonNullable<ChangeView['detail']>['events'][number]

function EventRow({ e, raw }: { e: RunEvent; raw?: boolean }) {
  const p = (e.payload ?? {}) as Record<string, unknown>
  const named = typeof p.step === 'string' ? p.step : typeof p.name === 'string' ? p.name : ''
  const said = eventSentence(e) ?? (raw ? `${e.kind} ${JSON.stringify(e.payload, null, 2)}` : named)
  return (
    <div className="ch-event">
      <span>{eventTitle(e.kind)}</span>
      <span className="ch-event-when">{fmtStamp(e.at)}</span>
      {said && (raw ? <pre className="ch-event-detail event-payload">{said}</pre> : <span className="ch-event-detail">{said}</span>)}
    </div>
  )
}
