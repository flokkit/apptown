import { Fragment, useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { currentUser, HttpError, post } from '../api'
import { QuietConfirm } from '../QuietConfirm'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { relTime } from '../format'
import type { AdoptionUpliftOpened, AuthzHoldings, PackHistory, WhatIf, WhatIfCheck, WhatIfProject, WhatIfRow } from '../wire.gen'

// One what-if on a pack version (design/subsystems/conformance.md):
// one row per project saying what taking the version would change, worst
// first, then the decision through the existing doors: recommend the
// version, or try it in an eval. Unknown is never shown as clear.

export const OUTCOME_WORDS: Record<string, string> = {
  'new-failures': 'new failures',
  'could-not-assess': 'could not assess',
  'no-new-failures': 'no new failures',
  'not-evaluated': 'not evaluated',
  'not-yet-judged': 'not yet judged',
}
export const OUTCOME_TONE: Record<string, string> = { 'new-failures': 'fail', 'could-not-assess': 'pending', 'no-new-failures': 'pass', 'not-evaluated': 'pending', 'not-yet-judged': '' }
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
      <div className="muted small"><Link to="/packs">Packs</Link> › <Link to={`/packs/${w.pack}`}>{w.pack}</Link></div>
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
      </div>
      <Decide w={w} />
      <Summary projects={w.projects} />
      <Table w={w} />
    </div>
  )
}

function State({ w }: { w: WhatIf }) {
  switch (w.state) {
    case 'queued': return <span className="chip pending">queued</span>
    case 'running': return <span className="chip pending">running · {w.projectsDone} of {w.projectsTotal}</span>
    case 'done': return <span className="chip">done</span>
    case 'interrupted': return <span className="chip fail" title={w.detail}>interrupted</span>
    default: return <span className="chip fail" title={w.detail}>stopped: {w.detail}</span>
  }
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

function Summary({ projects }: { projects: WhatIfProject[] }) {
  const counts: Record<string, number> = {}
  for (const p of projects) counts[p.outcome] = (counts[p.outcome] ?? 0) + 1
  return (
    <div className="row" style={{ flexWrap: 'wrap', gap: 6, marginBottom: 8 }}>
      <strong>{projects.length} project{projects.length === 1 ? '' : 's'}</strong>
      {OUTCOME_ORDER.filter((o) => counts[o]).map((o) => <span key={o} className={`chip ${OUTCOME_TONE[o]}`}>{counts[o]} {OUTCOME_WORDS[o]}</span>)}
    </div>
  )
}

// Adopted is what asking one project to take the version came to: the
// Change it opened, AppTown's refusal in its words, or not known (the
// answer never arrived, or AppTown failed while answering).
type Adopted = { changeId: string | null; changeNumber: number; slug: string } | { refused: string } | { unknown: string }

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
  const { run, busy } = useAction(undefined, { affects: ['/changes', '/projects'] })
  if (w.projects.length === 0) return <div className="muted small">No projects selected.</div>
  const chosen = w.projects.filter((p) => p.projectId && ticked.includes(p.projectId))
  const asking = busy
  const adopt = () => run(async () => {
    const who = currentUser()
    for (const p of chosen) {
      if (!here.current || currentUser() !== who) return
      let r: Adopted
      try {
        const o = await post<AdoptionUpliftOpened>(`/projects/${encodeURIComponent(p.projectSlug)}/adoption/adopt`, { fromWhatIf: w.id, viaChange: true })
        r = { changeId: o.changeId, changeNumber: o.changeNumber, slug: p.projectSlug }
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
          ask={`Opens one adoption Change per project, taking ${w.pack} v${w.version} as assessed in this what-if. Each lands through its own review.`}
          onYes={adopt} />
        {asking && <span className="muted small">asking each project in turn…</span>}
      </div>
      <table className="small" style={{ width: '100%' }}>
        <thead>
          <tr><th /><th>Project</th><th>Answer</th><th>New failures</th><th>Could not assess</th><th>Fixed</th><th>Commit</th><th>Adoption</th><th /></tr>
        </thead>
        <tbody>
          {w.projects.map((p) => {
            const pid = p.projectId ?? ''
            const rows = w.rows.filter((r) => r.projectId === p.projectId && r.result !== 'unchanged')
            const done = adopted[pid]
            const cannot = !p.candidateDigest ? (p.outcome === 'not-evaluated' || p.outcome === 'not-yet-judged' ? OUTCOME_WORDS[p.outcome] : 'no candidate was assessed') : done && 'changeId' in done ? 'its Change is open' : ''
            return (
              <Fragment key={pid}>
                <tr>
                  <td><input type="checkbox" aria-label={`adopt on ${p.projectSlug}`} title={cannot || undefined} disabled={!!cannot || asking}
                    checked={ticked.includes(pid)} onChange={(e) => setTicked(e.target.checked ? [...ticked, pid] : ticked.filter((x) => x !== pid))} /></td>
                  <td><Link to={`/conformance/projects/${p.projectSlug}`}>{p.projectSlug}</Link>{p.joins && <span className="chip" style={{ marginLeft: 6 }}>would join</span>}</td>
                  <td><span className={`chip ${OUTCOME_TONE[p.outcome]}`}>{OUTCOME_WORDS[p.outcome] ?? p.outcome}</span></td>
                  <td>{p.newFailures.map((c, i) => <div key={i}><Check c={c} />{c.exceptedToday && <span className="muted"> (excepted today)</span>}</div>)}</td>
                  <td>{p.notAssessed.map((c, i) => <div key={i}><Check c={c} />: <span className="muted">{c.why}</span></div>)}</td>
                  <td>{p.fixed || ''}</td>
                  <td>{p.sha ? <code title={`${p.sha} · adoption revision ${p.adoptionRevision}`}>{p.sha.slice(0, 10)}</code> : ''}</td>
                  <td><AdoptedCell a={done} /></td>
                  <td>{rows.length > 0 && <button className="subtle" onClick={() => setOpen(open === pid ? null : pid)}>{open === pid ? 'hide' : 'checks'}</button>}</td>
                </tr>
                {open === pid && (
                  <tr><td colSpan={9}>{rows.map((r, i) => <Row key={i} r={r} />)}</td></tr>
                )}
              </Fragment>
            )
          })}
        </tbody>
      </table>
    </>
  )
}

function AdoptedCell({ a }: { a?: Adopted }) {
  if (!a) return null
  if ('changeId' in a) return <Link to={`/changes/${a.changeId}`}>{a.changeNumber ? `change ${a.slug}#${a.changeNumber}` : 'its Change'}</Link>
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
  'no-longer-required': 'no longer required',
  unchanged: 'unchanged',
}
const RESULT_TONE: Record<string, string> = { 'would-fail': 'fail', 'would-pass': 'pass', 'not-assessed': 'pending' }

// Row is one check of one project: both sides, why it was not judged, what
// past fixes cost, and the brief a fix would start from.
function Row({ r }: { r: WhatIfRow }) {
  const what = r.block ? <code>{r.block} · {r.check}</code> : <span>the whole project</span>
  const sides = r.block ? `adopted: ${r.current || 'not required'} → this version: ${r.proposed || 'not required'}` : ''
  return (
    <div className="card" style={{ padding: '6px 10px', marginTop: 6 }}>
      <div className="row" style={{ flexWrap: 'wrap', gap: 6 }}>
        {what}
        <span className={`chip ${RESULT_TONE[r.result] ?? ''}`}>{RESULT_WORDS[r.result] ?? r.result}</span>
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
