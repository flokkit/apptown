import { Status, STATUS_LIGHT } from '../../Status'
// The pieces every Conformance view shares: the generated types it reads,
// the words and colours for standings, states and lanes, and the small
// chips, bars and banners the views are built from.

import type { CSSProperties } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { post } from '../../api'
import { useAction } from '../../data'
import type { ConformanceObservation, ConformanceView, CheckDefinition, Adjudication, Disposition, Lanes, Review, RemediationStarted, Verdict as WireVerdict } from '../../wire.gen'
import { fmtWhen } from '../../format'

// --- types: generated from internal/apptown/conformance (wire.gen.ts) ---------

export type Verdict = ConformanceObservation | WireVerdict
// LiveReview is a review being followed: the review and its verdicts so far.
export type LiveReview = { review: Review; verdicts: WireVerdict[] }

// --- vocabulary ------------------------------------------------------------------------

export const STANDING: Record<string, { tone: string; style?: CSSProperties }> = {
  'does-not-conform': { tone: 'fail' },
  'review-broken': { tone: 'error' },
  'needs-a-look': { tone: '', style: { color: 'var(--warn)', background: 'var(--warn-bg)' } },
  'conforms-so-far': { tone: 'pending' },
  'conforms-with-exceptions': { tone: 'pass' },
  conforms: { tone: 'pass' },
  'nothing-adopted': { tone: 'draft' },
}
export const STATE: Record<string, { tone: string; style?: CSSProperties; rank: number }> = {
  'does-not-conform': { tone: 'fail', rank: 0 },
  error: { tone: 'error', rank: 1 },
  excepted: { tone: 'overridden', rank: 3 },
  stale: { tone: 'pending', rank: 2 },
  'cannot-assess': { tone: '', rank: 2 },
  'not-scanned': { tone: '', rank: 2 },
  'not-applicable': { tone: 'draft', rank: 4 },
  conforms: { tone: 'pass', rank: 5 },
}
export const stateOf = (s: string) => STATE[s] ?? { tone: '', rank: 9 }
export const EVAL: Record<string, string> = { sh: 'shell check', llm: 'AI rubric', tool: 'scanner', runtime: 'runtime probe' }
export const SEV: Record<string, string> = { high: 'var(--bad)', medium: 'var(--warn)', low: 'var(--muted)', note: 'var(--note)' }
export const LANE_COLOR: Record<string, string> = {
  conforms: 'var(--ok)', excepted: 'var(--note)', flagged: 'var(--warn)', findings: 'var(--bad)',
  unassessed: 'var(--border-strong)', broken: 'var(--bad)',
}
export const LANE_LABEL: Record<string, string> = {
  conforms: 'conform', excepted: 'excepted', flagged: 'flagged by AI review', findings: 'findings',
  unassessed: 'not yet judged', broken: "couldn't run", na: 'not applicable',
}

export const ITEM: Record<string, { label: string; tone: string }> = {
  queued: { label: 'queued', tone: 'draft' },
  working: { label: 'working', tone: 'running' },
  accepting: { label: 'waiting on you', tone: 'pending' },
  landing: { label: 'landing', tone: 'running' },
  verifying: { label: 'verifying', tone: 'running' },
  done: { label: 'done', tone: 'pass' },
  stuck: { label: 'stuck', tone: 'fail' },
  skipped: { label: 'skipped', tone: 'draft' },
}
export const JOB: Record<string, { label: string; tone: string }> = {
  running: { label: 'running', tone: 'running' }, paused: { label: 'paused', tone: 'pending' },
  completed: { label: 'completed', tone: 'pass' }, completed_with_stuck: { label: 'completed, some stuck', tone: 'fail' },
  cancelled: { label: 'cancelled', tone: 'draft' },
}
export const ITEM_COLOR: Record<string, string> = { done: 'var(--ok)', working: 'var(--busy)', accepting: 'var(--warn)', landing: 'var(--busy)', verifying: 'var(--busy)', stuck: 'var(--bad)', queued: 'var(--border-strong)', skipped: 'var(--muted)' }

export function laneOf(v: Verdict, adj: Record<string, Adjudication>): keyof Lanes {
  switch (v.state) {
    case 'conforms': return 'conforms'
    case 'excepted': return 'excepted'
    case 'does-not-conform': return v.evaluator === 'llm' && !adj[String(v.id)] ? 'flagged' : 'findings'
    case 'not-applicable': return 'na'
    case 'error': return 'broken'
    default: return 'unassessed'
  }
}

