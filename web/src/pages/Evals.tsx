import { PRODUCT } from '../brand'
import { useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { del, post, type Change } from '../api'
import { useAction, useGet, useGetText } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { relTime } from '../format'
import type { Arm, CheckRow, Complaint, Eval, EvalList, EvalScorecard, EvalStartForm, ProjectRow, Tally } from '../wire.gen'

// Evals: is a model profile, or a pack version, better
// or worse than what projects run now? Finished Changes are re-run in
// throwaway copies, as their project runs now and with the one thing
// varied, and a person compares side by side: the work, each check's
// findings against the Change's base, and the cost. The scorecard decides
// nothing and is never evidence.

const dollars = (micros: number, incomplete = 0) => `${incomplete > 0 ? 'at least ' : ''}$${(micros / 1e6).toFixed(2)}`

const ARM_WORDS: Record<string, string> = {
  queued: 'waiting to start', copying: 'making its copy', copied: 'copy made, waiting for the eval to continue', starting: 'starting its lane', working: 'working', landing: 'ready to land, landing', scored: 'scored', failed: 'failed', 'not-run': 'not run',
}
const ARM_TONE: Record<string, string> = { copied: 'needs_human', working: 'pending', landing: 'pending', scored: 'pass', failed: 'fail' }
const EVAL_TONE: Record<string, string> = { running: 'pending', held: 'needs_human', finished: 'pass', stopped: '' }

// What an eval read refused to the caller says: who may, not an error.
const EVALS_DENIED = 'Evals are read by standards administrators and auditors, and started by people who manage packs; you are neither.'

export function EvalsPage() {
  const q = useGet<EvalList>('/evals', { poll: 10000 })
  return (
    <div>
      <div className="row" style={{ marginBottom: 12 }}>
        <h2 style={{ margin: 0 }}>Evals</h2>
        <span style={{ flex: 1 }} />
        <Link className="button" to="/evals/new">Start an eval</Link>
      </div>
      <p className="muted small" style={{ marginTop: 0 }}>Re-run finished Changes as their projects run now and with another model profile or pack version, then compare. A person reads the result; it approves nothing.</p>
      <ReadStatus q={q} what="the evals" denied={EVALS_DENIED} />
      <StaleNote q={q} />
      {q.data && q.data.evals.length === 0 && <div className="muted">No evals yet.</div>}
      {q.data?.evals.map((e) => <EvalRow key={e.id} e={e} />)}
    </div>
  )
}

function EvalRow({ e }: { e: Eval }) {
  return (
    <div className="card" style={{ marginBottom: 8 }}>
      <div className="row" style={{ flexWrap: 'wrap', gap: 8 }}>
        <Link to={`/evals/${e.id}`}><strong>Eval {e.number}</strong></Link>
        <span>{e.subject} against what projects run now</span>
        <span className={`chip ${EVAL_TONE[e.state] ?? ''}`}>{e.state}</span>
        <span style={{ flex: 1 }} />
        <span className="muted small">started by {e.startedBy} {relTime(e.startedAt)}</span>
      </div>
    </div>
  )
}

export function EvalStartPage() {
  const nav = useNavigate()
  const formQ = useGet<EvalStartForm>('/evals/start', { kind: 'config' })
  const projectsQ = useGet<ProjectRow[]>('/projects')
  const [slug, setSlug] = useState('')
  const changesQ = useGet<Change[]>(slug ? `/projects/${encodeURIComponent(slug)}/changes` : null)
  const [picked, setPicked] = useState<string[]>([])
  const [varies, setVaries] = useState<'model' | 'pack'>('pack')
  const [profile, setProfile] = useState('')
  // A what-if's "Try in an eval" names the pack version it was on.
  const [params] = useSearchParams()
  const [pack, setPack] = useState(params.get('pack') ?? '')
  const [version, setVersion] = useState(Number(params.get('version')) || 0)
  const [budget, setBudget] = useState('5')
  const [confirming, setConfirming] = useState(false)
  const { run, busy, err: actErr } = useAction(undefined, { affects: ['/evals'] })
  const form = formQ.data
  const max = form?.maxChanges ?? 5
  const landed = (changesQ.data ?? []).filter((c) => c.state === 'shipped' && c.baseSha)
  const budgetDollars = Number(budget)
  const subject = varies === 'model' ? profile : pack && version ? `${pack}@${version}` : ''
  const ready = picked.length > 0 && subject !== '' && budgetDollars > 0
  const choice = form?.packs.find((p) => p.name === pack)
  // The picked Changes of the project shown, by the numbers people read.
  const pickedHere = landed.filter((c) => c.id && picked.includes(c.id)).map((c) => `#${c.number}`)
  const toggle = (id: string) => setPicked((p) => p.includes(id) ? p.filter((x) => x !== id) : p.length < max ? [...p, id] : p)
  const start = () => run(async () => {
    const what = varies === 'model' ? { profile } : { pack, version }
    const e = await post<Eval>('/evals', { changes: picked, ...what, budgetDollars, shown: form!.meaning })
    nav(`/evals/${e.id}`)
  })

  return (
    <div style={{ maxWidth: 760 }}>
      <h2>Start an eval</h2>
      <ReadStatus q={formQ} what="the start form" denied={EVALS_DENIED} />
      <StaleNote q={[formQ, projectsQ, changesQ]} />
      {form && (
        <>
          <section className="card" style={{ marginBottom: 12 }}>
            <h3 style={{ marginTop: 0 }}>1. What varies</h3>
            <div className="row small" style={{ gap: 12, flexWrap: 'wrap' }}>
              <label><input type="radio" name="varies" checked={varies === 'pack'} onChange={() => { setVaries('pack'); setConfirming(false) }} /> a pack version</label>
              <label><input type="radio" name="varies" checked={varies === 'model'} onChange={() => { setVaries('model'); setConfirming(false) }} /> a model profile</label>
            </div>
            {varies === 'model' && (
              <label className="small" style={{ display: 'block', marginTop: 6 }}>Compare what projects run now with
                <select value={profile} onChange={(e) => { setProfile(e.target.value); setConfirming(false) }} style={{ marginLeft: 6 }}>
                  <option value="">a model profile…</option>
                  {form.profiles.map((p) => <option key={p} value={p}>{p}</option>)}
                </select>
              </label>
            )}
            {varies === 'pack' && (
              <div className="small" style={{ marginTop: 6 }}>
                <label>Compare what projects run now with
                  <select value={pack} onChange={(e) => { const c = form.packs.find((p) => p.name === e.target.value); setPack(e.target.value); setVersion(c?.latest ?? 0); setConfirming(false) }} style={{ marginLeft: 6 }}>
                    <option value="">a pack…</option>
                    {form.packs.map((p) => <option key={p.name} value={p.name}>{p.name}</option>)}
                  </select>
                </label>
                {choice && (
                  <label style={{ marginLeft: 6 }}>version
                    <select value={version} onChange={(e) => { setVersion(Number(e.target.value)); setConfirming(false) }} style={{ marginLeft: 6 }}>
                      {Array.from({ length: choice.latest }, (_, i) => choice.latest - i).map((v) => <option key={v} value={v}>v{v}{v === choice.recommended ? ' (recommended now)' : ''}</option>)}
                    </select>
                  </label>
                )}
                <div className="muted" style={{ marginTop: 6 }}>The variant adopts exactly what this version would be for each project (the same candidate a what-if describes; a project not on the pack joins it), with the model held. Its checks and today's are both judged on each arm's work, finding by finding.</div>
              </div>
            )}
          </section>
          <section className="card" style={{ marginBottom: 12 }}>
            <h3 style={{ marginTop: 0 }}>2. The work to re-run (up to {max} landed Changes)</h3>
            <ReadStatus q={projectsQ} what="your projects" />
            {projectsQ.data && (
              <select value={slug} onChange={(e) => { setSlug(e.target.value); setConfirming(false) }}>
                <option value="">a project…</option>
                {projectsQ.data.map((p) => <option key={p.slug} value={p.slug}>{p.name}</option>)}
              </select>
            )}
            {slug && <ReadStatus q={changesQ} what="its Changes" />}
            {slug && changesQ.data && landed.length === 0 && <div className="muted small" style={{ marginTop: 6 }}>No landed Change of this project can be re-run (it needs a base commit).</div>}
            {landed.map((c) => c.id && (
              <label key={c.id} className="row small" style={{ gap: 6, marginTop: 4 }}>
                <input type="checkbox" checked={picked.includes(c.id)} disabled={!picked.includes(c.id) && picked.length >= max} onChange={() => { toggle(c.id!); setConfirming(false) }} />
                <span>#{c.number} {c.title}</span>
              </label>
            ))}
            {picked.length > 0 && <div className="muted small" style={{ marginTop: 6 }}>{picked.length} picked{pickedHere.length ? `: ${pickedHere.join(', ')}` : ''}</div>}
          </section>
          <section className="card" style={{ marginBottom: 12 }}>
            <h3 style={{ marginTop: 0 }}>3. Budget</h3>
            <label className="small">Budget $ <input type="number" min="0.01" step="0.5" value={budget} onChange={(e) => { setBudget(e.target.value); setConfirming(false) }} style={{ width: 90 }} /></label>
            <div className="muted small" style={{ marginTop: 6 }}>All model spend of the eval's copies counts, checks and summaries included. It is checked before each arm and each step starts; a step already running when it runs out may finish past it. Spend that cannot be counted holds the next arm until you continue it.</div>
          </section>
          {!confirming && <button disabled={!ready} onClick={() => setConfirming(true)}>Review and start…</button>}
          {confirming && (
            <section className="card" style={{ borderLeft: '3px solid var(--warn)' }}>
              <p style={{ marginTop: 0 }}>Each of the {picked.length} Change{picked.length === 1 ? '' : 's'} is re-run twice in throwaway copies: as its project runs now, and with <strong>{varies === 'model' ? `model profile ${profile}` : `pack version ${subject}`}</strong>. Copies land and deploy with no one approving them, on the eval domain only, and are deleted with the eval. Budget ${budgetDollars.toFixed(2)}, checked before each arm and step.</p>
              <p><strong>Starting means: “{form.meaning}”</strong></p>
              {actErr && <div className="error">{actErr}</div>}
              <div className="row" style={{ gap: 8 }}>
                <button disabled={busy} onClick={start}>Start the eval</button>
                <button className="subtle" onClick={() => setConfirming(false)}>Not yet</button>
              </div>
            </section>
          )}
        </>
      )}
    </div>
  )
}

export function EvalScorecardPage() {
  const { id } = useParams()
  const nav = useNavigate()
  const q = useGet<EvalScorecard>(`/evals/${id}`, { poll: 10000 })
  const { run, busy, err: actErr } = useAction(q.reload, { affects: ['/evals'] })
  const e = q.data
  const variantWords = e ? (e.subjectKind === 'pack' ? `with ${e.subject}` : `on ${e.subject}`) : ''
  return (
    <div>
      <div className="row" style={{ marginBottom: 8 }}><Link to="/evals" className="small">← Evals</Link></div>
      <ReadStatus q={q} what="this eval" denied={EVALS_DENIED} />
      <StaleNote q={q} />
      {actErr && <div className="error small">{actErr}</div>}
      {e && (
        <>
          <div className="row" style={{ flexWrap: 'wrap', gap: 8 }}>
            <h2 style={{ margin: 0 }}>Eval {e.number}: {e.subject} against what projects run now</h2>
            <span className={`chip ${EVAL_TONE[e.state] ?? ''}`}>{e.state}</span>
          </div>
          <div className="muted small" style={{ marginTop: 4 }}>
            Started by {e.startedBy} {relTime(e.startedAt)}, meaning “{e.meaning}”
            {e.subjectDigest && <> · tested exactly <code title="digest of the version's resolved spec and pins at start">{e.subjectDigest}</code></>}
          </div>
          <div className="row" style={{ gap: 16, flexWrap: 'wrap', margin: '10px 0' }}>
            <span>Spent <strong>{dollars(e.costMicros, e.incomplete)}</strong> of ${(e.budgetMicros / 1e6).toFixed(2)}</span>
            {e.incomplete > 0 && <span className="muted small">{e.incomplete} model {e.incomplete === 1 ? 'call' : 'calls'} spent an unknown amount</span>}
          </div>
          {e.reason && e.state !== 'held' && <div className="small" style={{ marginBottom: 8 }}>{e.reason}</div>}
          {e.state === 'held' && (
            <div className="card" style={{ borderLeft: '3px solid var(--warn)', marginBottom: 12 }}>
              <div>Held: the next arm waits. Continuing says you answer for spend {PRODUCT} could not count; the cost stays “at least”.</div>
              <button disabled={busy} style={{ marginTop: 6 }} onClick={() => run(() => post(`/evals/${e.id}/continue`))}>Continue</button>
            </div>
          )}
          <Tallies e={e} variant={variantWords} />
          <p className="muted small">Approvals inside the copies were the eval's, not a person's acceptance: acceptance is not measured. Read the diffs to judge the work.</p>
          {groupByChange(e.arms).map(([change, arms]) => (
            <ChangeSection key={change} e={e} change={change} arms={arms} variant={variantWords} />
          ))}
          <ComplaintsByAbout complaints={e.complaints} arms={e.arms} />
          <div style={{ marginTop: 16 }}>
            <button className="subtle" disabled={busy} onClick={() => {
              if (window.confirm(`Delete eval ${e.number}? Its arms stop and its copies are deleted; what it cost and what was said in it are kept.`)) {
                run(async () => { await del(`/evals/${e.id}`); nav('/evals') })
              }
            }}>Delete this eval</button>
            <span className="muted small" style={{ marginLeft: 8 }}>its starter, while managing packs, or an administrator</span>
          </div>
        </>
      )}
    </div>
  )
}

// Tallies is the variant against the baseline, measure by measure, over
// the Changes both arms finished comparably: observed in this run, with N
// stated and no claim of significance.
function Tallies({ e, variant }: { e: EvalScorecard; variant: string }) {
  if (e.comparable === 0) {
    return <div className="muted small" style={{ marginBottom: 8 }}>No Change has both arms finished comparably yet{e.notComparable > 0 ? `; ${e.notComparable} not comparable` : ''}.</div>
  }
  return (
    <section className="card" style={{ marginBottom: 10 }}>
      <h3 style={{ marginTop: 0 }}>{variant} against as it runs now</h3>
      <div className="muted small">Over {e.comparable} Change{e.comparable === 1 ? '' : 's'}{e.notComparable > 0 ? ` (${e.notComparable} not comparable, left out)` : ''}. Observed in this run: a small sample, and a flaky check can move it.</div>
      <table className="small" style={{ marginTop: 6 }}>
        <thead><tr><th style={{ textAlign: 'left' }}>Measure</th><th>better</th><th>same</th><th>worse</th><th>unknown</th></tr></thead>
        <tbody>{e.tallies.map((t: Tally) => (
          <tr key={t.measure}><td>{t.measure}</td><td style={{ textAlign: 'center' }}>{t.better}</td><td style={{ textAlign: 'center' }}>{t.same}</td><td style={{ textAlign: 'center' }}>{t.worse}</td><td style={{ textAlign: 'center' }} className="muted">{t.unknown}</td></tr>
        ))}</tbody>
      </table>
    </section>
  )
}

function groupByChange(arms: Arm[]): [string, Arm[]][] {
  const m = new Map<string, Arm[]>()
  for (const a of arms) if (a.sourceChangeId) m.set(a.sourceChangeId, [...(m.get(a.sourceChangeId) ?? []), a])
  return [...m.entries()]
}

// ChangeSection is one Change side by side: the original accepted work for
// reference, then each arm, then its checks compared with the base.
function ChangeSection({ e, change, arms, variant }: { e: EvalScorecard; change: string; arms: Arm[]; variant: string }) {
  const baseline = arms.find((a) => a.arm === 'baseline')
  const varied = arms.find((a) => a.arm === 'variant')
  return (
    <section className="card" style={{ marginBottom: 10 }}>
      <h3 style={{ marginTop: 0 }}>{arms[0]?.sourceChangeNumber ? <>Change <Link to={`/changes/${change}`}>{arms[0].sourceChangeNumber}</Link>, re-run</> : 'A Change, re-run'}</h3>
      <div className="eval-arms">
        <div className="eval-arm">
          <strong>The original</strong>
          <div className="muted small" style={{ marginTop: 4 }}>The accepted work, shown for reference; not re-run.</div>
          <div className="small" style={{ marginTop: 4 }}><Link to={`/changes/${change}`}>its Change and diff</Link></div>
        </div>
        {[baseline, varied].map((a) => a && (
          <ArmCard key={a.id} e={e} a={a} label={a.arm === 'baseline' ? 'As it runs now' : variant} cost={e.armCosts[a.id ?? '']} complaints={e.complaints.filter((c) => c.armId === a.id)} />
        ))}
      </div>
      {baseline && varied && <CheckTable baseline={baseline} variant={varied} variantWords={variant} />}
    </section>
  )
}

function ArmCard({ e, a, label, cost, complaints }: { e: EvalScorecard; a: Arm; label: string; cost?: { costMicros: number; tokens: number; incomplete: number }; complaints: Complaint[] }) {
  const s = a.score
  const started = a.state !== 'queued' && a.state !== 'not-run'
  const [showDiff, setShowDiff] = useState(false)
  const diff = useGetText(showDiff ? `/evals/${e.id}/arms/${a.id}/diff` : null)
  const counts = compareCounts(s.compared?.rows ?? [])
  return (
    <div className="eval-arm">
      <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
        <strong>{label}</strong>
        <span className="muted small">{a.profile}</span>
        <span className={`chip ${ARM_TONE[a.state] ?? ''}`}>{ARM_WORDS[a.state] ?? a.state}</span>
        {s.notComparable && <span className="chip">not comparable</span>}
      </div>
      {a.reason && <div className="small" style={{ marginTop: 4 }}>{a.reason}</div>}
      {started && s.unreadable && (
        <dl className="eval-facts small">
          <dt>Score</dt><dd className="muted">not known: {s.unreadable}</dd>
          <dt>Cost</dt><dd>{cost ? dollars(cost.costMicros, cost.incomplete) : '$0.00'}</dd>
          <dt>Complaints</dt><dd>{complaints.length}</dd>
        </dl>
      )}
      {started && !s.unreadable && (
        <dl className="eval-facts small">
          <dt>Ready to land</dt><dd>{s.readyToLand ? 'yes' : 'no'}</dd>
          <dt>Landed</dt><dd>{s.landed ? 'yes' : 'no'}</dd>
          <dt>Deploy</dt><dd>{s.deploy || '—'}</dd>
          <dt>Findings</dt><dd>{s.compared ? `${counts.new} new, ${counts.fixed} fixed${counts.notComparable ? `, ${counts.notComparable} check${counts.notComparable === 1 ? '' : 's'} not comparable` : ''}` : s.compareError ? <span className="muted">not compared: {s.compareError}</span> : '—'}</dd>
          <dt>Request met</dt><dd title="a model's reading, not acceptance">{s.requestMet || '—'}</dd>
          <dt>Reworks</dt><dd>{s.reworks}</dd>
          <dt>Time to ready</dt><dd>{s.workSeconds ? `${Math.round(s.workSeconds / 60)} min` : '—'}</dd>
          <dt>Cost</dt><dd>{cost ? dollars(cost.costMicros, cost.incomplete) : '$0.00'}</dd>
          <dt>Complaints</dt><dd>{complaints.length}</dd>
        </dl>
      )}
      {(s.checks ?? []).length > 0 && (
        <div className="row small" style={{ gap: 4, flexWrap: 'wrap', marginTop: 4 }}>
          <span className="muted">landing gates:</span>
          {(s.checks ?? []).map((c) => <span key={c.step} className={`chip ${c.status === 'pass' ? 'pass' : c.status === 'pending' ? 'pending' : 'fail'}`}>{c.step}: {c.status}</span>)}
        </div>
      )}
      {a.projectId ? (
        <div className="small" style={{ marginTop: 4 }}>
          <button className="subtle" onClick={() => setShowDiff(!showDiff)}>{showDiff ? 'Hide its diff' : 'Show its diff'}</button>
          {a.changeId ? <> · <Link to={`/changes/${a.changeId}`}>the copy's Change{a.changeNumber ? ` ${a.changeNumber}` : ''}</Link></> : null}
        </div>
      ) : null}
      {showDiff && (diff.loading ? <div className="muted small">Reading the diff…</div>
        : diff.err ? <div className="error small">The diff could not be read: {diff.err}</div>
          : <pre className="small" style={{ whiteSpace: 'pre-wrap', maxHeight: 360, overflow: 'auto' }}>{diff.data || '(no change from the base)'}</pre>)}
    </div>
  )
}

function compareCounts(rows: CheckRow[]) {
  let n = 0, f = 0, nc = 0
  for (const r of rows) { n += r.new?.length ?? 0; f += r.fixed?.length ?? 0; if (r.notComparable) nc++ }
  return { new: n, fixed: f, notComparable: nc }
}

const rowKey = (r: { block: string; version: number; check: string }) => `${r.block}@${r.version}|${r.check}`

// CheckTable is the checks both arms declare, each judged on the base and
// on both arms' work: changed rows first, the ones only one setup declares
// marked.
function CheckTable({ baseline, variant, variantWords }: { baseline: Arm; variant: Arm; variantWords: string }) {
  const b = new Map((baseline.score.compared?.rows ?? []).map((r) => [rowKey(r), r]))
  const v = new Map((variant.score.compared?.rows ?? []).map((r) => [rowKey(r), r]))
  if (b.size === 0 && v.size === 0) return null
  const keys = [...new Set([...b.keys(), ...v.keys()])]
  const declares = (a: Arm, block: string, version: number) => a.declares.some((d) => d.block === block && d.version === version)
  const changed = (k: string) => {
    const x = b.get(k), y = v.get(k)
    return (x?.new?.length ?? 0) + (y?.new?.length ?? 0) + (x?.fixed?.length ?? 0) + (y?.fixed?.length ?? 0) > 0 || x?.arm !== y?.arm
  }
  keys.sort((p, q) => Number(changed(q)) - Number(changed(p)) || p.localeCompare(q))
  return (
    <details open style={{ marginTop: 8 }}>
      <summary className="small">Checks, finding by finding, against the Change's base ({keys.length})</summary>
      <table className="small" style={{ marginTop: 6, width: '100%' }}>
        <thead><tr><th style={{ textAlign: 'left' }}>Check</th><th style={{ textAlign: 'left' }}>Base</th><th style={{ textAlign: 'left' }}>As it runs now</th><th style={{ textAlign: 'left' }}>{variantWords}</th></tr></thead>
        <tbody>{keys.map((k) => {
          const any = (b.get(k) ?? v.get(k))!
          const only = declares(baseline, any.block, any.version) === declares(variant, any.block, any.version) ? ''
            : declares(variant, any.block, any.version) ? 'only the varied setup declares this' : 'only today\'s setup declares this'
          return (
            <tr key={k}>
              <td><code>{any.block} · {any.check}</code> <span className="muted">v{any.version}</span>{only && <div className="muted">{only}</div>}</td>
              <td>{any.base}</td>
              <td><Cell r={b.get(k)} /></td>
              <td><Cell r={v.get(k)} /></td>
            </tr>
          )
        })}</tbody>
      </table>
    </details>
  )
}

function Cell({ r }: { r?: CheckRow }) {
  if (!r) return <span className="muted">not compared</span>
  if (r.notComparable) return <span className="muted">{r.arm}; not comparable: {r.notComparable}</span>
  const parts = [r.arm]
  if (r.new?.length) parts.push(`${r.new.length} new: ${r.new.slice(0, 3).join(', ')}${r.new.length > 3 ? '…' : ''}`)
  if (r.fixed?.length) parts.push(`${r.fixed.length} fixed: ${r.fixed.slice(0, 3).join(', ')}${r.fixed.length > 3 ? '…' : ''}`)
  return <span>{parts.join('; ')}</span>
}

const ABOUT_WORDS: Record<string, string> = { apptown: PRODUCT, 'way-of-working': 'the way of working', project: 'the project' }

function armLabel(arms: Arm[], id?: string | null): string {
  const a = id ? arms.find((x) => x.id === id) : undefined
  if (!a) return 'an arm'
  const how = a.arm === 'baseline' ? 'as it runs now' : 'varied'
  return a.sourceChangeNumber ? `Change ${a.sourceChangeNumber} ${how}` : how
}

function ComplaintsByAbout({ complaints, arms }: { complaints: Complaint[]; arms: Arm[] }) {
  if (complaints.length === 0) return <div className="muted small">No complaints were filed in this eval.</div>
  const groups = new Map<string, Complaint[]>()
  for (const c of complaints) groups.set(c.about, [...(groups.get(c.about) ?? []), c])
  return (
    <section className="card">
      <h3 style={{ marginTop: 0 }}>Complaints filed in the eval</h3>
      <p className="muted small" style={{ marginTop: 0 }}>A signal, not a defect count. Only those about {PRODUCT} also reach {PRODUCT}'s queue.</p>
      {[...groups.entries()].map(([about, list]) => (
        <div key={about} style={{ marginBottom: 8 }}>
          <strong className="small">About {ABOUT_WORDS[about] ?? about} ({list.length})</strong>
          {list.map((c) => <div key={c.id} className="small" style={{ marginTop: 2 }}>#{c.number} ({armLabel(arms, c.armId)}{c.packVersion ? `, ${c.packVersion}` : ''}): {c.text}</div>)}
        </div>
      ))}
    </section>
  )
}
