import { ListTable } from '../../ListTable'
import './list-migrations.css'
import { QuietPrompt } from '../../QuietConfirm'
import './ProjectPage.css'
import { Fragment } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { post } from '../../api'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { ConformanceFixList, ConformanceHistory, ConformanceView, CheckDefinition, Adjudication, Disposition, Lanes, PriorConfirmation, RemediationOpened } from '../../wire.gen'
import { fmtWhen } from '../../format'
import { type Verdict, coveringChange, fixableFindings, BeingFixed, couldNotRun, laneOf, shortSha, StateChip, HowJudged, Evidence, JudgedAt, useStartRemediation } from './parts'
import { Section } from '../../Section'
import { PageHead } from '../../PageHead'
import { ListRow } from '../../ListRow'

// --- one project ---------------------------------------------------------------------------------

function CheckRow({ v, def, exceptions, adj, prior, readOnly, onConfirm, onFix, onRecheck, onDecision, covered, reviews }: {
  reviews: ConformanceView['reviews']; covered?: ConformanceView['openRemediations'][number]; v: Verdict; def?: CheckDefinition; exceptions: Disposition[]; adj?: Adjudication; prior?: PriorConfirmation; readOnly: boolean
  onConfirm?: (v: Verdict, note: string) => unknown; onFix?: (v: Verdict) => void; onRecheck?: () => void; onDecision?: (v: Verdict, reason: string) => unknown
}) {
  const lane = laneOf(v)
  const undecidedAI = v.evaluator === 'llm' && (lane === 'broken' || lane === 'unassessed')
  const decision = exceptions.find(d => d.fingerprint === `guidance|${v.block}@${v.version}|${v.check}|`)
  return (
    <>
      <ListRow identity={<>
        <div className="row row-3">
          {/* A check that has not judged anything is neither a pass nor a problem: it says why instead. */}
          {lane === 'unassessed' && v.state !== 'stale' ? <span className="muted small">not judged yet</span> : <StateChip finding={v.lane === 'findings'} label={v.statusLabel} />}
          <strong>{def?.title || v.check}</strong>
        </div>
        <span className="muted small">{v.state === 'stale' ? 'No judgement on the current commit' : lane === 'unassessed' || undecidedAI
          ? (v.detail ? v.detail.charAt(0).toUpperCase() + v.detail.slice(1) : 'Not run yet')
          : [`from ${v.block}`, lane === 'flagged' && prior ? `confirmed before at ${shortSha(prior.sha)} by ${prior.by}` : ''].filter(Boolean).join(' · ')}</span>
      </>}
        state={covered && <BeingFixed change={covered} />}
        action={!readOnly && undecidedAI && <>
          <button onClick={onRecheck}>Re-check</button>
          {!decision && <QuietPrompt label="Accept risk" yes="Accept risk"
            ask={<>Accept the risk that {def?.title || v.check} may not be met for {v.block} version {v.version} on this project. This decision covers this check version, until revoked. An undecided result stays undecided.</>}
            fields={[{ key: 'reason', label: 'Reason for accepting this risk', placeholder: 'Reason (required)', required: true }]}
            onYes={({ reason }) => onDecision?.(v, reason.trim())} />}
        </>}
      />
      {(
        <div className="check-results">
          <div className="project-page-check-row-div">
            <div className="small muted">{v.block} version {v.version} · {v.check}</div>
            <Evidence v={v} exceptions={exceptions} adj={adj} reviews={reviews} />
            {undecidedAI && decision && <p className="small">Recorded decision: {decision.status === 'accepted' ? 'accepted risk' : 'false positive'} — {decision.justification} · {decision.by}. This check remains undecided.</p>}
            {!readOnly && (lane === 'findings' || lane === 'flagged') && (
              <div className="row mt-3 project-page-check-row-div-2">
                {lane === 'flagged' && onConfirm && <QuietPrompt label="Confirm finding" yes="Confirm finding"
                  ask="Confirm this possible problem is a real finding."
                  fields={[{ key: 'note', label: 'Finding note (optional)', placeholder: 'Note (optional)' }]}
                  onYes={({ note }) => onConfirm(v, note)} />}
                {onFix && !covered && <button className={lane === 'findings' ? 'primary' : ''} onClick={() => onFix(v)}>Open a change to fix</button>}
                <span className="muted small">{covered ? '' : lane === 'flagged' ? 'A possible problem counts as a finding only after a person confirms it.' : 'Opens a change with this check as its acceptance test.'}</span>
              </div>
            )}
          </div>
          <HowJudged def={def} brief />
        </div>
      )}
    </>
  )
}

