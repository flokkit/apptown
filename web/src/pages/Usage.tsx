import { PRODUCT } from '../brand'
import { useState } from 'react'
import { fmtCost, fmtDuration, fmtTokens } from '../format'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { TimingReport, UsageReport } from '../wire.gen'

// Usage reports summarize observed harness tokens and estimated costs by time,
// project, model and workflow. They are not reconciled provider invoices.

type Row = Record<string, string | number>



// Usage is recorded by kind: a workflow step, a chat, an assist, or the
// purpose of one of AppTown's own calls (narrative, conformance-review, …),
// which reads as written.
export const LANE_LABEL: Record<string, string> = { step: 'workflow', console: 'interactive', assist: 'assist' }
const LANE_COLOR: Record<string, string> = { step: 'var(--accent)', console: 'var(--note)', assist: 'var(--warn)' }

function StatCard({ label, value, sub }: { label: string; value: React.ReactNode; sub?: React.ReactNode }) {
  return (
    <div className="stat-card">
      <div className="stat-label">{label}</div>
      <div className="stat-value">{value}</div>
      {sub && <div className="muted small">{sub}</div>}
    </div>
  )
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
                {fmtTokens(Number(r.total))}{hasCost && Number(r.costMicros) > 0 ? ` · ${fmtCost(Number(r.costMicros))}` : ''}
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
export function TimingView({ days }: { days: number }) {
  const tq = useGet<TimingReport>(`/timing?days=${days}`)
  const wfs = tq.data ? tq.data.workflows ?? [] : null
  if (!wfs) return <ReadStatus q={tq} what="the step timings" />
  if (wfs.length === 0) return <><StaleNote q={tq} /><div className="muted">No runs in the last {days} days.</div></>
  const sec = (ms: number) => fmtDuration(Math.round(ms / 1000))
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      <StaleNote q={tq} />
      {wfs.map((wf) => {
        const max = Math.max(1, ...wf.steps.map((s) => s.waitMedianMs + s.medianMs))
        const cost = wf.steps.reduce((a, s) => a + s.costMicros, 0)
        return (
          <div key={wf.workflow} className="card">
            <div className="row" style={{ justifyContent: 'space-between', marginBottom: 8 }}>
              <h3 style={{ margin: 0 }}>{wf.workflow}</h3>
              <span className="muted small">
                {wf.runs} run{wf.runs === 1 ? '' : 's'} · a typical run takes {sec(wf.totalMedianMs)} from open to its last step
                {cost > 0 ? ` · ${fmtCost(cost)} in agent time` : ''}
              </span>
            </div>
            {wf.steps.map((s) => {
              const isApproval = s.type === 'approval'
              const waitPct = (s.waitMedianMs / max) * 100
              const tookPct = (s.medianMs / max) * 100
              return (
                <div key={s.stage + '/' + s.name} className="bar-row">
                  <div className="bar-row-head">
                    <span className="bar-row-label">
                      <span className="muted">{s.stage} · </span>{s.name}
                      {s.reworks > 0 && <span className="muted"> · reworked {s.reworks}×</span>}
                    </span>
                    <span className="muted" style={{ whiteSpace: 'nowrap' }}>
                      {isApproval
                        ? `waited ${sec(s.waitMedianMs)} for a person`
                        : `waited ${sec(s.waitMedianMs)} · took ${sec(s.medianMs)} (p90 ${sec(s.p90Ms)}, max ${sec(s.maxMs)})`}
                      {s.costMicros > 0 ? ` · ${fmtCost(s.costMicros)}` : ''}
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

type DayTotals = { step: number; console: number; assist: number; cost: number }

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
  const total = e ? e.step + e.console + e.assist : 0
  const maxDay = Math.max(1, ...[...perDay.values()].map((x) => x.step + x.console + x.assist))
  const label = new Date(day + 'T00:00:00Z').toLocaleDateString('en-AU', { day: 'numeric', month: 'short', timeZone: 'UTC' })
  const detail = !e ? `${label}: no use`
    : `${label}: ${fmtTokens(total)} tokens — ${LANE_LABEL.step} ${fmtTokens(e.step)}, ${LANE_LABEL.console} ${fmtTokens(e.console)}`
      + (e.assist > 0 ? `, ${LANE_LABEL.assist} ${fmtTokens(e.assist)}` : '') + (e.cost > 0 ? ` · ${fmtCost(e.cost)}` : '')
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
  return (
    <div className="card">
      <div className="row" style={{ marginBottom: 8 }}>
        <h3 style={{ margin: 0 }}>Daily</h3>
        <span className="muted small">
          <span style={{ color: LANE_COLOR.step }}>■</span> {LANE_LABEL.step}&nbsp;
          <span style={{ color: LANE_COLOR.console }}>■</span> {LANE_LABEL.console}
        </span>
        <span style={{ flex: 1 }} />
        <span className="muted small">tallest day {fmtTokens(maxDay)}</span>
      </div>
      <div className="usage-chart" role="slider" tabIndex={0} aria-label="day to show"
        aria-valuemin={0} aria-valuemax={days.length - 1} aria-valuenow={at} aria-valuetext={detail}
        onPointerDown={(ev) => { ev.currentTarget.setPointerCapture?.(ev.pointerId); pickAt(ev.currentTarget, ev.clientX) }}
        onPointerMove={(ev) => { if (ev.buttons) pickAt(ev.currentTarget, ev.clientX) }}
        onKeyDown={(ev) => { if (key(ev.key)) ev.preventDefault() }}>
        {days.map((d, i) => {
          const x = perDay.get(d)
          const t = x ? x.step + x.console + x.assist : 0
          return (
            <div key={d} className={'usage-col' + (i === at ? ' on' : '')}>
              {x && (
                <div className="usage-stack" style={{ height: `${Math.max(3, (t / maxDay) * 100)}%` }}>
                  {x.console > 0 && <div style={{ flex: x.console, background: LANE_COLOR.console }} />}
                  {x.assist > 0 && <div style={{ flex: x.assist, background: LANE_COLOR.assist }} />}
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
      <div className="usage-day" aria-live="polite">{detail}</div>
    </div>
  )
}

export function UsageView({ project }: { project?: string }) {
  const [window, setWindow] = useState(30)
  const q = new URLSearchParams({ window: String(window) })
  if (project) q.set('project', project)
  const uq = useGet<UsageReport>(`/usage?${q}`)
  const data = uq.data

  if (!data) return <ReadStatus q={uq} what="the usage" />

  const hasCost = data.totals.costMicros > 0
  const interactive = Number(data.byKind.find((k) => k.kind === 'console')?.total ?? 0)
  const workflow = data.totals.total - interactive
  const cachePct = data.totals.total > 0 ? Math.round((data.totals.cacheRead / data.totals.total) * 100) : 0

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
    const e = perDay.get(r.day) ?? { step: 0, console: 0, assist: 0, cost: 0 }
    e[(r.kind as 'step' | 'console' | 'assist')] = (e[(r.kind as 'step' | 'console' | 'assist')] ?? 0) + r.total
    e.cost += r.costMicros
    perDay.set(r.day, e)
  }
  const shownDays = window > 45 ? days.filter((_, i) => i % 3 === 0 || i === days.length - 1) : days

  return (
    <div>
      <StaleNote q={uq} />
      <div className="row usage-stats" style={{ marginBottom: 14, alignItems: 'stretch' }}>
        <StatCard label="Tokens" value={fmtTokens(data.totals.total)}
          sub={<>↑{fmtTokens(data.totals.input)} in · ↓{fmtTokens(data.totals.output)} out</>} />
        <StatCard label="Cost" value={`${data.totals.incomplete > 0 ? 'at least ' : ''}${hasCost ? fmtCost(data.totals.costMicros) : '$0'}`}
          sub={data.totals.incomplete > 0
            ? `${data.totals.incomplete} model ${data.totals.incomplete === 1 ? 'call' : 'calls'} spent an unknown amount`
            : hasCost ? 'estimated per message' : 'local backends'} />
        <StatCard label="Agents vs chat"
          value={
            <div className="lane-split" title={`workflow ${fmtTokens(workflow)} · interactive ${fmtTokens(interactive)}`}>
              <div style={{ width: `${data.totals.total ? (workflow / data.totals.total) * 100 : 50}%`, background: LANE_COLOR.step }} />
              <div style={{ flex: 1, background: LANE_COLOR.console }} />
            </div>
          }
          sub={<><span style={{ color: LANE_COLOR.step }}>■</span> workflow {fmtTokens(workflow)} · <span style={{ color: LANE_COLOR.console }}>■</span> interactive {fmtTokens(interactive)}</>} />
        <StatCard label="Cache reads" value={`${cachePct}%`} sub={`${fmtTokens(data.totals.cacheRead)} tokens served from cache`} />
        <span style={{ flex: 1 }} />
        <div className="row" style={{ alignItems: 'flex-start' }}>
          {[7, 30, 90].map((w) => (
            <button key={w} className={window === w ? 'primary' : 'subtle'} onClick={() => setWindow(w)}>{w}d</button>
          ))}
        </div>
      </div>

      <DailyChart days={days} perDay={perDay} shownFrom={shownDays[0]} />

      <div className="row" style={{ alignItems: 'stretch', gap: 12 }}>
        <BarList title="By model" rows={data.byModel} labelKey="model" hasCost={hasCost}
          mapLabel={(m) => m.length > 42 ? m.slice(0, 20) + '…' + m.slice(-19) : m} />
        <BarList title={project ? 'By workflow' : 'By project'}
          rows={project ? data.byWorkflow : data.byProject}
          labelKey={project ? 'workflow' : 'project'} hasCost={hasCost} />
        <BarList title="By kind" rows={data.byKind} labelKey="kind" hasCost={hasCost}
          mapLabel={(k) => LANE_LABEL[k] ?? k} />
      </div>

      {!project && (
        <div style={{ marginTop: 16 }}>
          <h2 style={{ marginBottom: 4 }}>Where the time goes</h2>
          <div className="muted small" style={{ marginBottom: 10 }}>
            Each workflow's steps across the runs of the last {window} days: how long a step waited to start,
            how long it ran, and how long an approval waited for a person.
          </div>
          <TimingView days={window} />
        </div>
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
      <h1>Usage</h1>
      <div className="muted small" style={{ marginBottom: 14 }}>
        Token accounting for the projects you can read (the whole of {PRODUCT}, if you read it
        all) — workflow (batch) and interactive (chat) use, by model and project. Costs are
        pi's own per-message pricing; local backends are $0.
      </div>
      <UsageView />
    </div>
  )
}
