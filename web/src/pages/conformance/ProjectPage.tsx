import './ProjectPage.css'
import { Fragment, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { post } from '../../api'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { ConformanceFixList, ConformanceHistory, ConformanceView, CheckDefinition, Adjudication, Disposition, Lanes, PriorConfirmation, RemediationOpened } from '../../wire.gen'
import { fmtWhen } from '../../format'
import { type Verdict, coveringChange, fixableFindings, BeingFixed, laneOf, shortSha, StateChip, HowJudged, Evidence, useStartRemediation } from './parts'
import { Section } from '../../Section'

// --- one project ---------------------------------------------------------------------------------

function CheckRow({ v, def, exceptions, adj, prior, readOnly, onConfirm, onFix, onRecheck, onDecision, covered }: {
  covered?: ConformanceView['openRemediations'][number]; v: Verdict; def?: CheckDefinition; exceptions: Disposition[]; adj?: Adjudication; prior?: PriorConfirmation; readOnly: boolean
  onConfirm?: (v: Verdict) => void; onFix?: (v: Verdict) => void; onRecheck?: () => void; onDecision?: (v: Verdict) => void
}) {
  const [open, setOpen] = useState(false)
  const lane = laneOf(v)
  const undecidedAI = v.evaluator === 'llm' && (lane === 'broken' || lane === 'unassessed')
  const decision = exceptions.find(d => d.fingerprint === `guidance|${v.block}@${v.version}|${v.check}|`)
  return (
    <>
      <div className="card row check-row project-page-check-row-card-check-row">
        <button className="row subtle check-open" aria-expanded={open} onClick={() => setOpen(!open)}>
          {/* A check that has not judged anything is neither a pass nor a problem: it says why instead. */}
          <span className="row-status">{lane === 'unassessed' ? <span className="muted small">not judged yet</span> : <StateChip finding={v.lane === 'findings'} label={v.statusLabel} />}</span>
          <strong>{def?.title || v.check}</strong>
          <span className="muted small">{lane === 'unassessed' || undecidedAI
            ? (v.detail ? v.detail.charAt(0).toUpperCase() + v.detail.slice(1) : 'Not run yet')
            : [`from ${v.block}`, lane === 'flagged' && prior ? `confirmed before at ${shortSha(prior.sha)} by ${prior.by}` : ''].filter(Boolean).join(' · ')}</span>
        </button>
        {covered && <BeingFixed change={covered} />}
        {!readOnly && undecidedAI && <div className="row project-page-check-row-div-2">
          <button onClick={onRecheck}>Re-check</button>
          {!decision && <button onClick={() => onDecision?.(v)}>Record a decision</button>}
        </div>}
      </div>
      {open && (
        <div className="check-results">
          <div className="project-page-check-row-div">
            <div className="small muted">{v.block} version {v.version} · {v.check}</div>
            <Evidence v={v} exceptions={exceptions} adj={adj} />
            {undecidedAI && decision && <p className="small">Recorded decision: {decision.status === 'accepted' ? 'accepted risk' : 'false positive'} — {decision.justification} · {decision.by}. This check remains undecided.</p>}
            {!readOnly && (lane === 'findings' || lane === 'flagged') && (
              <div className="row mt-3 project-page-check-row-div-2">
                {lane === 'flagged' && onConfirm && <button onClick={() => onConfirm(v)}>Confirm finding</button>}
                {onFix && !covered && <button className={lane === 'findings' ? 'primary' : ''} onClick={() => onFix(v)}>Open a change to fix</button>}
                <span className="muted small">{covered ? '' : lane === 'flagged' ? 'A possible problem counts as a finding only after a person confirms it.' : 'Opens a change with this check as its acceptance test.'}</span>
              </div>
            )}
          </div>
          <HowJudged def={def} />
        </div>
      )}
    </>
  )
}

function HistoryStrip({ slug }: { slug: string }) {
  const q = useGet<ConformanceHistory>(`/conformance/projects/${slug}/history`)
  // A strip never read says so; a held one stays (it changes only when a
  // review finishes).
  if (!q.data) return <ReadStatus q={q} what="the review history" className="small" />
  const hist = q.data.history ?? []
  if (hist.length < 2) return <StaleNote q={q} />
  const max = Math.max(1, ...hist.map((h) => h.findings + h.flagged))
  return (
    <Section title="Reviews" count={hist.length} note="Oldest to newest. Red: findings. Amber: possible problems. Select a bar to open that review.">
      <StaleNote q={q} />
      <div className="project-page-history-strip-div">
        {hist.map((h) => {
          const tot = h.findings + h.flagged
          const hgt = tot === 0 ? 3 : Math.max(6, (tot / max) * 36)
          const color = h.findings ? 'var(--bad)' : h.flagged ? 'var(--warn)' : h.unassessed ? 'var(--border-strong)' : 'var(--ok)'
          return (
            <Link key={h.reviewId} to={`/conformance/reviews/${h.reviewId}/projects/${slug}`}
              title={`review of ${fmtWhen(h.at)} · ${h.findings} findings · ${h.flagged} flagged · ${h.unassessed} not judged`}
              style={{ height: hgt, background: color }} className="project-page-link" />
          )
        })}
      </div>
    </Section>
  )
}

const bySev = (a: Verdict, b: Verdict) => (['high', 'medium', 'low', 'note'].indexOf(a.severity) - ['high', 'medium', 'low', 'note'].indexOf(b.severity)) || a.check.localeCompare(b.check)

// One lane of a project's checks, a section with a row per check. It is a
// component of its own, not one defined inside ProjectPage: a component made
// during a render is a new type each render, so every poll of a running
// review remounted the lanes and closed whatever the person had opened.
function LaneSection({ lane, title, hint, groups, view, exceptions, readOnly, onConfirm, onFix, onRecheck, onDecision }: {
  lane: keyof Lanes; title: string; hint?: string
  groups: Record<keyof Lanes, Verdict[]>; view: ConformanceView; exceptions: Disposition[]; readOnly: boolean
  onConfirm: (v: Verdict) => void; onFix: (v: Verdict) => void; onRecheck: () => void; onDecision: (v: Verdict) => void
}) {
  const list = [...groups[lane]].sort(bySev)
  if (list.length === 0) return null
  return (
    <Section title={title} count={list.length} note={hint}>
      {list.map((v) => (
        <CheckRow key={v.id || `${v.block}${v.check}`} v={v} def={view.checkDefs[`${v.block}@${v.version}·${v.check}`]} exceptions={exceptions}
          covered={coveringChange(view, v)} adj={view.adjudications[String(v.id)]} prior={view.priorConfirmations[String(v.id)]} readOnly={readOnly}
          onConfirm={onConfirm} onFix={onFix} onRecheck={onRecheck} onDecision={onDecision} />
      ))}
    </Section>
  )
}

export function ProjectPage({ view, slug, readOnly, onRunOne }: {
  view: ConformanceView; slug: string; readOnly: boolean; onRunOne: (slug: string) => void
}) {
  const nav = useNavigate()
  const { run, busy: acting, err: actErr } = useAction()
  const remediation = useStartRemediation()
  const p = view.projects.find((x) => x.slug === slug)
  const vs = view.byProject[slug] ?? []
  const exceptions = view.exceptions[slug] ?? []
  const busy = acting || remediation.busy
  const err = actErr || remediation.err
  if (!p) return <div className="muted project-page-div">No conformance record for <span className="mono">{slug}</span>.</div>

  const groups: Record<keyof Lanes, Verdict[]> = { findings: [], flagged: [], broken: [], excepted: [], unassessed: [], na: [], conforms: [] }
  for (const v of vs) groups[laneOf(v)].push(v)

  const confirm = async (v: Verdict) => {
    const note = window.prompt('Confirm this possible problem is a real finding. A short note (optional):', '') ?? null
    if (note === null) return
    await run(() => post(`/conformance/verdicts/${v.id}/confirm`, { note }))
  }
  const fix = async (v: Verdict) => {
    const r = await run(() => post<RemediationOpened>('/conformance/remediate', { project: slug, block: v.block, version: v.version, check: v.check, reviewId: v.reviewId, detail: v.detail }))
    if (r) nav(`/changes/${r.change.id}`)
  }

  const recordDecision = async (v: Verdict) => {
    const justification = window.prompt(`Accept the risk that ${v.check} may not be met for ${v.block} version ${v.version}. This decision covers this check version until revoked. An undecided result stays undecided. Reason:`, '')
    if (!justification?.trim()) return
    await run(() => post(`/projects/${slug}/dispositions`, { fingerprint: `guidance|${v.block}@${v.version}|${v.check}|`, status: 'accepted', justification }))
  }
  const fixable = fixableFindings(view, vs)
  const sectionProps = { groups, view, exceptions, readOnly: readOnly || busy, onConfirm: confirm, onFix: fix, onRecheck: () => onRunOne(slug), onDecision: recordDecision }

  return (
    <div>
      <div className="row mt-3 project-page-div-2">
        <h2 className="grow project-page-h2">{p.name || slug}</h2>
        {!readOnly && (
          <span className="row project-page-check-row-div-2">
            {fixable > 0 && <button disabled={busy} onClick={() => remediation.start('project', slug)}>Fix {fixable} {fixable === 1 ? 'problem' : 'problems'}</button>}
            <button disabled={busy} onClick={() => onRunOne(slug)}>Check this project</button>
          </span>
        )}
      </div>
      <div className="muted small mt-1">
        {/* The owner's sentence already says what could not run; the findings count alone does not. */}
        {[p.standing === 'does-not-conform' && p.lanes.findings > 0 ? `${p.lanes.findings} ${p.lanes.findings === 1 ? 'finding' : 'findings'}` : p.sentence.replace(/\.$/, ''),
          p.standing === 'does-not-conform' && p.lanes.findings > 0 && p.lanes.broken > 0 ? `${p.lanes.broken} couldn't run` : '',
          p.judgedAt ? `checked ${fmtWhen(p.judgedAt)}` : 'not checked yet'].filter(Boolean).join(' · ')}
        {' · '}<Link to={`/projects/${slug}`}>Open project →</Link>
      </div>
      {err && <div className="error small mb-3">{err}</div>}
      <HistoryStrip slug={slug} />
      <FixedSection slug={slug} />
      <LaneSection {...sectionProps} lane="findings" title="Findings" hint="the app does not meet these" />
      <LaneSection {...sectionProps} lane="broken" title="Couldn't run" hint="the check itself failed to run; the app is not judged" />
      <LaneSection {...sectionProps} lane="flagged" title="Needs your review" hint="Confirm whether these are problems" />
      <LaneSection {...sectionProps} lane="excepted" title="Approved exceptions" hint="fails, but accepted on the record" />
      <LaneSection {...sectionProps} lane="unassessed" title="Not judged yet" hint="Waiting for something before they can judge, or not run yet" />
      <LaneSection {...sectionProps} lane="na" title="Not applicable" hint="checks that apply to nothing this app declares, or that judge one change and run when a change lands; each says why" />
      <LaneSection {...sectionProps} lane="conforms" title="Conforms" />
    </div>
  )
}


function FixedSection({ slug }: { slug: string }) {
  const q = useGet<ConformanceFixList>(`/conformance/projects/${slug}/fixes`)
  if (!q.data) return <ReadStatus q={q} what="the fixes" className="small" />
  const rows = q.data.fixes ?? []
  if (rows.length === 0) return <StaleNote q={q} />
  return (
    <Section title="Fixed" count={rows.length}>
      <StaleNote q={q} className="mt-2" />
      {rows.map((r) => (
        <div key={r.fix.id} className="card small">
          <span className="mono">{r.fix.check}</span> — fixed {r.fix.changeIds.length === 1 ? <>by <Link to={`/changes/${r.fix.changeIds[0]}`}>{r.fix.changeNumbers[0] ? `change ${r.fix.changeNumbers[0]}` : 'a change'}</Link></> : r.fix.changeIds.length > 1 ? <>by one of changes {r.fix.changeIds.map((id, i) => <Fragment key={id ?? i}>{i > 0 ? ', ' : ''}<Link to={`/changes/${id}`}>{r.fix.changeNumbers[i] || 'a change'}</Link></Fragment>)}</> : `by commits ${r.fix.commits}`}
          , verified by <Link to={`/conformance/reviews/${r.fix.reviewId}/projects/${slug}`}>{r.fix.reviewNumber ? `review #${r.fix.reviewNumber}` : 'a review'}</Link>{r.fix.acceptedBy ? `, accepted by ${r.fix.acceptedBy}` : ''} <span className="muted">· {fmtWhen(r.fix.at)}</span>
          {r.fix.also && r.fix.also.length > 0 && <span className="muted"> · also fixed: {r.fix.also.join(', ')}</span>}
        </div>
      ))}
    </Section>
  )
}
