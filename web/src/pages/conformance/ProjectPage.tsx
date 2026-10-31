import { Fragment, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { post } from '../../api'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { ConformanceFixList, ConformanceHistory, ConformanceView, CheckDefinition, Adjudication, Disposition, Lanes, PriorConfirmation, RemediationOpened } from '../../wire.gen'
import { fmtWhen } from '../../format'
import { type Verdict, laneOf, shortSha, StandingChip, BrokenNote, StateChip, SevDot, LaneBar, HowJudged, Evidence, useStartRemediation } from './parts'

// --- one project ---------------------------------------------------------------------------------

function CheckRow({ v, def, exceptions, adj, prior, readOnly, onConfirm, onFix }: {
  v: Verdict; def?: CheckDefinition; exceptions: Disposition[]; adj?: Adjudication; prior?: PriorConfirmation; readOnly: boolean
  onConfirm?: (v: Verdict) => void; onFix?: (v: Verdict) => void
}) {
  const [open, setOpen] = useState(false)
  const lane = laneOf(v, adj ? { [String(v.id)]: adj } : {})
  return (
    <div style={{ borderTop: '1px solid var(--border)' }}>
      <div className="row" style={{ justifyContent: 'space-between', gap: 'var(--space-3)', padding: '8px 14px', cursor: 'pointer' }} onClick={() => setOpen(!open)}>
        <span className="row" style={{ gap: 'var(--space-2)', alignItems: 'center', minWidth: 0 }}>
          <span className="muted" style={{ fontSize: 'var(--fs-xs)' }}>{open ? '▾' : '▸'}</span>
          <SevDot sev={v.severity || def?.severity || 'note'} />
          <span style={{ fontSize: 'var(--fs-sm)' }}>{def?.title || v.check}</span>
          <span className="muted small phone-secondary" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{def?.title ? `${v.block}@${v.version} · ${v.check}` : def?.expects}</span>
        </span>
        <span className="row" style={{ gap: 'var(--space-2)', flex: '0 0 auto' }}>
          {lane === 'flagged' && prior && <span className="muted small" title={`confirmed before at ${shortSha(prior.sha)} by ${prior.by}`}>confirmed before at {shortSha(prior.sha)}</span>}
          <StateChip finding={v.state === 'does-not-conform'} label={v.statusLabel} help={v.detail} />
        </span>
      </div>
      {open && (
        <div style={{ paddingBottom: 'var(--space-3)' }}>
          <div style={{ padding: '0 14px 10px 30px' }}>
            <div className="small muted">{v.block}@{v.version} · {v.check}</div>
            <Evidence v={v} exceptions={exceptions} adj={adj} />
            {!readOnly && (lane === 'findings' || lane === 'flagged') && (
              <div className="row" style={{ gap: 'var(--space-2)', marginTop: 'var(--space-3)' }}>
                {lane === 'flagged' && onConfirm && <button onClick={() => onConfirm(v)}>Confirm finding</button>}
                {onFix && <button className={lane === 'findings' ? 'primary' : ''} onClick={() => onFix(v)}>Open a change to fix</button>}
                <span className="muted small">{lane === 'flagged' ? 'An AI flag counts as a finding only after a person confirms it.' : 'Opens a change with this check as its acceptance test.'}</span>
              </div>
            )}
          </div>
          <HowJudged def={def} />
        </div>
      )}
    </div>
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
    <div className="card" style={{ marginBottom: 'var(--space-4)', padding: '10px 14px' }}>
      <StaleNote q={q} />
      <div className="muted small mb-2">Oldest → newest. Red: findings. Amber: AI flags. Hover for review.</div>
      <div style={{ display: 'flex', gap: 3, alignItems: 'flex-end', height: 36 }}>
        {hist.map((h) => {
          const tot = h.findings + h.flagged
          const hgt = tot === 0 ? 3 : Math.max(6, (tot / max) * 36)
          const color = h.findings ? 'var(--bad)' : h.flagged ? 'var(--warn)' : h.unassessed ? 'var(--border-strong)' : 'var(--ok)'
          return (
            <Link key={h.reviewId} to={`/conformance/reviews/${h.reviewId}/projects/${slug}`}
              title={`review of ${fmtWhen(h.at)} · ${h.findings} findings · ${h.flagged} flagged · ${h.unassessed} not judged`}
              style={{ width: 14, height: hgt, background: color, borderRadius: 2, display: 'block', opacity: 0.9 }} />
          )
        })}
      </div>
    </div>
  )
}

const bySev = (a: Verdict, b: Verdict) => (['high', 'medium', 'low', 'note'].indexOf(a.severity) - ['high', 'medium', 'low', 'note'].indexOf(b.severity)) || a.check.localeCompare(b.check)

// One lane of a project's checks, folded or open. It is a component of its
// own, not one defined inside ProjectPage: a component made during a render
// is a new type each render, so every poll of a running review remounted
// the lanes and folded back whatever the person had opened.
function LaneSection({ lane, title, hint, collapsed, groups, view, exceptions, readOnly, onConfirm, onFix }: {
  lane: keyof Lanes; title: string; hint?: string; collapsed?: boolean
  groups: Record<keyof Lanes, Verdict[]>; view: ConformanceView; exceptions: Disposition[]; readOnly: boolean
  onConfirm: (v: Verdict) => void; onFix: (v: Verdict) => void
}) {
  const [open, setOpen] = useState(!collapsed)
  const list = [...groups[lane]].sort(bySev)
  if (list.length === 0) return null
  const tone = lane === 'findings' || lane === 'broken' ? ' tone-bad' : lane === 'flagged' ? ' tone-warn' : lane === 'excepted' ? ' tone-note' : ''
  return (
    <div className={`card${tone}`} style={{ padding: 0, marginBottom: 'var(--space-3)', overflow: 'hidden' }}>
      <div className="row" style={{ justifyContent: 'space-between', padding: '10px 14px', cursor: 'pointer' }} onClick={() => setOpen(!open)}>
        <span><strong>{title}</strong> <span className="muted small">({list.length})</span>{hint && <span className="muted small"> · {hint}</span>}</span>
        <span className="muted small">{open ? '▾' : '▸'}</span>
      </div>
      {open && list.map((v) => (
        <CheckRow key={v.id || `${v.block}${v.check}`} v={v} def={view.checkDefs[`${v.block}@${v.version}·${v.check}`]} exceptions={exceptions}
          adj={view.adjudications[String(v.id)]} prior={view.priorConfirmations[String(v.id)]} readOnly={readOnly}
          onConfirm={onConfirm} onFix={onFix} />
      ))}
    </div>
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
  if (!p) return <div className="muted" style={{ padding: '30px 0' }}>No conformance record for <span className="mono">{slug}</span>.</div>

  const groups: Record<keyof Lanes, Verdict[]> = { findings: [], flagged: [], broken: [], excepted: [], unassessed: [], na: [], conforms: [] }
  for (const v of vs) groups[laneOf(v, view.adjudications)].push(v)

  const confirm = async (v: Verdict) => {
    const note = window.prompt('Confirm this AI flag is a real finding. A short note (optional):', '') ?? null
    if (note === null) return
    await run(() => post(`/conformance/verdicts/${v.id}/confirm`, { note }))
  }
  const fix = async (v: Verdict) => {
    const r = await run(() => post<RemediationOpened>('/conformance/remediate', { project: slug, block: v.block, version: v.version, check: v.check, reviewId: v.reviewId, detail: v.detail }))
    if (r) nav(`/changes/${r.change.id}`)
  }

  const sectionProps = { groups, view, exceptions, readOnly: readOnly || busy, onConfirm: confirm, onFix: fix }

  return (
    <div>
      <div className="row" style={{ justifyContent: 'space-between', alignItems: 'flex-start', gap: 'var(--space-3)', marginBottom: 'var(--space-3)' }}>
        <div>
          <div className="row" style={{ gap: 'var(--space-3)', alignItems: 'center' }}>
            <h2 style={{ margin: 0, textTransform: 'none', letterSpacing: 0 }}>{p.name || slug}</h2>
            {p.name && p.name !== slug && <span className="mono muted small">{slug}</span>}
            <StandingChip broken={p.lanes.broken} findings={p.standing === 'does-not-conform' ? p.lanes.findings : undefined} label={p.statusLabel} help={p.sentence} />
            <LaneBar lanes={p.lanes} total={p.total} width={200} />
          </div>
          <div className="muted small mt-1">
            {p.lanes.findings > 0 && p.lanes.broken > 0 && <><BrokenNote broken={p.lanes.broken} />{' · '}</>}{p.judgedAt ? <>Checked {fmtWhen(p.judgedAt)} · </> : <>Not checked yet · </>}
            <Link to={`/projects/${slug}`} className="subtle">Open project →</Link>
          </div>
          <details className="small mt-1"><summary>{p.adopted.length} rules</summary><div className="muted">{p.adopted.join(', ') || 'None'}</div></details>
        </div>
        {!readOnly && (
          <span className="row" style={{ gap: 'var(--space-2)' }}>
            {p.lanes.findings > 0 && <button disabled={busy} onClick={() => remediation.start('project', slug)}>Fix {p.lanes.findings} problems</button>}
            <button disabled={busy} onClick={() => onRunOne(slug)}>Check now</button>
          </span>
        )}
      </div>
      {err && <div className="error small mb-3">{err}</div>}
      <HistoryStrip slug={slug} />
      <FixedSection slug={slug} />
      <LaneSection {...sectionProps} lane="findings" title="Findings" hint="the app does not meet these" />
      <LaneSection {...sectionProps} lane="broken" title="Couldn't run" hint="the check itself failed to run; the app is not judged" />
      <LaneSection {...sectionProps} lane="flagged" title="Needs your review" hint="Confirm whether these are problems" />
      <LaneSection {...sectionProps} lane="excepted" title="Approved exceptions" hint="fails, but accepted on the record" />
      <LaneSection {...sectionProps} lane="unassessed" title="Not checked yet" collapsed />
      <LaneSection {...sectionProps} lane="na" title="Not applicable" hint="checks that apply to nothing this app declares, or that judge one change and run when a change lands; each says why" collapsed />
      <LaneSection {...sectionProps} lane="conforms" title="Conforms" collapsed />
    </div>
  )
}


function FixedSection({ slug }: { slug: string }) {
  const q = useGet<ConformanceFixList>(`/conformance/projects/${slug}/fixes`)
  if (!q.data) return <ReadStatus q={q} what="the fixes" className="small" />
  const rows = q.data.fixes ?? []
  if (rows.length === 0) return <StaleNote q={q} />
  return (
    <div className="card tone-ok" style={{ padding: 0, marginBottom: 'var(--space-3)', overflow: 'hidden' }}>
      <StaleNote q={q} className="mt-2" />
      <div style={{ padding: '10px 14px' }}><strong>Fixed</strong> <span className="muted small">({rows.length})</span></div>
      {rows.map((r) => (
        <div key={r.fix.id} className="small" style={{ padding: '7px 14px', borderTop: '1px solid var(--border)' }}>
          <span className="mono">{r.fix.check}</span> — fixed {r.fix.changeIds.length === 1 ? <>by <Link to={`/changes/${r.fix.changeIds[0]}`}>{r.fix.changeNumbers[0] ? `change ${r.fix.changeNumbers[0]}` : 'a change'}</Link></> : r.fix.changeIds.length > 1 ? <>by one of changes {r.fix.changeIds.map((id, i) => <Fragment key={id ?? i}>{i > 0 ? ', ' : ''}<Link to={`/changes/${id}`}>{r.fix.changeNumbers[i] || 'a change'}</Link></Fragment>)}</> : `by commits ${r.fix.commits}`}
          , verified by <Link to={`/conformance/reviews/${r.fix.reviewId}/projects/${slug}`}>{r.fix.reviewNumber ? `review #${r.fix.reviewNumber}` : 'a review'}</Link>{r.fix.acceptedBy ? `, accepted by ${r.fix.acceptedBy}` : ''} <span className="muted">· {fmtWhen(r.fix.at)}</span>
          {r.fix.also && r.fix.also.length > 0 && <span className="muted"> · also fixed: {r.fix.also.join(', ')}</span>}
        </div>
      ))}
    </div>
  )
}
