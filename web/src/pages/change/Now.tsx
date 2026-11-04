import { useCallback, useEffect, useState } from 'react'
import type { ChangeView, RunDetail, Step } from '../../api'
import type { RunTrace, StepProgress } from '../../wire.gen'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { fmtDuration, fmtStamp } from '../../format'
import { stepTitle } from './Evidence'
import { approversOf } from './next'

const checks = new Set(['scan', 'tests', 'guidance/conformance'])
const failed = (s: Step) => ['fail', 'error', 'needs_human'].includes(s.status)
const done = (s: Step) => ['pass', 'not_applicable', 'skipped'].includes(s.status)
export const nowRunning = (view: ChangeView) => view.detail?.run.status === 'running' && view.waitingOn.kind === 'work'

function currentSteps(d: RunDetail): Step[] {
  const stages = new Set([d.run.currentStage, ...d.steps.filter(s => s.status === 'running').map(s => s.stage)])
  return d.steps.filter(s => stages.has(s.stage))
}

function CalledRun({ id, receive }: { id: string; receive: (id: string, detail: RunDetail) => void }) {
  const q = useGet<RunTrace>(`/runs/${id}/trace`, { poll: 3000 })
  useEffect(() => {
    const t = q.data
    if (t?.run) receive(id, { ...t, run: t.run, steps: t.steps.filter(Boolean) as Step[], events: t.events as RunDetail['events'], next: [], landingMeans: [] })
  }, [id, q.data, receive])
  return <><ReadStatus q={q} what="called workflow" /><StaleNote q={q} /></>
}

function StepFact({ step: s }: { step: Step }) {
  const live = useGet<StepProgress>(s.status === 'running' && s.type === 'deploy' ? `/steps/${s.id}/live?from=0` : null, { poll: 3000 })
  const fact = s.type === 'approval' ? s.evidence.approver || approversOf(s)
    : s.type === 'deploy' ? live.data?.lines.at(-1)?.slice(0, 180) || s.evidence.url || s.evidence.reason || (s.status === 'running' ? 'Deploying the accepted work' : s.status === 'pending' ? 'Waiting to deploy' : s.status === 'pass' ? 'Accepted work deployed' : 'Deployment stopped')
    : s.type === 'agent/pi' ? s.status === 'running' ? 'Agent working' : s.status === 'pending' ? 'Waiting to start' : s.evidence.reason || 'Agent finished'
    : checks.has(s.type) ? s.name.replace(/[-_]/g, ' ') : s.evidence.reason
  return <span className="ch-now-fact">{fact}<StaleNote q={live} />{live.err && <span className="error">Progress could not be read: {live.err}</span>}</span>
}

function Timing({ steps }: { steps: Step[] }) {
  const starts = steps.map(s => s.startedAt).filter(Boolean).sort()
  const finishes = steps.map(s => s.finishedAt).filter(Boolean).sort()
  if (starts.length && finishes.length === steps.length) return <span>{fmtDuration(Math.max(0, Math.round((Date.parse(finishes.at(-1)!) - Date.parse(starts[0])) / 1000)))}</span>
  return starts.length ? <time dateTime={starts[0]}>Started {fmtStamp(starts[0])}</time> : <span>—</span>
}
function Mark({ steps }: { steps: Step[] }) {
  if (steps.some(failed)) return <span className="bad" aria-label="failed">✗</span>
  if (steps.some(s => s.status === 'running')) return <span className="ch-now-spinner" role="status" aria-label="running" />
  if (steps.every(done)) return <span className="ok" aria-label="done">✓</span>
  return <span className="muted">pending</span>
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

export function Now({ view }: { view: ChangeView }) {
  const [called, setCalled] = useState<Record<string, RunDetail>>({})
  const receive = useCallback((id: string, detail: RunDetail) => setCalled(old => ({ ...old, [id]: detail })), [])
  if (!nowRunning(view) || !view.detail) return null
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
  const activeRun = details.find(d => d.run.id === view.waitingOn.run)
  const stageSteps = activeRun?.steps.filter(s => s.stage === activeRun.run.currentStage) ?? []
  const position = stageSteps.findIndex(s => s.id === view.waitingOn.step) + 1
  const name = view.runs?.find(r => r.id === root.run.id)?.role === 'landing' ? 'Landing' : 'Work'
  return <section className="ch-now" aria-label="Now" aria-live="polite">
    <h2>Now</h2>
    <h3>{name}{position > 0 && ` — step ${position} of ${stageSteps.length}`}</h3>
    {root.run.createdAt && <p className="small muted">Started {fmtStamp(root.run.createdAt)}</p>}
    {children.map(id => <CalledRun key={id} id={id} receive={receive} />)}
    {details.map(d => <div key={d.run.id}>
      {d !== root && <p className="small muted">{d.run.workflowName}</p>}
      <ol className="ch-now-steps">{nowRows(currentSteps(d)).map(group => <li key={group[0].id}>
        <Mark steps={group} /><strong>{checks.has(group[0].type) && group.length > 1 ? 'Checks' : stepTitle(group[0])}</strong>
        {group.length > 1 ? <span className="ch-now-fact">{group.map(s => s.name.replace(/[-_]/g, ' ')).join(', ')}</span> : <StepFact step={group[0]} />}
        <Timing steps={group} />
      </li>)}</ol>
    </div>)}
  </section>
}
