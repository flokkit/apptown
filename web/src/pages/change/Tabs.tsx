import { Status, STATUS_LIGHT } from '../../Status'
import { PRODUCT } from '../../brand'
import { FormEvent, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { renderMd } from '../../md'
import { TryIt } from '../../TryIt'
import { Adr, Artifact, ChangeView, RunDetail, Step, isStep, patch, post } from '../../api'
import { sameCommitWords } from '../../Screenshots'
import { landingMeansFor } from '../../DecisionForm'
import { useAction, useGet } from '../../data'
import { fmtStamp } from '../../format'
import { checkedWords } from '../../runtime'
import type { RunTrace } from '../../wire.gen'
import { StepCard } from './Steps'
import { eventSentence, eventTitle } from './events'

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
        <div className="row ch-runpick" role="navigation" aria-label="runs of this change">
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
      {detail && <details open={!!focusStep || !!runId}><summary>Step details</summary><RunWork view={view} detail={detail} reload={reload} focusStep={focusStep} deciding={detail.run.id === view.detail?.run.id ? deciding : undefined} /></details>}
    </div>
  )
}

function RunWork({ view, detail, reload, focusStep, deciding }: { view: ChangeView; detail: RunDetail; reload: () => void; focusStep?: string; deciding?: string }) {
  const run = detail.run
  const currentIdx = detail.stages.indexOf(run.currentStage)
  const running = run.status === 'running'
  const currentSteps = detail.steps.filter((s) => s.stage === run.currentStage)
  const earlierSteps = detail.steps.filter((s) => s.stage !== run.currentStage)
  const hasAdrCheck = currentSteps.some((s) => s.type === 'adr/require')
  const isLatest = run.id === view.detail?.run.id
  return (
    <div>
      <div className="row">
<h3 style={{ margin: 0 }}>{running ? 'Current work' : 'Finished work'}</h3>
        <Status word={running && isLatest ? view.statusLabel : run.statusLabel} tone={STATUS_LIGHT[running && isLatest ? view.statusLabel : run.statusLabel]} />
        <details><summary>Run details</summary>{run.workflowName} · version {run.workflowVersion} · run {run.number} · started {fmtStamp(run.createdAt)}</details>
      </div>
      <details><summary>Stages</summary><div className="stagebar">
        {detail.stages.map((s, i) => (
          <span key={s} className={'stage' + (i === currentIdx && running ? ' current' : i < currentIdx || run.status === 'done' ? ' past' : '')}>{s}</span>
        ))}
        <span className={'stage' + (run.status === 'done' ? ' past' : '')}>done</span>
      </div></details>

      {running ? (
        <>
          <h3>Steps</h3>
          {currentSteps.map((s) => <StepCard key={s.id} step={s} head={view.head} onChange={reload} expanded={focusStep === s.id} awaitingDecision={isLatest && view.approval?.viewer === 'yes' && view.approval.stepId === s.id} hideApproveForm={s.id === deciding} landingMeans={landingMeansFor(detail, s)} />)}
          {earlierSteps.length > 0 && (
            <details open={earlierSteps.some((s) => s.id === focusStep)}>
              <summary>Earlier stages ({earlierSteps.length} steps)</summary>
              {earlierSteps.map((s) => <StepCard key={s.id} step={s} head={view.head} onChange={reload} expanded={focusStep === s.id} />)}
            </details>
          )}
        </>
      ) : detail.steps.map((s) => <StepCard key={s.id} step={s} head={view.head} onChange={reload} expanded={focusStep === s.id} />)}

      {isLatest && (hasAdrCheck || view.adrs.length > 0) && (
        <details open={hasAdrCheck}>
          <summary>Linked decisions ({view.adrs.length})</summary>
          <AdrLinks view={view} onChange={reload} />
        </details>
      )}

      <details>
        <summary>Artifacts ({detail.artifacts.length})</summary>
        {detail.artifacts.map((a) => (
          <div className="card" key={a.id}>
            <div className="row">
              <span className="chip">{a.kind === 'agent-reported' ? `reported by the agent (not checked by ${PRODUCT})` : a.kind}</span>
              <strong>{a.name || '(unnamed)'}</strong>
              {a.ref && <a href={a.ref} target="_blank" rel="noreferrer">{a.ref}</a>}
              {a.body && <span className="muted small">{a.body.length.toLocaleString()} chars</span>}
            </div>
            {a.body && <ArtifactBody artifact={a} />}
          </div>
        ))}
        <AddArtifact runId={run.id!} onChange={reload} />
      </details>
    </div>
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
      <summary>view{truncated ? ` (${((artifact.bodyBytes ?? 0) / 1024).toFixed(0)}KB, loads on open)` : ''}</summary>
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
          <Status word={a.statusLabel} tone={STATUS_LIGHT[a.statusLabel]} help={`Decision: ${a.status}.`} />
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
    <form onSubmit={add} className="row" style={{ marginTop: 6 }}>
      <input placeholder="kind (e.g. notes)" value={kind} onChange={(e) => setKind(e.target.value)} required />
      <input placeholder="name" value={name} onChange={(e) => setName(e.target.value)} />
      <input placeholder="url / ref (optional)" value={ref} onChange={(e) => setRef(e.target.value)} style={{ width: 220 }} />
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
    <div className="ch-tab-stack">
      <section>
        <h3>Preview</h3>
        <div className="row">
          <TryIt changeId={ch.id!} label={ch.state === 'shipped' ? '▶ View as of this change' : undefined} className="" />
          <span className="muted small">A preview boots this change's files on demand, apart from any deployment.</span>
        </div>
        {previews.map((p) => {
          const commit = boot?.evidence.url === p.ref ? boot?.evidence.commit : undefined
          return (
            <div key={p.id} className="small" style={{ marginTop: 6 }}>This run's preview: <a href={p.ref} target="_blank" rel="noreferrer">{p.ref}</a>
              {commit && <span className="muted"> · built from commit <code>{commit.slice(0, 7)}</code> ({sameCommitWords(commit, view.head).text})</span>}
            </div>
          )
        })}
      </section>
      {mrs.length > 0 && (
        <section>
          <h3>Merge requests</h3>
          {mrs.map((m) => <div key={m.id}><a href={m.ref} target="_blank" rel="noreferrer">{m.name || m.ref} ↗</a></div>)}
        </section>
      )}
      <section>
        <h3>Releases</h3>
        {releases.length === 0 && releaseLinks.length === 0 && <div className="muted">No release is recorded for this change.</div>}
        {releases.map((r) => (
          <div key={r.number} className="row" style={{ marginBottom: 4 }}>
            <Link to={`/projects/${view.projectSlug}/releases/${r.number}`}>Release #{r.number}</Link>
            <span className="muted small">to {r.deployment} · {r.statusLabel}</span>
            {r.serving && <span className="chip pass">running now</span>}
            {!r.serving && r.observed && <span className="muted small" title={r.reason}>
              {r.observed === 'stopped' ? 'stopped' : r.observed === 'missing' ? 'its container is missing' : `running: ${r.observed}`}
            </span>}
            {r.observed && <span className="muted small">{checkedWords(r)}</span>}
          </div>
        ))}
        {releaseLinks.map((a) => (
          <div key={a.id} className="small"><a href={a.ref} target="_blank" rel="noreferrer">Open the recorded release ({a.name}) ↗</a></div>
        ))}
      </section>
    </div>
  )
}

