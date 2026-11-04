import { Tabs } from '../Tabs'
import { useState } from 'react'
import { fmtCost, fmtPrice as price, fmtDuration, fmtTokens } from '../format'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { TimingReport, UsageReport } from '../wire.gen'
import { PageHead } from '../PageHead'
import { Section } from '../Section'
import './Usage.css'

// Usage reports summarize observed harness tokens and estimated costs by time,
// project, model and workflow. They are not reconciled provider invoices.

type Row = Record<string, string | number>



// Usage is recorded by kind: a workflow step, a chat, or the
// purpose of one of AppTown's own calls (narrative, conformance-review, …),
// which reads as written.
export const LANE_LABEL: Record<string, string> = { step: 'Automated work', console: 'Chat', 'guidance-checklist': 'Rule checks', 'conformance-review': 'Project checks', 'approval-brief': 'Approval summaries', 'pack-assistance': 'Ways of working help', 'what-if': 'Setup comparisons', 'project-description': 'Project descriptions', narrative: 'Summaries' }
const LANE_COLOR: Record<string, string> = { step: 'var(--accent)', console: 'var(--note)' }

// One figure in the totals panel: a label, the amount, and a qualifying line
// only when there is something to qualify.
function StatCard({ label, value, sub }: { label: string; value: React.ReactNode; sub?: React.ReactNode }) {
  return (
    <div className="usage-figure">
      <div className="stat-label">{label}</div>
      <div className="stat-value">{value}</div>
      {sub && <div className="muted small">{sub}</div>}
    </div>
  )
}

function sources(reported: number, catalog: number, unpriced: number): string {
  return [reported > 0 ? `${reported} reported by the provider` : '', catalog > 0 ? `${catalog} from the model catalog` : '', unpriced > 0 ? `${unpriced} spend record${unpriced === 1 ? '' : 's'} without a price source` : ''].filter(Boolean).join(' · ')
}

function BarList({ title, rows, labelKey, hasCost, mapLabel }: {
  title: string; rows?: Row[]; labelKey: string; hasCost: boolean
  mapLabel?: (s: string) => string
}) {
  if (!rows || rows.length === 0) return null
  const max = Math.max(1, ...rows.map((r) => Number(r.total)))
  return (
    <div className="card" style={{ flex: 1, minWidth: 300 }}>
      <h3 style={{ marginTop: 0 }}>{title}</h3>
      {rows.map((r, i) => {
        const label = mapLabel ? mapLabel(String(r[labelKey])) : String(r[labelKey])
        return (
          <div key={i} className="bar-row" title={`${label}: ${fmtTokens(Number(r.total))} tokens (↑${fmtTokens(Number(r.input))} ↓${fmtTokens(Number(r.output))})${Number(r.costMicros) > 0 ? ` · ${fmtCost(Number(r.costMicros))}` : ''}`}>
            <div className="bar-row-head">
              <span className="bar-row-label">{label}</span>
              <span className="muted small">
                {fmtTokens(Number(r.total))}{hasCost || Number(r.unpriced) > 0 ? ` · ${price(Number(r.costMicros), Number(r.unpriced))}` : ''}
                <span className="muted">{sources(Number(r.providerReported), Number(r.catalog), Number(r.unpriced)) ? ` · ${sources(Number(r.providerReported), Number(r.catalog), Number(r.unpriced))}` : ''}</span>
              </span>
            </div>
            <div className="bar-track"><div className="bar-fill" style={{ width: `${(Number(r.total) / max) * 100}%` }} /></div>
          </div>
        )
      })}
    </div>
  )
}

// Where the time goes: per workflow, per step,
// across the runs of the window — how long the step waited to start (or, for
// an approval, waited for a person), how long it took, and what it cost. The
// bars share one scale inside a workflow so the long pole is obvious.
// spend is a step's (or a workflow's) cost as the ledger knows it: "at
// least" when some of it went unrecorded, "spend unknown" when none of it
// was, nothing when nothing was spent.
function spend(costMicros: number, incomplete: number): string {
  if (incomplete > 0) return costMicros > 0 ? `at least ${fmtCost(costMicros)}` : 'spend unknown'
  return costMicros > 0 ? fmtCost(costMicros) : ''
}

