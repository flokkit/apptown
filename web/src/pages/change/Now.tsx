import { Elapsed } from '../../Elapsed'
import { Spinner } from '../../Spinner'
import { Fragment, useCallback, useEffect, useMemo, useState } from 'react'
import type { ChangeView, RunDetail, Step } from '../../api'
import type { RunTrace } from '../../wire.gen'
import { useGet } from '../../data'
import { ReadStatus, StaleNote, type Gated } from '../../ReadGate'
import { fmtDuration, fmtStamp } from '../../format'
import { stepTitle } from './Evidence'
import { Link } from 'react-router-dom'
import { StepProgressFact } from './StepProgressFact'

const checks = new Set(['scan', 'tests', 'guidance/conformance'])
const failed = (s: Step) => s.progress === 'failed' || s.progress === 'unknown' || s.status === 'fail'
const done = (s: Step) => ['done', 'not_applicable'].includes(s.progress)
export const nowRunning = (view: ChangeView) => view.detail?.run.status === 'running' && view.waitingOn.kind === 'work'

// stageLabel is a stage's name as the progress strip and its panel say it.
export const stageLabel = (stage: string) => stage.replace(/-/g, ' ').replace(/^./, c => c.toUpperCase())

export function currentSteps(d: RunDetail): Step[] {
  const stage = d.run.status === 'done' ? d.stages.at(-1) : d.run.currentStage
  return d.steps.filter(s => s.stage === stage)
}

function CalledRun({ id, receive }: { id: string; receive: (id: string, reading: Gated, detail?: RunDetail) => void }) {
  const { data, err, reason, loading, denied, refreshFailed, answeredAt, notFound } = useGet<RunTrace>(`/runs/${id}/trace`, { poll: 3000 })
  const reading = useMemo(() => ({ data, err, reason, loading, denied, refreshFailed, answeredAt, notFound }), [data, err, reason, loading, denied, refreshFailed, answeredAt, notFound])
  useEffect(() => {
    const t = reading.data
    receive(id, reading, t?.run ? { ...t, run: t.run, steps: t.steps.filter(Boolean) as Step[], events: t.events as RunDetail['events'], next: [], landingMeans: [] } : undefined)
  }, [id, reading, receive])
  return null
}


function Timing({ steps }: { steps: Step[] }) {
  if (steps.every(s => ['not_applicable', 'skipped'].includes(s.status))) return <span>—</span>
  const starts = steps.map(s => s.startedAt).filter(Boolean).sort()
  const finishes = steps.map(s => s.finishedAt).filter(Boolean).sort()
  if (starts.length && finishes.length === steps.length) return <span>{fmtDuration(Math.max(0, Math.round((Date.parse(finishes.at(-1)!) - Date.parse(starts[0])) / 1000)))}</span>
  if (starts.length && steps.some(s => s.status === 'running')) return <Elapsed startedAt={starts[0]} />
  return starts.length ? <time dateTime={starts[0]}>Started {fmtStamp(starts[0])}</time> : <span>—</span>
}
function Mark({ steps }: { steps: Step[] }) {
  if (steps.some(s => s.progress === 'failed')) return <span className="bad" aria-label="failed">✗</span>
  if (steps.some(s => s.progress === 'unknown' || s.progress === 'skipped')) return <span className="warn" aria-label="unknown">?</span>
  if (steps.some(s => s.progress === 'running')) return <Spinner />
  if (steps.every(s => s.progress === 'not_applicable')) return <span className="muted" aria-label="not applicable">−</span>
  if (steps.every(done)) return <span className="ok" aria-label="passed">✓</span>
  return <span className="muted" aria-label="pending">○</span>
}

// Consecutive checks stay compact until a result needs attention or is in flight.
export function nowRows(steps: Step[]): Step[][] {
  const groups: Step[][] = []
  for (const s of steps) {
    const last = groups.at(-1)
    if (checks.has(s.type) && last && checks.has(last[0].type)) last.push(s)
    else groups.push([s])
  }
  return groups.flatMap(group => group.some(s => failed(s) || s.status === 'running') ? group.map(s => [s]) : [group])
}