// ---- History ---------------------------------------------------------------

// Every root run this change had, and the latest run's event log: the
// order things happened in, as recorded.
export function HistoryTab({ view, runHref }: { view: ChangeView; runHref: (runId: string) => string }) {
  const [count, setCount] = useState(10)
  const runs = view.runs ?? []
  const events = view.detail?.events ?? []
  return (
    <div className="ch-tab-stack">
      <section>
        <h3>Runs ({runs.length})</h3>
        {runs.length === 0 ? <div className="muted">No run has started on this change.</div> : (
          <table className="stack">
            <thead><tr><th>Run</th><th>Workflow</th><th>Role</th><th>Result</th><th>Updated</th></tr></thead>
            <tbody>
              {runs.slice(0, count).map((r) => (
                <tr key={r.id}>
                  <td><Link className="tap-open" to={runHref(r.id!)} title={`started ${fmtStamp(r.createdAt)}`}>run {r.number}</Link></td>
<td className="stack-lead"><details><summary>Workflow details</summary>{r.workflowName}</details></td>
                  <td className="muted">{r.role ?? ''}</td>
                  <td><Status word={r.statusLabel} tone={STATUS_LIGHT[r.statusLabel]} /></td>
                  <td className="muted small">{fmtStamp(r.updatedAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {runs.length > count && <button onClick={() => setCount(n => n + 10)}>Show 10 more runs</button>}
      </section>
      <section>
        <h3>Event log{view.detail ? ` of run ${view.detail.run.number}` : ''} ({events.length})</h3>
        {events.length === 0 ? <div className="muted">No events recorded.</div> : (
          <table className="stack event-log">
            <tbody>
              {[...events].reverse().slice(0, count).map((e) => (
                <tr key={e.id}>
                  <td className="muted small">{fmtStamp(e.at)}</td>
                  <td className="small">{eventTitle(e.kind)}</td>
                  <td className="muted small"><details><summary>Details</summary>{eventSentence(e) ?? <pre className="event-payload">{e.kind}{'\n'}{JSON.stringify(e.payload, null, 2)}</pre>}</details></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {events.length > count && <button onClick={() => setCount(n => n + 10)}>Show 10 more</button>}
      </section>
    </div>
  )
}