export function TimingView({ days }: { days: number }) {
  const tq = useGet<TimingReport>(`/timing?days=${days}`)
  const wfs = tq.data ? tq.data.workflows ?? [] : null
  if (!wfs) return <ReadStatus q={tq} what="the step timings" />
  if (wfs.length === 0) return <><StaleNote q={tq} /><div className="muted">No runs in the last {days} days.</div></>
  // A measured time under a second is said so, never rounded down to a 0 that reads as missing.
  const sec = (ms: number) => ms > 0 && ms < 1000 ? 'under 1s' : fmtDuration(Math.round(ms / 1000))
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      <StaleNote q={tq} />
      {wfs.map((wf) => {
        const max = Math.max(1, ...wf.steps.map((s) => s.waitMedianMs + s.medianMs))
        const cost = spend(wf.steps.reduce((a, s) => a + s.costMicros, 0), wf.steps.reduce((a, s) => a + s.costIncomplete, 0))
        return (
          <div key={wf.workflow} className="card">
            <div className="row" style={{ justifyContent: 'space-between', marginBottom: 8 }}>
              <h3 style={{ margin: 0 }}>{wf.workflow === 'land' ? 'Landing a change' : wf.workflow.replaceAll('-', ' ')}</h3>
              <span className="muted small">
                {wf.runs} run{wf.runs === 1 ? '' : 's'} · a typical run takes {sec(wf.totalMedianMs)} from open to its last step
                {cost ? ` · ${cost} in agent time` : ''}
              </span>
            </div>
            {[...wf.steps].sort((a, b) => (b.waitMedianMs + b.medianMs) - (a.waitMedianMs + a.medianMs)).slice(0, 3).map((s) => {
              const isApproval = s.type === 'approval'
              const waitPct = (s.waitMedianMs / max) * 100
              const tookPct = (s.medianMs / max) * 100
              return (
                <div key={s.stage + '/' + s.name} className="bar-row">
                  <div className="bar-row-head">
                    <span className="bar-row-label">
                      {s.name.replaceAll('-', ' ')}
                      {s.reworks > 0 && <span className="muted"> · reworked {s.reworks}×</span>}
                    </span>
                    <span className="muted" style={{ whiteSpace: 'nowrap' }}>
                      {isApproval
                        ? `waited ${sec(s.waitMedianMs)} for a person`
                        : `waited ${sec(s.waitMedianMs)} · took ${sec(s.medianMs)}`}
                      {spend(s.costMicros, s.costIncomplete) && ` · ${spend(s.costMicros, s.costIncomplete)}`}
                    </span>
                  </div>
                  <div style={{ display: 'flex', height: 8, background: 'var(--rule)', borderRadius: 2, overflow: 'hidden' }}
                    title={`median wait ${sec(s.waitMedianMs)}, median duration ${sec(s.medianMs)} over ${s.runs} runs`}>
                    <div style={{ width: `${waitPct}%`, background: isApproval ? 'var(--warn)' : 'var(--note)' }} />
                    <div style={{ width: `${tookPct}%`, background: 'var(--accent)' }} />
                  </div>
                </div>
              )
            })}
            <div className="muted small" style={{ marginTop: 6 }}>
              <span style={{ color: 'var(--note)' }}>■</span> waiting to start&nbsp;
              <span style={{ color: 'var(--warn)' }}>■</span> waiting for a person&nbsp;
              <span style={{ color: 'var(--accent)' }}>■</span> running · medians over the window
            </div>
          </div>
        )
      })}
    </div>
  )
}

type DayTotals = { step: number; console: number; cost: number }