function ProjectReviews({ slug }: { slug: string }) {
  const q = useGet<ConformanceHistory>(`/conformance/projects/${slug}/history`)
  if (!q.data) return <ReadStatus q={q} what="the review history" className="small" />
  const hist = q.data.history ?? []
  return (
    <Section title="Reviews" count={hist.length} note="Dated snapshots of this project. Open a report to see the evidence recorded then.">
      <StaleNote q={q} />
      {hist.length === 0 && <p className="muted small">No reviews yet.</p>}
      {hist.length > 0 && <ListTable className="small" label="Review history" rows={[...hist].reverse()} rowKey={h => String(h.reviewId)} columns={[
        { key: 'review', label: 'Review', phone: 'name', render: h => <Link to={`/conformance/reviews/${h.reviewId}/projects/${slug}`}>Review · {fmtWhen(h.at)}</Link> },
        { key: 'findings', label: 'Findings', phone: 'state', kind: 'number', render: h => <><span className="record-phone">Findings: </span>{h.findings}</> },
        { key: 'flagged', label: 'Possible problems', kind: 'number', render: h => <><span className="record-phone">Possible problems: </span>{h.flagged}</> },
        { key: 'unassessed', label: 'Not judged', kind: 'number', render: h => <><span className="record-phone">Not judged: </span>{h.unassessed}</> },
      ]} />}
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
  onConfirm: (v: Verdict, note: string) => unknown; onFix: (v: Verdict) => void; onRecheck: () => void; onDecision: (v: Verdict, reason: string) => unknown
}) {
  const { reviewId } = useParams()
  const list = [...groups[lane]].sort(bySev)
  if (list.length === 0) return null
  // Lanes with nothing to do here are a table of checks; each row opens the
  // check's page, which holds its evidence across projects and how it judges.
  if (!['findings', 'broken', 'flagged'].includes(lane)) {
    const base = reviewId ? `/conformance/reviews/${reviewId}` : '/conformance'
    const key = (v: Verdict) => `${v.block}@${v.version}·${v.check}`
    return <Section title={title} count={list.length} note={hint}>
      <ListTable label={title} rows={list} rowKey={v => String(v.id || key(v))} rowTo={v => `${base}/checks/${encodeURIComponent(key(v))}`} columns={[
        { key: 'check', label: 'Check', phone: 'name', render: v => <Link to={`${base}/checks/${encodeURIComponent(key(v))}`}>{view.checkDefs[key(v)]?.title || v.check}</Link> },
        { key: 'why', label: 'Result', render: v => <span className="muted">{v.state === 'stale' ? 'No judgement on the current commit' : v.detail ? (laneOf(v) === 'unassessed' ? v.detail.charAt(0).toUpperCase() + v.detail.slice(1) : v.detail) : `from ${v.block}`}</span> },
        { key: 'judged', label: 'Judged', render: v => <JudgedAt v={v} reviews={view.reviews ?? []} /> },
        { key: 'status', label: 'Status', phone: 'state', render: v => laneOf(v) === 'unassessed' && v.state !== 'stale' ? <span className="muted">—</span> : <StateChip label={v.statusLabel} /> },
      ]} />
    </Section>
  }
  return (
    <Section title={title} count={list.length} note={hint}>
      {list.map((v) => (
        <CheckRow key={v.id || `${v.block}${v.check}`} v={v} def={view.checkDefs[`${v.block}@${v.version}·${v.check}`]} exceptions={exceptions}
          covered={coveringChange(view, v)} adj={view.adjudications[String(v.id)]} prior={view.priorConfirmations[String(v.id)]} readOnly={readOnly} reviews={view.reviews ?? []}
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

  const confirm = async (v: Verdict, note: string) => {
    const saved = await run(async () => { await post(`/conformance/verdicts/${v.id}/confirm`, { note }); return true })
    if (!saved) throw new Error('The finding could not be confirmed. Your note is kept here; try again.')
  }
  const fix = async (v: Verdict) => {
    const r = await run(() => post<RemediationOpened>('/conformance/remediate', { project: slug, block: v.block, version: v.version, check: v.check, reviewId: v.reviewId, detail: v.detail }))
    if (r) nav(`/changes/${r.change.id}`)
  }

  const recordDecision = async (v: Verdict, justification: string) => {
    const saved = await run(async () => { await post(`/projects/${slug}/dispositions`, { fingerprint: `guidance|${v.block}@${v.version}|${v.check}|`, status: 'accepted', justification }); return true })
    if (!saved) throw new Error('The risk acceptance could not be saved. Your reason is kept here; try again.')
  }
  const fixable = fixableFindings(view, vs)
  const sectionProps = { groups, view, exceptions, readOnly, onConfirm: confirm, onFix: fix, onRecheck: () => onRunOne(slug), onDecision: recordDecision }

  return (
    <div>
      <PageHead page="conformance" title={p.name || slug} actions={!readOnly && <span className="row project-page-check-row-div-2">
        {fixable > 0 && <button disabled={busy} onClick={() => remediation.start('project', slug)}>Fix {fixable} {fixable === 1 ? 'problem' : 'problems'}</button>}
        <button className="primary" disabled={busy} onClick={() => onRunOne(slug)}>Check this project</button>
      </span>} />
      <div className="muted small">
        {/* A lead line is a summary: checks that could not run are counted, and
            each one's reason is on its row below. The owner's sentence carries
            those reasons, so it leads only when nothing failed to run. */}
        {[p.standing === 'does-not-conform' && p.lanes.findings > 0 ? `${p.lanes.findings} ${p.lanes.findings === 1 ? 'finding' : 'findings'}` : p.notKnown?.length ? couldNotRun(p.notKnown.length) : p.sentence.replace(/\.$/, ''),
          p.standing === 'does-not-conform' && p.lanes.findings > 0 && p.lanes.broken > 0 ? couldNotRun(p.lanes.broken) : '',
          p.judgedAt ? `checked ${fmtWhen(p.judgedAt)}` : 'not checked yet'].filter(Boolean).join(' · ')}
        {' · '}<Link to={`/projects/${slug}`}>Open project →</Link>
      </div>
      {err && <div className="error small mb-3">{err}</div>}
      <LaneSection {...sectionProps} lane="findings" title="Findings" hint="the app does not meet these" />
      <LaneSection {...sectionProps} lane="broken" title="Couldn't run" hint="the check itself failed to run; the app is not judged" />
      <LaneSection {...sectionProps} lane="flagged" title="Needs your review" hint="Confirm whether these are problems" />
      <LaneSection {...sectionProps} lane="excepted" title="Approved exceptions" hint="fails, but accepted on the record" />
      <LaneSection {...sectionProps} lane="unassessed" title="No current judgement" hint="Checks not run yet, waiting for something, or judged on an older commit" />
      <LaneSection {...sectionProps} lane="na" title="Not applicable" hint="checks that apply to nothing this app declares, or that judge one change and run when a change lands; each says why" />
      <LaneSection {...sectionProps} lane="conforms" title="Conforms" />
      {!readOnly && <><FixedSection slug={slug} /><ProjectReviews slug={slug} /></>}
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
        <ListRow key={r.fix.id} className="card small fixed-list-row" identity={<>
          <span className="mono">{r.fix.check}</span> — fixed {r.fix.changeIds.length === 1 ? <>by <Link to={`/changes/${r.fix.changeIds[0]}`}>{r.fix.changeNumbers[0] ? `change ${r.fix.changeNumbers[0]}` : 'a change'}</Link></> : r.fix.changeIds.length > 1 ? <>by one of changes {r.fix.changeIds.map((id, i) => <Fragment key={id ?? i}>{i > 0 ? ', ' : ''}<Link to={`/changes/${id}`}>{r.fix.changeNumbers[i] || 'a change'}</Link></Fragment>)}</> : `by commits ${r.fix.commits}`}
          , verified by <Link to={`/conformance/reviews/${r.fix.reviewId}/projects/${slug}`}>{r.fix.reviewNumber ? `review #${r.fix.reviewNumber}` : 'a review'}</Link>{r.fix.acceptedBy ? `, accepted by ${r.fix.acceptedBy}` : ''} <span className="muted">· {fmtWhen(r.fix.at)}</span>
          {r.fix.also && r.fix.also.length > 0 && <span className="muted"> · also fixed: {r.fix.also.join(', ')}</span>}</>} />
      ))}
    </Section>
  )
}
