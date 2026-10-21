import { useState } from 'react'
import { Link } from 'react-router-dom'
import { post } from '../api'
import { firstErr, useAction, useGet } from '../data'
import { relTime } from '../format'
import type { FleetView, LatestWhatIf, ProjectRow, WhatIf, WhatIfStarted } from '../wire.gen'
import { OUTCOME_ORDER, OUTCOME_TONE, OUTCOME_WORDS } from './WhatIfPage'

// What-if on one pack version (design/subsystems/conformance.md): by
// default on every eligible project (on an earlier version of the pack), or
// on chosen projects, on the pack or not. Model rubrics cost money, so they
// run only when included, under a spend limit. The newest one shows as its
// summary; its page has the table and the decision.

export function WhatIfPanel({ pack, version }: { pack: string; version: number }) {
  const q = useGet<LatestWhatIf>(`/conformance/what-if?pack=${encodeURIComponent(pack)}&version=${version}`, { poll: 3000 })
  const [choosing, setChoosing] = useState(false)
  const fleetQ = useGet<FleetView>(choosing ? '/wow' : null)
  const projectsQ = useGet<ProjectRow[]>(choosing ? '/projects' : null)
  const { run, busy, err: actErr } = useAction(q.reload)
  const [added, setAdded] = useState<string[]>([])
  const [left, setLeft] = useState<string[]>([])
  const [rubrics, setRubrics] = useState(false)
  const [advance, setAdvance] = useState(false)
  const [limit, setLimit] = useState('0.50')
  const err = firstErr(actErr, q.err, fleetQ.err)
  const eligible = q.data?.eligible ?? null
  // Membership is known only once read: while a read is pending or failed
  // the picker says so, never that there is nothing.
  const fleet = fleetQ.data
  const onPack = fleet ? [...new Set((fleet.packs.find((p) => p.pack === pack)?.versions ?? []).flatMap((v) => v.projects))].sort() : null
  const others = onPack && projectsQ.data ? projectsQ.data.filter((p) => p.defaultBranch && !onPack.includes(p.slug) && !added.includes(p.slug)).map((p) => p.slug).sort() : []
  const chosen = choosing ? (onPack ? [...onPack.filter((s) => !left.includes(s)), ...added] : []) : (eligible ?? [])
  const limitMicros = Math.round(parseFloat(limit) * 1e6)
  const limitOK = !rubrics || (Number.isFinite(limitMicros) && limitMicros > 0)
  const w = q.data?.whatIf
  const start = () => run(() => post<WhatIfStarted>('/conformance/what-if', { pack, version, ...(choosing ? { projects: chosen } : {}), rubrics, spendLimitMicros: rubrics ? limitMicros : 0, advanceWorkflows: advance }))

  return (
    <div className="small" style={{ marginTop: 8, borderTop: '1px solid var(--border)', paddingTop: 8 }}>
      <div className="row" style={{ flexWrap: 'wrap', gap: 6 }}>
        <strong>What-if</strong>
        <span className="muted">hypothetical: never a project's standing</span>
      </div>
      <div className="card" style={{ padding: '6px 10px', marginTop: 6 }}>
        {!choosing && (
          <div className="row" style={{ flexWrap: 'wrap', gap: 6 }}>
            <span>Eligible (on an earlier version):</span>
            {eligible === null
              ? <span className="muted">{q.err ? 'could not be read' : 'reading…'}</span>
              : eligible.length === 0 ? <span className="muted">none</span>
                : eligible.map((s) => <code key={s}>{s}</code>)}
            <button className="subtle" onClick={() => setChoosing(true)}>choose projects</button>
          </div>
        )}
        {choosing && (onPack === null
          ? <div className="muted">{fleetQ.err ? 'Who is on this pack could not be read.' : 'reading who is on this pack…'}</div>
          : (
            <>
              <div>Projects <button className="subtle" onClick={() => setChoosing(false)}>eligible only</button></div>
              {onPack.length === 0 && added.length === 0 && <div className="muted">No project you can see is on this pack; add one to ask what joining it would say.</div>}
              <div className="row" style={{ flexWrap: 'wrap', gap: 8, marginTop: 2 }}>
                {onPack.map((s) => (
                  <label key={s}><input type="checkbox" checked={!left.includes(s)} onChange={(e) => setLeft(e.target.checked ? left.filter((x) => x !== s) : [...left, s])} /> {s}</label>
                ))}
                {added.map((s) => (
                  <label key={s}><input type="checkbox" checked onChange={() => setAdded(added.filter((x) => x !== s))} /> {s} <span className="muted">(would join)</span></label>
                ))}
                {others.length > 0 && (
                  <select value="" aria-label="add a project not on this pack" onChange={(e) => e.target.value && setAdded([...added, e.target.value])}>
                    <option value="">add a project not on this pack…</option>
                    {others.map((s) => <option key={s} value={s}>{s}</option>)}
                  </select>
                )}
                {projectsQ.err && <span className="muted">Other projects could not be read.</span>}
              </div>
            </>
          ))}
        <div style={{ marginTop: 6 }}>
          <label><input type="checkbox" checked={advance} onChange={(e) => setAdvance(e.target.checked)} /> Also move each project's other workflows to the newest version</label>
          <div className="muted">Workflows this pack does not pin otherwise keep the version each project is on.</div>
        </div>
        <div style={{ marginTop: 6 }}>
          <label><input type="checkbox" checked={rubrics} onChange={(e) => setRubrics(e.target.checked)} /> Include model rubrics (they cost money)</label>
          {rubrics && (
            <div className="row" style={{ gap: 6, flexWrap: 'wrap', marginTop: 2 }}>
              <label>Spend limit $<input style={{ width: 70 }} value={limit} onChange={(e) => setLimit(e.target.value)} aria-label="spend limit in dollars" /></label>
              <span className="muted">{chosen.length} project{chosen.length === 1 ? '' : 's'}, up to {2 * chosen.length} calls; one project's calls may pass the limit.</span>
            </div>
          )}
        </div>
        <button className="subtle" style={{ marginTop: 6 }} disabled={busy || w?.state === 'queued' || w?.state === 'running' || chosen.length === 0 || !limitOK} onClick={start}>
          Run on {chosen.length}{choosing ? '' : ' eligible'} project{chosen.length === 1 ? '' : 's'}
        </button>
      </div>
      {err && <div className="error">{err}</div>}
      {q.data && !w && <div className="muted" style={{ marginTop: 4 }}>None run on this version.</div>}
      {!q.data && !q.err && <div className="muted" style={{ marginTop: 4 }}>reading what-ifs…</div>}
      {w && <Latest w={w} />}
    </div>
  )
}