// The daily chart. Its numbers are readable without a mouse: one day is
// always selected and spelled out under the chart (the latest day with use,
// until someone picks another). The chart is one wide control, a slider
// over the days: tap or drag along it on a phone, click it, or use the
// arrow keys. Columns are too narrow to be separate tap targets.
function DailyChart({ days, perDay, shownFrom }: { days: string[]; perDay: Map<string, DayTotals>; shownFrom?: string }) {
  const [picked, setPicked] = useState<number | null>(null)
  const lastUsed = days.reduce((at, d, i) => (perDay.has(d) ? i : at), days.length - 1)
  const at = Math.min(picked ?? lastUsed, days.length - 1)
  const day = days[at]
  const e = perDay.get(day)
  const total = e ? e.step + e.console : 0
  const maxDay = Math.max(1, ...[...perDay.values()].map((x) => x.step + x.console))
  const label = new Date(day + 'T00:00:00Z').toLocaleDateString('en-AU', { day: 'numeric', month: 'short', timeZone: 'UTC' })
  const detail = !e ? `${label}: no use`
    : `${label}: ${fmtTokens(total)} tokens · ${LANE_LABEL.step} ${fmtTokens(e.step)}, ${LANE_LABEL.console} ${fmtTokens(e.console)}`
      + (e.cost > 0 ? ` · ${fmtCost(e.cost)}` : '')
  const pickAt = (el: HTMLElement, clientX: number) => {
    const r = el.getBoundingClientRect()
    setPicked(Math.max(0, Math.min(days.length - 1, Math.floor(((clientX - r.left) / r.width) * days.length))))
  }
  const key = (k: string) => {
    if (k === 'ArrowLeft' || k === 'ArrowDown') setPicked(Math.max(0, at - 1))
    else if (k === 'ArrowRight' || k === 'ArrowUp') setPicked(Math.min(days.length - 1, at + 1))
    else if (k === 'Home') setPicked(0)
    else if (k === 'End') setPicked(days.length - 1)
    else return false
    return true
  }
  // The legend names only the kinds of use the window has.
  const lanes = (['step', 'console'] as const).filter(k => [...perDay.values()].some(x => x[k] > 0))
  return (
    <Section title="Daily tokens" action={<span className="muted">tallest day {fmtTokens(maxDay)}</span>}>
      <div className="usage-chart-panel">
      {lanes.length > 0 && <div className="muted small usage-legend">
        {lanes.map(k => <span key={k}><span style={{ color: LANE_COLOR[k] }}>■</span> {LANE_LABEL[k]}</span>)}
      </div>}
      <div className="usage-chart" role="slider" tabIndex={0} aria-label="day to show"
        aria-valuemin={0} aria-valuemax={days.length - 1} aria-valuenow={at} aria-valuetext={detail}
        onPointerDown={(ev) => { ev.currentTarget.setPointerCapture?.(ev.pointerId); pickAt(ev.currentTarget, ev.clientX) }}
        onPointerMove={(ev) => { if (ev.buttons) pickAt(ev.currentTarget, ev.clientX) }}
        onKeyDown={(ev) => { if (key(ev.key)) ev.preventDefault() }}>
        {days.map((d, i) => {
          const x = perDay.get(d)
          const t = x ? x.step + x.console : 0
          return (
            <div key={d} className={'usage-col' + (picked !== null && i === at ? ' on' : '')}>
              {x && (
                <div className="usage-stack" style={{ height: `${Math.max(3, (t / maxDay) * 100)}%` }}>
                  {x.console > 0 && <div style={{ flex: x.console, background: LANE_COLOR.console }} />}
                  {x.step > 0 && <div style={{ flex: x.step, background: LANE_COLOR.step }} />}
                </div>
              )}
            </div>
          )
        })}
      </div>
      <div className="row" style={{ justifyContent: 'space-between' }}>
        <span className="muted small">{shownFrom?.slice(5)}</span>
        <span className="muted small">{days[days.length - 1]?.slice(5)}</span>
      </div>
      <div className="usage-day" aria-live="polite">{picked !== null ? detail : null}</div>
      </div>
    </Section>
  )
}

