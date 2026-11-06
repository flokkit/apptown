import { ProjectCount } from '../DependentProjects'
import { Spinner } from '../../Spinner'
import './parts.css'
import { Status } from '../../Status'
// The pieces every Conformance view shares: the generated types it reads,
// the words and colours for standings, states and lanes, and the small
// chips, bars and banners the views are built from.

import type { CSSProperties } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { post } from '../../api'
import { useAction } from '../../data'
import type { ConformanceObservation, ConformanceView, CheckDefinition, Adjudication, Disposition, Lanes, Review, RemediationStarted, Verdict as WireVerdict } from '../../wire.gen'
import { fmtWhen } from '../../format'

// --- types: generated from internal/apptown/conformance (wire.gen.ts) ---------

export type Verdict = ConformanceObservation | WireVerdict
// LiveReview is a review being followed: the review and its verdicts so far.
export type LiveReview = { review: Review; verdicts: ConformanceObservation[] }

// --- vocabulary ------------------------------------------------------------------------

export const STANDING: Record<string, { tone: string; style?: CSSProperties }> = {
  'does-not-conform': { tone: 'pending' },
  'review-broken': { tone: 'error' },
  'needs-a-look': { tone: '', style: { color: 'var(--warn)', background: 'var(--warn-bg)' } },
  'conforms-so-far': { tone: 'pending' },
  'conforms-with-exceptions': { tone: 'pass' },
  conforms: { tone: 'pass' },
  'nothing-adopted': { tone: 'draft' },
}
export const STATE: Record<string, { tone: string; style?: CSSProperties; rank: number }> = {
  'does-not-conform': { tone: 'pending', rank: 0 },
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
  conforms: 'var(--ok)', excepted: 'var(--note)', flagged: 'var(--warn)', findings: 'var(--warn)',
  unassessed: 'var(--border-strong)', broken: 'var(--bad)',
}
export const LANE_LABEL: Record<string, string> = {
  conforms: 'conform', excepted: 'excepted', flagged: 'flagged by AI review', findings: 'findings',
  unassessed: 'not judged yet', broken: "couldn't run", na: 'not applicable',
}

export const ITEM: Record<string, { label: string }> = {
 queued: {label:'queued'}, working:{label:'working'}, accepting:{label:'waiting on you'},
 landing:{label:'landing'}, verifying:{label:'verifying'}, done:{label:'done'},
 stuck:{label:'stuck'}, skipped:{label:'skipped'},
}
export const ITEM_COLOR: Record<string, string> = { done: 'var(--ok)', working: 'var(--busy)', accepting: 'var(--warn)', landing: 'var(--busy)', verifying: 'var(--busy)', stuck: 'var(--bad)', queued: 'var(--border-strong)', skipped: 'var(--muted)' }

export function laneOf(v: Verdict): keyof Lanes {
  return v.lane
}

export const shortSha = (s?: string) => (s ? s.slice(0, 8) : '')

// --- small parts ---------------------------------------------------------------------------

export function StandingChip({ label, help, findings, broken = 0 }: { label: import('../../wire.gen').CheckLabel; help: string; findings?: number; broken?: number }) {
  if (findings !== undefined) return <span className="row conformance-findings"><span className="finding-count" title={help}>{findings} {findings === 1 ? 'finding' : 'findings'}</span><BrokenNote broken={broken} /></span>
  return <Status word={label} help={help} />
}
export function BrokenNote({ broken }: { broken: number }) {
  return broken > 0 ? <span className="conformance-broken">· {broken} not known</span> : null
}
export function StateChip({ label, help, finding = false, findings = 1 }: { label: import('../../wire.gen').CheckLabel; help?: string; finding?: boolean; findings?: number }) {
  if (finding) return <span className="finding-count" title={help}>{findings} {findings === 1 ? 'finding' : 'findings'}</span>
  return <Status word={label} help={help} />
}

export function SevDot({ sev }: { sev: string }) {
  return <span title={`severity ${sev}`} style={{ '--severity': SEV[sev] ?? 'var(--muted)' } as CSSProperties} className="conformance-severity" />
}