export const shortSha = (s?: string) => (s ? s.slice(0, 8) : '')
export const code: CSSProperties = { fontSize: 'var(--fs-xs)', whiteSpace: 'pre-wrap', background: 'var(--code-bg)', border: '1px solid var(--border)', borderRadius: 6, padding: '8px 10px', margin: 0, maxHeight: 220, overflow: 'auto' }

// --- small parts ---------------------------------------------------------------------------

export function StandingChip({ label, help }: { label: import('../../wire.gen').CheckLabel; help: string }) {
  return <Status word={label} tone={STATUS_LIGHT[label]} help={help} />
}
export function StateChip({ label, help }: { label: import('../../wire.gen').CheckLabel; help?: string }) {
  return <Status word={label} tone={STATUS_LIGHT[label]} help={help} />
}

export function SevDot({ sev }: { sev: string }) {
  return <span title={`severity ${sev}`} style={{ width: 8, height: 8, borderRadius: 999, background: SEV[sev] ?? 'var(--muted)', flex: '0 0 auto', display: 'inline-block' }} />
}

// The lane bar: one project's checks in lane order. Width = share of M.
export function LaneBar({ lanes, total, width = 160 }: { lanes: Lanes; total: number; width?: number }) {
  const order: (keyof Lanes)[] = ['conforms', 'excepted', 'flagged', 'findings', 'unassessed', 'broken']
  const m = Math.max(total, 1)
  const title = order.filter((k) => lanes[k] > 0).map((k) => `${lanes[k]} ${LANE_LABEL[k]}`).join(' · ') || 'nothing to judge'
  return (
    <span title={title} style={{ display: 'inline-flex', width, height: 8, borderRadius: 4, overflow: 'hidden', background: 'var(--panel2)', border: '1px solid var(--border)', flex: '0 0 auto' }}>
      {order.map((k) => lanes[k] > 0 && (
        <span key={k} style={{
          width: `${(lanes[k] / m) * 100}%`, background: LANE_COLOR[k],
          opacity: k === 'unassessed' ? 0.6 : 1,
          backgroundImage: k === 'broken' ? 'repeating-linear-gradient(45deg, transparent 0 3px, var(--panel) 3px 5px)' : undefined,
        }} />
      ))}
    </span>
  )
}

// Where a view came from. Under a review lens every control is hidden — a
// record is for reading.
export function LensBanner({ view }: { view: ConformanceView }) {
  const r = view.lens.review
  if (!r) return null
  const d = view.lens.delta
  return (
    <div className="card tone-accent" style={{ marginBottom: 'var(--space-4)', padding: '10px 14px' }}>
      <div className="row" style={{ justifyContent: 'space-between', gap: 'var(--space-3)' }}>
        <div>
          <strong>As of review #{r.number}</strong>
          <span className="muted small"> · {fmtWhen(r.finishedAt || r.startedAt)} · {r.actor} · {r.trigger} · {r.projects} project{r.projects === 1 ? '' : 's'}</span>
          {d && (
            <div className="small" style={{ marginTop: 3 }}>
              Since the review before it, over {d.comparedProjects} project{d.comparedProjects === 1 ? '' : 's'} in both:{' '}
              <span style={{ color: d.fixed ? 'var(--ok)' : undefined, fontWeight: d.fixed ? 600 : 400 }}>{d.fixed} fixed</span>
              {' · '}<span style={{ color: d.new ? 'var(--bad)' : undefined, fontWeight: d.new ? 600 : 400 }}>{d.new} new</span>
              {' · '}{d.stillOpen} still open{d.newlyJudged ? ` · ${d.newlyJudged} newly judged` : ''}{d.newlyUnjudged ? ` · ${d.newlyUnjudged} no longer judged` : ''}
            </div>
          )}
          {!d && r.status === 'done' && <div className="muted small" style={{ marginTop: 3 }}>No earlier review to compare with.</div>}
          {r.status === 'queued' && <div className="muted small" style={{ marginTop: 3 }}>Queued.</div>}
          {r.status === 'running' && <div className="muted small" style={{ marginTop: 3 }}>Still running · {reviewProgressWords(r.status, r.projectsDone, r.projectsTotal || 0)}.</div>}
        </div>
        <Link to="/conformance" className="subtle small">Back to current →</Link>
      </div>
    </div>
  )
}