// Latest is the newest what-if's state and its count of projects by
// answer; its page has the table.
function Latest({ w }: { w: WhatIf }) {
  const counts: Record<string, number> = {}
  for (const p of w.projects) counts[p.outcome] = (counts[p.outcome] ?? 0) + 1
  return (
    <div className="row" style={{ flexWrap: 'wrap', gap: 6, marginTop: 6 }}>
      <Link to={`/conformance/what-ifs/${w.id}`}>Open the what-if</Link>
      {w.state === 'queued' && <span className="chip pending">queued</span>}
      {w.state === 'running' && <span className="chip pending">running · {w.projectsDone} of {w.projectsTotal} projects</span>}
      {w.state === 'error' && <span className="chip fail" title={w.detail}>stopped</span>}
      {w.state === 'interrupted' && <span className="chip fail">interrupted</span>}
      <span className="muted">{w.requestedBy} · {relTime(w.createdAt)}</span>
      {OUTCOME_ORDER.filter((o) => counts[o]).map((o) =>
        <span key={o} className={`chip ${OUTCOME_TONE[o]}`}>{counts[o]} {OUTCOME_WORDS[o]}</span>)}
      {w.movesBackProjects > 0 && <span className="chip moves-back">{w.movesBackProjects} move rules back</span>}
      <Link to="/conformance/what-ifs" className="muted">all what-ifs</Link>
    </div>
  )
}