// The lane bar: one project's checks in lane order. Width = share of M.
export function LaneBar({ lanes, total, width = 160 }: { lanes: Lanes; total: number; width?: number }) {
  const order: (keyof Lanes)[] = ['conforms', 'excepted', 'flagged', 'findings', 'unassessed', 'broken']
  const m = Math.max(total, 1)
  const title = order.filter((k) => lanes[k] > 0).map((k) => `${lanes[k]} ${LANE_LABEL[k]}`).join(' · ') || 'nothing to judge'
  return (
    <span title={title} style={{ '--lane-width': `${width}px` } as CSSProperties} className="conformance-lanes">
      {order.map((k) => lanes[k] > 0 && (
        <span key={k} style={{
          '--lane-share': `${(lanes[k] / m) * 100}%`, '--lane-color': LANE_COLOR[k],
        } as CSSProperties} className={`conformance-lane conformance-lane-${k}`} />
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
    <div className="card tone-accent mb-4 conformance-lens">
      <div className="row conformance-lens-heading">
        <div>
          <strong>As of review #{r.number}</strong>
          <span className="muted small"> · {fmtWhen(r.finishedAt || r.startedAt)} · {r.actor} · {r.trigger} · {r.projects} project{r.projects === 1 ? '' : 's'}</span>
          {d && d.comparedProjects > 0 && (
            <div className="small conformance-lens-note">
              Since the review before it, over {d.comparedProjects} project{d.comparedProjects === 1 ? '' : 's'} in both:{' '}
              <span className={d.fixed ? 'conformance-fixed' : 'conformance-no-delta'}>{d.fixed} fixed</span>
              {' · '}<span className={d.new ? 'conformance-new' : 'conformance-no-delta'}>{d.new} new</span>
              {' · '}{d.stillOpen} still open{d.newlyJudged ? ` · ${d.newlyJudged} newly judged` : ''}{d.newlyUnjudged ? ` · ${d.newlyUnjudged} no longer judged` : ''}
            </div>
          )}
          {!d && r.status === 'done' && <div className="muted small conformance-lens-note">No earlier review to compare with.</div>}
          {r.status === 'queued' && <div className="muted small conformance-lens-note">Queued.</div>}
          {r.status === 'running' && <div className="muted small conformance-lens-note">Still running · {reviewProgressWords(r.status, r.projectsDone, r.projectsTotal || 0)}.</div>}
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
  const open = d.verdicts.filter((v) => v.lane === 'findings').length
  const unknown = d.verdicts.filter(v => v.lane === 'broken').length
  const feed = [...d.verdicts].reverse()
  return (
    <div className={((`card${running ? ' tone-busy' : open> 0 ? ' tone-bad' : ''}`) + " mb-4") + " conformance-feed"}>
      <div className="row conformance-feed-heading">
        <div className="row conformance-feed-summary">
          {r.status === 'running' && <Spinner size="small" />}
          <strong className="conformance-feed-title">{r.status === 'queued' ? 'Queued' : running ? 'Reviewing…' : r.status === 'error' ? 'Review errored' : `Review #${r.number}`}</strong>
          <span className="muted small">
            {reviewProgressWords(r.status, r.projectsDone, r.projectsTotal || r.projects || 0)} · {d.verdicts.length} checks
            {running && r.currentProject && <> · now <span className="mono">{r.currentProject}</span></>}
            {unknown > 0 && <> · {unknown} not known</>}
            {open> 0 && <> · <span className="conformance-finding-total">{open} findings</span></>}
          </span>
        </div>
        {!running && <Link to={`/conformance/reviews/${r.id}`} className="subtle small">open this review →</Link>}
      </div>
      <div className="conformance-feed-scroll">
        {feed.length === 0 && <div className="muted small conformance-feed-empty">{r.status === 'queued' ? 'Not started yet.' : running ? 'Starting…' : 'No checks recorded.'}</div>}
        {feed.map((v, i) => (
          <div key={i} className="row conformance-feed-row">
            <span className="small conformance-feed-identity">
              <span className="conformance-feed-project">{v.projectSlug}</span>
              <span className="mono muted conformance-feed-check"> · {v.block}/{v.check}</span>
            </span>
            <StateChip finding={v.lane === 'findings'} label={v.statusLabel} help={v.detail} />
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
  const open = d.verdicts.filter((v) => v.lane === 'findings').length
  const unknown = d.verdicts.filter(v => v.lane === 'broken').length
  return (
    <div className="conformance-progress">
      <div className="row mb-1 conformance-progress-heading">
        <span className="row conformance-progress-label">
          {r.status === 'running' && <Spinner size="tiny" />}
          <strong>{r.status === 'queued' ? 'Queued' : 'Reviewing'}</strong>
          <span className="muted">{reviewProgressWords(r.status, r.projectsDone, total)}{r.currentProject ? <> · <span className="mono">{r.currentProject}</span></> : null}</span>
        </span>
        <span className="muted">
          {open> 0 ? <span className="conformance-finding-total">{open} findings</span> : `${d.verdicts.length} checks`}
          {unknown > 0 && <> · {unknown} not known</>}
          {' · '}<Link to={`/conformance/reviews/${r.id}`} className="subtle">watch</Link>
        </span>
      </div>
      <div className="bar-track"><div className="bar-fill conformance-progress-fill" style={{ '--progress': `${pct}%` } as CSSProperties} /></div>
    </div>
  )
}

// "How it's judged": the actual sh/rubric/tool/probe, where it runs, adopters.
export function HowJudged({ def }: { def?: CheckDefinition }) {
  const { reviewId } = useParams()
  const base = reviewId ? `/conformance/reviews/${reviewId}` : '/conformance'
  if (!def) return null
  const how = def.evaluator === 'sh' ? def.sh : def.evaluator === 'llm' ? def.rubric : def.evaluator === 'tool' ? def.tool : def.probe
  return (
    <div className="stack conformance-how-judged">
      <div>
        <span className="muted">How it's judged: </span>
        <strong>{EVAL[def.evaluator] ?? def.evaluator}</strong>
        <span className="muted"> · {def.judgedWhere} · {def.block}@{def.version} · severity {def.severity}</span>
      </div>
      {how && <pre className="mono conformance-code conformance-code-short">{how}</pre>}
      {def.files && def.files.length > 0 && <div className="muted small">judged on: <span className="mono">{def.files.join(', ')}</span></div>}
      {def.evaluator === 'llm' && def.scope === 'diff' && <div className="muted small">Runs when a change lands.</div>}
      <div className="muted small">Adopted by <ProjectCount count={def.adopters.length} to={`${base}/checks/${encodeURIComponent(def.key)}/projects`} /></div>
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
    <div className="stack conformance-evidence">
      {v.sha && (
        <div className="muted small">
          judged <span className="mono">{shortSha(v.sha)}</span> · {fmtWhen(v.at)} · {EVAL[v.evaluator] ?? v.evaluator}
          {v.reviewId ? <> · <Link to={`/conformance/reviews/${v.reviewId}`} className="subtle">its review</Link></> : null}
          {v.evidenceHash && <> · evidence <span className="mono">{v.evidenceHash}</span></>}
        </div>
      )}
      {v.detail && <pre className="mono conformance-code">{v.detail}</pre>}
      {v.agentEvidence?.reused && <p className="small muted">Reused result: source and evaluator inputs are unchanged.</p>}
      {exc && (
        <div className="small conformance-exception">
          <span className="badge conformance-evidence-badge">{exc.status.replace('_', ' ')}</span>
          {covering ? <>{covering.justification} <span className="muted">— {covering.by}{covering.expiresAt ? `, until ${fmtWhen(covering.expiresAt)}` : ''}</span></> : <span className="muted">an exception by {exc.by}</span>}
        </div>
      )}
      {adj && (
        <div className="small conformance-confirmation">
          <span className="muted conformance-evidence-badge">confirmed</span>
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

export function coveringChange(view: ConformanceView, v: Verdict) {
  return view.openRemediations.find(c => c.projectId === v.projectId && c.block === v.block && c.version === v.version && c.check === v.check)
}

export function fixableFindings(view: ConformanceView, vs: Verdict[]) {
  return vs.filter(v => laneOf(v) === 'findings' && !coveringChange(view, v)).length
}

export function BeingFixed({ change }: { change: ConformanceView['openRemediations'][number] }) {
  return <span className="muted small">being fixed in <Link to={`/changes/${change.changeId}`}>change #{change.number}</Link></span>
}