// A running (or finished) review as an activity feed, newest first.
export function ReviewFeed({ d }: { d: LiveReview }) {
  const r = d.review
  const running = isUnfinished(r.status)
  const open = d.verdicts.filter((v) => v.state === 'does-not-conform' || v.state === 'error').length
  const feed = [...d.verdicts].reverse()
  return (
    <div className={`card${running ? ' tone-busy' : open > 0 ? ' tone-bad' : ''}`} style={{ padding: 0, marginBottom: 'var(--space-4)', overflow: 'hidden' }}>
      <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center', padding: '10px 14px' }}>
        <div className="row" style={{ gap: 'var(--space-3)', alignItems: 'center' }}>
          {r.status === 'running' && <span style={{ width: 13, height: 13, border: '2px solid var(--border-strong)', borderTopColor: 'transparent', borderRadius: '50%', display: 'inline-block', animation: 'spin 0.8s linear infinite' }} />}
          <strong style={{ fontSize: 'var(--fs-base)' }}>{r.status === 'queued' ? 'Queued' : running ? 'Reviewing…' : r.status === 'error' ? 'Review errored' : `Review #${r.number}`}</strong>
          <span className="muted small">
            {reviewProgressWords(r.status, r.projectsDone, r.projectsTotal || r.projects || 0)} · {d.verdicts.length} checks
            {running && r.currentProject && <> · now <span className="mono">{r.currentProject}</span></>}
            {open > 0 && <> · <span style={{ color: 'var(--bad)', fontWeight: 600 }}>{open} findings</span></>}
          </span>
        </div>
        {!running && <Link to={`/conformance/reviews/${r.id}`} className="subtle small">open this review →</Link>}
      </div>
      <div style={{ maxHeight: 260, overflowY: 'auto', borderTop: '1px solid var(--border)' }}>
        {feed.length === 0 && <div className="muted small" style={{ padding: '12px 14px' }}>{r.status === 'queued' ? 'Not started yet.' : running ? 'Starting…' : 'No checks recorded.'}</div>}
        {feed.map((v, i) => (
          <div key={i} className="row" style={{ justifyContent: 'space-between', gap: 'var(--space-3)', padding: '5px 14px', borderTop: '1px solid var(--border)' }}>
            <span className="small" style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              <span style={{ fontWeight: 600 }}>{v.projectSlug}</span>
              <span className="mono muted" style={{ fontSize: 'var(--fs-xs)' }}> · {v.block}/{v.check}</span>
            </span>
            <StateChip label={v.statusLabel} help={v.detail} />
          </div>
        ))}
      </div>
    </div>
  )
}

// A running review in the header: how far along, what it is on, what it has
// found so far — a bar, not a log. The per-check feed lives on the review's page.
// A review still to finish: queued behind another, or running.
export const isUnfinished = (status: string) => status === 'queued' || status === 'running'

// How far a review has got, in words. A queued review has not started, so it
// shows no count; a running one whose project total is not known yet shows
// what it has done, never "of ?".
export function reviewProgressWords(status: string, done: number, total: number): string {
  if (status === 'queued') return 'not started yet'
  return total ? `${done} of ${total} projects` : `${done} projects so far`
}

export function ReviewProgress({ d }: { d: LiveReview }) {
  const r = d.review
  const total = r.projectsTotal || r.projects || 0
  const pct = total ? Math.min(100, Math.round((r.projectsDone / total) * 100)) : 0
  const open = d.verdicts.filter((v) => v.state === 'does-not-conform' || v.state === 'error').length
  return (
    <div style={{ minWidth: 260, maxWidth: 360, flex: '1 1 260px' }}>
      <div className="row" style={{ justifyContent: 'space-between', gap: 'var(--space-2)', fontSize: 'var(--fs-xs)', marginBottom: 'var(--space-1)' }}>
        <span className="row" style={{ gap: 'var(--space-2)', alignItems: 'center' }}>
          {r.status === 'running' && <span style={{ width: 11, height: 11, border: '2px solid var(--border-strong)', borderTopColor: 'transparent', borderRadius: '50%', display: 'inline-block', animation: 'spin 0.8s linear infinite' }} />}
          <strong>{r.status === 'queued' ? 'Queued' : 'Reviewing'}</strong>
          <span className="muted">{reviewProgressWords(r.status, r.projectsDone, total)}{r.currentProject ? <> · <span className="mono">{r.currentProject}</span></> : null}</span>
        </span>
        <span className="muted">
          {open > 0 ? <span style={{ color: 'var(--bad)', fontWeight: 600 }}>{open} findings</span> : `${d.verdicts.length} checks`}
          {' · '}<Link to={`/conformance/reviews/${r.id}`} className="subtle">watch</Link>
        </span>
      </div>
      <div className="bar-track"><div className="bar-fill" style={{ width: `${pct}%`, transition: 'width 0.5s' }} /></div>
    </div>
  )
}

