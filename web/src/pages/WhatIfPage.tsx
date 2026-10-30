import { Status, STATUS_LIGHT } from '../Status'
import { Fragment, useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { currentUser, get, HttpError, post } from '../api'
import { QuietConfirm } from '../QuietConfirm'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { relTime } from '../format'
import type { AdoptionUpliftOpened, AuthzHoldings, InAdvanceQuestion, PackHistory, WhatIf, WhatIfCheck, WhatIfFleetGap, WhatIfMove, WhatIfRow, WhatIfScore } from '../wire.gen'

// One what-if on a pack version (design/subsystems/conformance.md):
// one row per project saying what taking the version would change, worst
// first, with its score and every rule it moves, then the decision through
// the existing doors: recommend the version, or try it in an eval. Unknown
// is never shown as clear, and a rule moved back is never shown as nothing.

// REASON_SHORT says why checks were not judged in a few words, for a
// project's row; the fleet summary and the hover say it in full, with what
// would make them assessable.
const REASON_SHORT: Record<string, string> = {
  error: 'could not answer',
  'no-image': 'need an image',
  'no-running-app': 'need the running app',
  'rubrics-not-included': 'need model rubrics',
  'spend-limit': 'past the spend limit',
  'spend-unknown': 'spend unknown',
  'judges-a-change': 'judge a change',
  'no-evaluator': 'no evaluator',
  'no-main': 'no main',
  'not-adoptable': 'not adoptable',
}

// RULE_MOVES are the results that are rule moves, not check answers.
const RULE_MOVES = new Set(['added', 'newer-version', 'older-version', 'removed'])

export const OUTCOME_WORDS: Record<string, string> = {
  'new-failures': 'new failures',
  'could-not-assess': 'could not assess',
  'no-new-failures': 'no new failures',
  'not-evaluated': 'not evaluated',
  'not-yet-judged': 'not yet judged',
}

export const OUTCOME_ORDER = ['new-failures', 'could-not-assess', 'no-new-failures', 'not-evaluated', 'not-yet-judged']

export default function WhatIfPage() {
  const { id = '' } = useParams()
  const [running, setRunning] = useState(true)
  const q = useGet<WhatIf>(`/conformance/what-if/${id}`, { poll: running ? 3000 : undefined })
  const w = q.data
  if (w && running !== (w.state === 'queued' || w.state === 'running')) setRunning(!running)
  if (!w) return <ReadStatus q={q} what="the what-if" />
  return (
    <div style={{ maxWidth: 1000 }}>
      <div className="muted small"><Link to="/conformance/what-ifs">What-ifs</Link> · <Link to="/packs">Packs</Link> › <Link to={`/packs/${w.pack}`}>{w.pack}</Link></div>
      <h1 style={{ marginBottom: 4 }}>What-if: {w.pack} v{w.version}</h1>
      <StaleNote q={q} />
      <div className="row small" style={{ flexWrap: 'wrap', gap: 8 }}>
        <State w={w} />
        <span className="muted">{w.requestedBy} · {relTime(w.createdAt)}</span>
        <span className="muted">
          {w.rubrics
            ? `model rubrics: ${w.incomplete > 0 ? 'at least ' : ''}$${(w.costMicros / 1e6).toFixed(2)} of $${(w.spendLimitMicros / 1e6).toFixed(2)}`
            : 'model rubrics not included'}
        </span>
        {w.advanceWorkflows && <span className="muted">moves each project's other workflows to the newest version</span>}
        {(w.also ?? []).length > 0 && <span className="muted">with, for projects already on them: {alsoWords(w)}</span>}
      </div>
      <Decide w={w} />
      <Summary w={w} />
      <Fleet gaps={w.fleet} projects={w.projects.length} />
      <Table w={w} />
    </div>
  )
}

function State({ w }: { w: WhatIf }) {
  return <><Status word={w.statusLabel} tone={STATUS_LIGHT[w.statusLabel]} help={w.detail || w.state} /> <span>{w.projectsDone} of {w.projectsTotal}</span></>
}

// Decide: recommend the version (the pack page's own act) or try it in an
// eval. The button says why it cannot be pressed rather than vanishing, and
// says so only from answers it has: a pack or holdings read that failed or
// is on its way is said as such, never as "recommended" or "not allowed".
function Decide({ w }: { w: WhatIf }) {
  const packQ = useGet<PackHistory>(`/packs/${w.pack}`, { kind: 'config' })
  const meQ = useGet<AuthzHoldings>('/authz/me', { kind: 'config' })
  const { run, busy, err } = useAction(packQ.reload, { affects: [`/packs/${w.pack}`] })
  const recommended = packQ.data?.recommended
  const holds = meQ.data ? meQ.data.instance?.['standards:manage-packs'] === true : undefined
  const why = recommended === undefined ? 'the pack has not been read'
    : recommended === w.version ? `v${w.version} is recommended`
      : holds === undefined ? (meQ.err ? `what you may do could not be read: ${meQ.reason || meQ.err}` : 'reading what you may do…')
        : !holds ? 'needs standards:manage-packs' : ''
  return (
    <div className="row" style={{ flexWrap: 'wrap', gap: 8, margin: '12px 0' }}>
      {why
        ? <button className="subtle" disabled title={why}>Recommend v{w.version}</button>
        : <QuietConfirm label={`Recommend v${w.version}`} className="subtle" yes="Recommend it" disabled={busy}
            ask={`Recommend v${w.version}, as assessed in this what-if? New projects get it; projects on other versions are offered it. No project moves.`}
            onYes={() => run(() => post(`/packs/${w.pack}/recommended`, { version: w.version, whatIf: w.id }))} />}
      {recommended === undefined ? <ReadStatus q={packQ} what="the pack" className="small" /> : why && <span className="muted small">{why}</span>}
      <StaleNote q={[packQ, meQ]} inline />
      <Link className="button subtle" to={`/evals/new?pack=${encodeURIComponent(w.pack)}&version=${w.version}`}>Try in an eval</Link>
      {err && <span className="error">{err}</span>}
    </div>
  )
}

function Summary({ w }: { w: WhatIf }) {
  const projects = w.projects
  const counts: Record<string, number> = {}
  for (const p of projects) counts[p.outcome] = (counts[p.outcome] ?? 0) + 1
  return (
    <div className="row" style={{ flexWrap: 'wrap', gap: 6, marginBottom: 8 }}>
      <strong>{projects.length} project{projects.length === 1 ? '' : 's'}</strong>
      {OUTCOME_ORDER.filter((o) => counts[o]).map((o) => <span key={o}>{counts[o]} <Status word={w.outcomeLabels[o]} tone={STATUS_LIGHT[w.outcomeLabels[o]]} help={OUTCOME_WORDS[o]} /></span>)}
      {w.movesBackProjects > 0 && <span className="chip moves-back">{w.movesBackProjects} move rules back</span>}
    </div>
  )
}

// Fleet is the checks the version could not judge, across projects, by
// check and reason, most projects first: a check that can never be judged
// stands out, with what would make it assessable.
function Fleet({ gaps, projects }: { gaps: WhatIfFleetGap[], projects: number }) {
  if (gaps.length === 0) return null
  const shown = gaps.slice(0, 8)
  return (
    <details className="card" style={{ padding: '6px 10px', margin: '8px 0' }} open={gaps[0].projects > 1}>
      <summary><strong>Could not assess, across projects</strong> <span className="muted small">{gaps.length} check{gaps.length === 1 ? '' : 's'}</span></summary>
      {shown.map((g, i) => (
        <div key={i} className="small" style={{ marginTop: 6 }}>
          <div>{g.block ? <code style={{ overflowWrap: 'anywhere' }}>{g.block} · {g.check}</code> : <span>the whole project</span>} <strong>{g.projects} of {projects}</strong> <span className="muted">— {g.words}</span></div>
          <div>To assess: {g.remedy}</div>
        </div>
      ))}
      {gaps.length > shown.length && <div className="muted small">and {gaps.length - shown.length} more; each project's row lists its own.</div>}
    </details>
  )
}

// ScoreLine says the project's checks by the version's side.
function ScoreLine({ s }: { s: WhatIfScore }) {
  if (s.checks === 0) return <span className="muted">no checks</span>
  const parts = [`${s.checks} check${s.checks === 1 ? '' : 's'}`, `${s.passed} pass`]
  if (s.failing) parts.push(`${s.failing} fail${s.failing === 1 ? 's' : ''}`)
  if (s.couldNotAssess) parts.push(`${s.couldNotAssess} could not assess`)
  if (s.noLongerRequired) parts.push(`${s.noLongerRequired} no longer required`)
  if (s.notApplicable) parts.push(`${s.notApplicable} not applicable`)
  return <span>{parts.join(' · ')}</span>
}

function moveWords(m: WhatIfMove): string {
  if (!m.from) return `adds ${m.rule} v${m.to}`
  if (!m.to) return `removes ${m.rule} (v${m.from})`
  return `${m.rule} v${m.from} → v${m.to}`
}

// Moves lists the rules a project's candidate moves, the ones going back
// first and marked.
function Moves({ moves }: { moves: WhatIfMove[] }) {
  if (moves.length === 0) return null
  const ordered = [...moves.filter((m) => m.back), ...moves.filter((m) => !m.back)]
  return (
    <div className="card" style={{ padding: '6px 10px', marginTop: 6 }}>
      <strong>Rule changes</strong>
      {ordered.map((m, i) => (
        <div key={i} className={m.back ? 'warn-line' : 'muted'}>{m.back && 'moves back: '}<code>{moveWords(m)}</code></div>
      ))}
    </div>
  )
}

// Adopted is what asking one project to take the version came to: the
// Change it opened, AppTown's refusal in its words, or not known (the
// answer never arrived, or AppTown failed while answering).
type Adopted = { changeId: string | null; changeNumber: number; slug: string; started?: string; runError?: string; notStarted?: string } | { refused: string } | { unknown: string }

// Table is one row per project. A project with a candidate the what-if
// assessed can be ticked; adopting asks each ticked project's own adopt
// door in turn, bound to that candidate, and its row shows what came of it.
function Table({ w }: { w: WhatIf }) {
  // Rows are keyed by project id.
  const [open, setOpen] = useState<string | null>(null)
  const [ticked, setTicked] = useState<string[]>([])
  const [adopted, setAdopted] = useState<Record<string, Adopted>>({})
  // The run belongs to the person who confirmed it on this page: once the
  // page goes (the app unmounts it when the person acting changes) or the
  // person acting is someone else, no further project is asked. Requests
  // already sent settle; the rest stay unasked.
  const here = useRef(true)
  useEffect(() => { here.current = true; return () => { here.current = false } }, [])
  // A yes in advance: each chosen project's own question, read when the
  // person opens the confirmation and shown there; what was shown is what
  // each project's request sends back.
  const [inAdvance, setInAdvance] = useState(false)
  const asked = useRef<Record<string, InAdvanceQuestion>>({})
  const { run, busy } = useAction(undefined, { affects: ['/changes', '/projects'] })
  if (w.projects.length === 0) return <div className="muted small">No projects selected.</div>
  const chosen = w.projects.filter((p) => p.projectId && ticked.includes(p.projectId))
  const back = chosen.filter((p) => p.score.movesBack > 0)
  const backWords = back.length === 0 ? '' : ` ${back.length} of ${chosen.length} move rules back: ` +
    back.map((p) => `${p.projectSlug} (${p.moves.filter((m) => m.back).map(moveWords).join(', ')})`).join('; ') + '.'
  const asking = busy
  const adopt = () => run(async () => {
    const who = currentUser()
    for (const p of chosen) {
      if (!here.current || currentUser() !== who) return
      let r: Adopted
      try {
        const q = inAdvance ? asked.current[p.projectSlug] : undefined
        const shown = q && !q.notOffered ? [q.acceptance, ...q.landing] : undefined
        const o = await post<AdoptionUpliftOpened>(`/projects/${encodeURIComponent(p.projectSlug)}/adoption/adopt`, shown ? { fromWhatIf: w.id, viaChange: true, inAdvance: { shown } } : { fromWhatIf: w.id, viaChange: true })
        r = { changeId: o.changeId, changeNumber: o.changeNumber, slug: p.projectSlug, started: o.workflow ?? undefined, runError: o.runError ?? undefined, notStarted: o.notStarted ?? undefined }
      } catch (e) {
        r = e instanceof HttpError && e.status < 500 ? { refused: e.message } : { unknown: (e as Error).message }
      }
      if (here.current) setAdopted((was) => ({ ...was, [p.projectId ?? '']: r }))
    }
    if (here.current) setTicked([])
  })
  return (
    <>
      <div className="row" style={{ gap: 8, margin: '8px 0' }}>
        <QuietConfirm label={`Adopt v${w.version} on ${chosen.length} project${chosen.length === 1 ? '' : 's'}`} className="subtle" yes="Open the Changes"
          disabled={asking || chosen.length === 0}
          ask={`Opens one adoption Change per project, taking ${w.pack} v${w.version}${(w.also ?? []).length > 0 ? ` with ${alsoWords(w)} where the project is on them` : ''} as assessed in this what-if${w.advanceWorkflows ? ", with each project's other workflows at the newest version" : ''}. ${inAdvance ? 'Where a project offers it, you accept it now, in the words below; any other Change waits for a person.' : 'Each lands through its own review.'}${backWords}`}
          prepare={inAdvance ? () => askInAdvance(chosen.map((p) => p.projectSlug), asked.current) : undefined}
          onYes={adopt} />
        <label className="small muted" title="Accept now each adoption that turns out to need no code and passes every check; the rest wait for a person">
          <input type="checkbox" checked={inAdvance} onChange={(e) => { asked.current = {}; setInAdvance(e.target.checked) }} disabled={asking} /> accept in advance if it needs no code
        </label>
        {asking && <span className="muted small">asking each project in turn…</span>}
      </div>
      <div style={{ overflowX: 'auto' }}>
      <table className="small" style={{ width: '100%' }}>
        <thead>
          <tr><th /><th>Project</th><th>Answer</th><th>Score</th><th>New failures</th><th>Could not assess</th><th>Rules</th><th>Commit</th><th>Adoption</th><th /></tr>
        </thead>
        <tbody>
          {w.projects.map((p) => {
            const pid = p.projectId ?? ''
            const rows = w.rows.filter((r) => r.projectId === p.projectId && r.result !== 'unchanged' && !RULE_MOVES.has(r.result))
            const back = p.moves.filter((m) => m.back).length
            const done = adopted[pid]
            const cannot = !p.candidateDigest ? (p.outcome === 'not-evaluated' || p.outcome === 'not-yet-judged' ? OUTCOME_WORDS[p.outcome] : 'no candidate was assessed') : done && 'changeId' in done ? 'its Change is open' : ''
            return (
              <Fragment key={pid}>
                <tr>
                  <td><input type="checkbox" aria-label={`adopt on ${p.projectSlug}`} title={cannot || undefined} disabled={!!cannot || asking}
                    checked={ticked.includes(pid)} onChange={(e) => setTicked(e.target.checked ? [...ticked, pid] : ticked.filter((x) => x !== pid))} /></td>
                  <td><Link to={`/conformance/projects/${p.projectSlug}`}>{p.projectSlug}</Link>{p.joins && <span className="chip" style={{ marginLeft: 6 }}>would join</span>}</td>
                  <td><Status word={p.statusLabel} tone={STATUS_LIGHT[p.statusLabel]} help={OUTCOME_WORDS[p.outcome]} /></td>
                  <td><ScoreLine s={p.score} />{p.fixed > 0 && <div className="muted">{p.fixed} fixed</div>}</td>
                  <td>{p.newFailures.map((c, i) => <div key={i}><Check c={c} />{c.exceptedToday && <span className="muted"> (excepted today)</span>}</div>)}</td>
                  <td>
                    {p.notAssessedBy.map((g) => <div key={g.reason} title={`${g.words}. To assess: ${g.remedy}`}>{g.count} {REASON_SHORT[g.reason] ?? g.words}</div>)}
                    {p.notAssessed.filter((c) => !c.block).map((c, i) => <div key={`whole${i}`}>the project: <span className="muted">{c.why}</span></div>)}
                  </td>
                  <td>
                    {back > 0 && <span className="chip moves-back">moves {back} back</span>}
                    {p.moves.length - back > 0 && <div className="muted">{p.moves.length - back} forward</div>}
                  </td>
                  <td>{p.sha ? <code title={`${p.sha} · adoption revision ${p.adoptionRevision}`}>{p.sha.slice(0, 10)}</code> : ''}</td>
                  <td><AdoptedCell a={done} /></td>
                  <td>{(rows.length > 0 || p.moves.length > 0) && <button className="subtle" onClick={() => setOpen(open === pid ? null : pid)}>{open === pid ? 'hide' : 'details'}</button>}</td>
                </tr>
                {open === pid && (
                  <tr><td colSpan={10}><Moves moves={p.moves} />{rows.map((r, i) => <Row key={i} r={r} />)}</td></tr>
                )}
              </Fragment>
            )
          })}
        </tbody>
      </table>
      </div>
    </>
  )
}

// alsoWords names the other packs a what-if moved with its own.
function alsoWords(w: WhatIf): string {
  return (w.also ?? []).map((a) => `${a.pack} v${a.version}`).join(', ')
}

// askInAdvance reads each project's question for a yes in advance into
// asked and says it: the sentences once for the projects that share them,
// and why each other project offers none.
async function askInAdvance(slugs: string[], asked: Record<string, InAdvanceQuestion>) {
  const groups = new Map<string, string[]>()
  const none: string[] = []
  for (const slug of slugs) {
    const q = await get<InAdvanceQuestion>(`/projects/${encodeURIComponent(slug)}/adoption/in-advance`)
    asked[slug] = q
    if (q.notOffered) { none.push(`${slug}: ${q.notOffered}`); continue }
    const words = [q.acceptance, ...q.landing].join('\n')
    groups.set(words, [...(groups.get(words) ?? []), slug])
  }
  return <>
    {[...groups].map(([words, who]) => <div key={words} data-testid="in-advance-words">
      <strong>{who.join(', ')}</strong>: you say yes now, meaning:
      {words.split('\n').map((line) => <div key={line}>“{line}”</div>)}
    </div>)}
    {none.length > 0 && <div>No yes in advance on {none.join('; ')}.</div>}
  </>
}

function AdoptedCell({ a }: { a?: Adopted }) {
  if (!a) return null
  if ('changeId' in a) {
    const link = <Link to={`/changes/${a.changeId}`}>{a.changeNumber ? `change ${a.slug}#${a.changeNumber}` : 'its Change'}</Link>
    // What came of starting its working run: working, a run that did not
    // get going (open it), or none started (start one from the change).
    if (a.notStarted) return <>{link} <span className="warn">not started: {a.notStarted}; start a workflow from the change</span></>
    if (a.runError) return <>{link} <span className="warn">its {a.started} run did not get going: {a.runError}</span></>
    return <>{link} <span className="muted">{a.started} working</span></>
  }
  if ('refused' in a) return <span className="error">refused: {a.refused}</span>
  return <span className="chip pending" title={a.unknown}>not known whether it opened; look at the project's Changes</span>
}

function Check({ c }: { c: WhatIfCheck }) {
  return c.block ? <code>{c.block} · {c.check}</code> : <span>the project</span>
}

const RESULT_WORDS: Record<string, string> = {
  'would-fail': 'would fail',
  'would-pass': 'would pass',
  'not-assessed': 'not assessed',
  'not-applicable': 'not applicable here',
  'no-longer-required': 'no longer required',
  unchanged: 'unchanged',
}

// Row is one check of one project: both sides, why it was not judged, what
// past fixes cost, and the brief a fix would start from.
function Row({ r }: { r: WhatIfRow }) {
  const what = r.block ? <code>{r.block} · {r.check}</code> : <span>the whole project</span>
  const sides = r.block ? `adopted: ${r.current || 'not required'} → this version: ${r.proposed || 'not required'}` : ''
  return (
    <div className="card" style={{ padding: '6px 10px', marginTop: 6 }}>
      <div className="row" style={{ flexWrap: 'wrap', gap: 6 }}>
        {what}
        <Status word={r.statusLabel} tone={STATUS_LIGHT[r.statusLabel]} help={RESULT_WORDS[r.result] ?? r.result} />
        <span className="muted">{sides}</span>
      </div>
      {r.detail && <div className="muted" style={{ marginTop: 2 }}>{r.detail}</div>}
      {r.result === 'would-fail' && (
        <div className="muted" style={{ marginTop: 2 }}>
          {r.history
            ? `Past fixes of this check: ${r.history.attempts}, median ${r.history.medianMinutes.toFixed(0)} min and $${r.history.medianCost.toFixed(2)}, ${Math.round(r.history.succeededShare * 100)}% landed.`
            : 'Past fixes of this check: no history.'}
        </div>
      )}
      {r.brief && <details style={{ marginTop: 4 }}><summary>The brief a fix would start from</summary><pre style={{ whiteSpace: 'pre-wrap', fontSize: 'var(--fs-xs)' }}>{r.brief}</pre></details>}
    </div>
  )
}