export function useNow(view: ChangeView) {
  const [called, setCalled] = useState<Record<string, RunDetail>>({})
  const [readings, setReadings] = useState<Record<string, Gated>>({})
  const receive = useCallback((id: string, reading: Gated, detail?: RunDetail) => {
    setReadings(old => ({ ...old, [id]: reading }))
    if (detail) setCalled(old => ({ ...old, [id]: detail }))
  }, [])
  if (!view.detail) return { details: [], steps: [], readers: null, readings: [] }
  const root = view.detail
  const details: RunDetail[] = []
  const visit = (d: RunDetail) => {
    if (details.some(other => other.run.id === d.run.id)) return
    details.push(d)
    for (const s of currentSteps(d)) if (s.type === 'workflow/call' && s.status === 'running' && s.evidence.childRunId && called[s.evidence.childRunId]) visit(called[s.evidence.childRunId])
  }
  visit(root)
  const steps = details.flatMap(currentSteps)
  const children = [...new Set(steps.flatMap(s => s.type === 'workflow/call' && s.status === 'running' && s.evidence.childRunId ? [s.evidence.childRunId] : []))]
  return { details, steps, readings: children.flatMap(id => readings[id] ? [readings[id]] : []), readers: children.map(id => <CalledRun key={id} id={id} receive={receive} />) }
}

export function Now({ view, details, readings }: { view: ChangeView; details: RunDetail[]; readings: Gated[] }) {
  if (!view.detail) return null
  const root = view.detail
  const activeRun = details.find(d => d.run.id === view.waitingOn.run) ?? details.at(-1)!
  const stageSteps = currentSteps(activeRun)
  const started = stageSteps.map(s => s.startedAt).filter(Boolean).sort()[0]
  const position = stageSteps.findIndex(s => s.id === view.waitingOn.step) + 1 || stageSteps.length
  // The strip's bold phase: the root run's current stage.
  const name = stageLabel(currentSteps(root)[0]?.stage ?? root.run.currentStage)
  return <section className="ch-now" aria-label="Progress details" aria-live="polite">
    <h3>{name}{position > 0 && ` — step ${position} of ${stageSteps.length}`}</h3>
    {started && <p className="small muted">Started {fmtStamp(started)}</p>}
    <ReadStatus q={readings} what="called workflow" /><StaleNote q={readings} />
    {details.map(d => <div key={d.run.id}>
      <p className="ch-now-stages small muted">{d !== root && `${d.run.workflowName} · `}{d.stages.map((stage, i) => {
        const title = stageLabel(stage)
        const current = stage === (currentSteps(d)[0]?.stage ?? d.run.currentStage)
        const bad = d.steps.some(s => s.stage === stage && s.progress === 'failed')
        return <Fragment key={stage}>{i > 0 && ' · '}{current ? <strong className={bad ? 'bad' : undefined}>{title}</strong> : <span className={bad ? 'bad' : undefined}>{title}</span>}</Fragment>
      })}</p>
      <ol className="ch-now-steps">{nowRows(currentSteps(d)).map(group => <li key={group[0].id}>
        <Mark steps={group} /><strong>{checks.has(group[0].type) && group.length > 1 ? 'Checks' : group[0].type === 'guidance/conformance' ? 'Standards check' : stepTitle(group[0])}</strong>
        {group.length > 1 ? <span className="ch-now-fact">{group.map(s => `${stepTitle(s)}${s.status === 'not_applicable' ? ' (not applicable)' : s.status === 'skipped' ? ' (skipped)' : ''}`).join(', ')}</span> : group[0].isCheck && group[0].observation && group[0].status === 'fail' ? <span className="ch-now-fact">finished{group[0].type === 'scan' && group[0].evidence.findings && ` · ${group[0].evidence.findings.total} findings`} · <Link to={`/projects/${view.projectSlug}/security`}>Security →</Link></span> : <StepProgressFact step={group[0]} waitingOn={view.waitingOn} />}
        <Timing steps={group} />
      </li>)}</ol>
    </div>)}
  </section>
}