// "How it's judged": the actual sh/rubric/tool/probe, where it runs, adopters.
export function HowJudged({ def }: { def?: CheckDefinition }) {
  if (!def) return null
  const how = def.evaluator === 'sh' ? def.sh : def.evaluator === 'llm' ? def.rubric : def.evaluator === 'tool' ? def.tool : def.probe
  return (
    <div style={{ padding: '10px 14px', background: 'var(--panel2)', fontSize: 'var(--fs-xs)', display: 'flex', flexDirection: 'column', gap: 'var(--space-2)' }}>
      <div>
        <span className="muted">How it's judged: </span>
        <strong>{EVAL[def.evaluator] ?? def.evaluator}</strong>
        <span className="muted"> · {def.judgedWhere} · {def.block}@{def.version} · severity {def.severity}</span>
      </div>
      {how && <pre className="mono" style={{ ...code, maxHeight: 160 }}>{how}</pre>}
      {def.files && def.files.length > 0 && <div className="muted small">judged on: <span className="mono">{def.files.join(', ')}</span></div>}
      {def.evaluator === 'llm' && def.scope === 'diff' && <div className="muted small">Runs when a change lands.</div>}
      <div className="muted small">adopted by {def.adopters.length} project{def.adopters.length === 1 ? '' : 's'}: {def.adopters.join(', ')}</div>
    </div>
  )
}

// One verdict's evidence: the commit, the check's own words, the exception
// behind an excepted verdict, the adjudication on an AI flag.
export function Evidence({ v, exceptions, adj }: { v: Verdict; exceptions: Disposition[]; adj?: Adjudication }) {
  let exc: { id: string; status: string; by: string } | null = null
  try { exc = v.exceptedBy ? JSON.parse(v.exceptedBy) : null } catch { exc = null }
  const covering = exc ? exceptions.find((d) => d.id === exc!.id) : undefined
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--space-2)' }}>
      {v.sha && (
        <div className="muted small">
          judged <span className="mono">{shortSha(v.sha)}</span> · {fmtWhen(v.at)} · {EVAL[v.evaluator] ?? v.evaluator}
          {v.reviewId ? <> · <Link to={`/conformance/reviews/${v.reviewId}`} className="subtle">its review</Link></> : null}
          {v.evidenceHash && <> · evidence <span className="mono">{v.evidenceHash}</span></>}
        </div>
      )}
      {v.detail && <pre className="mono" style={code}>{v.detail}</pre>}
      {exc && (
        <div className="small" style={{ borderLeft: '2px solid var(--note)', paddingLeft: 8 }}>
          <span className="chip overridden" style={{ marginRight: 'var(--space-2)' }}>{exc.status.replace('_', ' ')}</span>
          {covering ? <>{covering.justification} <span className="muted">— {covering.by}{covering.expiresAt ? `, until ${fmtWhen(covering.expiresAt)}` : ''}</span></> : <span className="muted">an exception by {exc.by}</span>}
        </div>
      )}
      {adj && (
        <div className="small" style={{ borderLeft: '2px solid var(--bad)', paddingLeft: 8 }}>
          <span className="chip fail" style={{ marginRight: 'var(--space-2)' }}>confirmed</span>
          {adj.by} confirmed this finding{adj.note ? `: ${adj.note}` : ''} <span className="muted">· {fmtWhen(adj.createdAt)}</span>
        </div>
      )}
    </div>
  )
}

export const tabClass = (on: boolean) => 'tab' + (on ? ' active' : '')


// Start a job and open it. Used by the header, a check card, and a project
// page; each shows its own failure.
export function useStartRemediation() {
  const nav = useNavigate()
  const { run, busy, err } = useAction()
  const start = async (scope: string, ref: string) => {
    const r = await run(() => post<RemediationStarted>('/conformance/remediations', { scope, ref }))
    if (r) nav(`/conformance/remediations/${r.remediation.id}`)
  }
  return { start, busy, err }
}