export function UsageView({ project }: { project?: string }) {
  const [window, setWindow] = useState(30)
  const [tab, setTab] = useState<'summary' | 'details' | 'timing'>('summary')
  const q = new URLSearchParams({ window: String(window) })
  if (project) q.set('project', project)
  const uq = useGet<UsageReport>(`/usage?${q}`)
  const data = uq.data

  if (!data) return <ReadStatus q={uq} what="the usage" />

  const hasCost = data.totals.costMicros > 0
  const chat = data.byKind.find((k) => k.kind === 'console')
  const chatCost = Number(chat?.costMicros ?? 0)
  const chatUnpriced = chat?.unpriced ?? 0
  const workUnpriced = data.totals.unpriced - chatUnpriced
  const workCost = data.totals.costMicros - chatCost

  // Daily chart: fill every day in the window so gaps are visible.
  const days: string[] = []
  {
    const d = new Date()
    for (let i = window - 1; i >= 0; i--) {
      const x = new Date(d.getTime() - i * 86400e3)
      days.push(x.toISOString().slice(0, 10))
    }
  }
  const perDay = new Map<string, DayTotals>()
  for (const r of data.byDayKind ?? []) {
    const e = perDay.get(r.day) ?? { step: 0, console: 0, cost: 0 }
    if (r.kind === 'step' || r.kind === 'console') e[r.kind] += r.total
    e.cost += r.costMicros
    perDay.set(r.day, e)
  }
  const shownDays = window > 45 ? days.filter((_, i) => i % 3 === 0 || i === days.length - 1) : days

  return (
    <div>
      <StaleNote q={uq} />
      <Section title="Recorded cost" action={<div className="usage-range" role="group" aria-label="Range">
        {[7, 30, 90].map((w) => (
          <button key={w} aria-pressed={window === w} className={window === w ? 'on' : ''} onClick={() => setWindow(w)}>{w}d</button>
        ))}
      </div>}>
        <div className="usage-totals">
          <StatCard label={`Cost, ${window} days`} value={price(data.totals.costMicros, data.totals.unpriced)}
            sub={data.totals.incomplete > 0 ? `${data.totals.incomplete} activity records have unknown or partial cost.` : undefined} />
          <StatCard label="Automated work" value={price(workCost, workUnpriced)} />
          <StatCard label="Chat" value={price(chatCost, chatUnpriced)} />
        </div>
        <p className="muted small">{sources(data.totals.providerReported, data.totals.catalog, data.totals.unpriced)}</p>
      </Section>

      <Tabs className="tabs" aria-label="Usage views">
        {(['summary', 'details', 'timing'] as const).filter(t => !project || t !== 'timing').map(t => <button key={t} className={tab === t ? 'active' : ''} onClick={() => setTab(t)}>{t === 'summary' ? 'Summary' : t === 'details' ? 'Details' : 'Time spent'}</button>)}
      </Tabs>
      {tab === 'summary' && <DailyChart days={days} perDay={perDay} shownFrom={shownDays[0]} />}
      {tab === 'details' && <>
      <p className="muted small">{fmtTokens(data.totals.total)} tokens · {fmtTokens(data.totals.cacheRead)} from saved responses</p>
      <DailyChart days={days} perDay={perDay} shownFrom={shownDays[0]} />

      <div className="row" style={{ alignItems: 'stretch', gap: 12 }}>
        <BarList title="By model" rows={data.byModel} labelKey="model" hasCost={hasCost}
          mapLabel={(m) => m.split('/').at(-1)!.replaceAll('-', ' ')} />
        <BarList title={project ? 'By workflow' : 'By project'}
          rows={project ? data.byWorkflow : data.byProject}
          labelKey={project ? 'workflow' : 'project'} hasCost={hasCost} />
        <BarList title="By kind" rows={data.byKind} labelKey="kind" hasCost={hasCost}
          mapLabel={(k) => LANE_LABEL[k] ?? k} />
      </div>

      </>}
      {!project && tab === 'timing' && (
        <Section title="Where the time goes" note={`Last ${window} days · wait and run time`}>
          <TimingView days={window} />
        </Section>
      )}

      {data.totals.total === 0 && (
        <div className="muted" style={{ marginTop: 12 }}>
          No usage recorded yet.
        </div>
      )}
    </div>
  )
}

export default function UsagePage() {
  return (
    <div>
      <PageHead page="usage" />
      <UsageView />
    </div>
  )
}
