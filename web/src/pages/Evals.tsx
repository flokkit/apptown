import { QuietConfirm } from '../QuietConfirm'
import { ListTable } from '../ListTable'
import { Status } from '../Status'
import { PRODUCT } from '../brand'
import { useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { del, post, type Change } from '../api'
import { useAction, useGet, useGetText } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { fmtCost, fmtPrice, relTime } from '../format'
import type { Arm, CheckRow, Complaint, Eval, EvalList, EvalScorecard, EvalStartForm, ProjectRow, Tally } from '../wire.gen'
import { PageHead } from '../PageHead'
import { Section } from '../Section'
import './Evals.css'

// Evals: is an AI setup, or a pack version, better
// or worse than what projects run now? Finished Changes are re-run in
// throwaway copies, as their project runs now and with the one thing
// varied, and a person compares side by side: the work, each check's
// findings against the Change's base, and the cost. The scorecard decides
// nothing and is never evidence.

// Money follows the one rule in format.ts, so arms and the total agree.
const dollars = (micros: number, incomplete = 0) => fmtPrice(micros, incomplete)


// What an eval read refused to the caller says: who may, not an error.
const EVALS_DENIED = 'Evals are read by standards administrators and auditors, and started by people who manage packs; you are neither.'

export function EvalsPage() {
  const q = useGet<EvalList>('/evals', { poll: 10000 })
  return (
    <div className="evals-page">
      <PageHead page="evals" actions={<Link className="btn primary" to="/evals/new">Start an eval</Link>} />
      <ReadStatus q={q} what="the evals" denied={EVALS_DENIED} />
      <StaleNote q={q} />
      {q.data && <Section title="Evals" count={q.data.evals.length}>
        {q.data.evals.length === 0 ? <p className="muted">No evals yet.</p> : q.data.evals.map((e) => <EvalRow key={e.id} e={e} />)}
      </Section>}
    </div>
  )
}

function EvalRow({ e }: { e: Eval }) {
  return (
    <div className="card">
      <div className="row evals-row">
        <Link className="tap-open" to={`/evals/${e.id}`}><strong>Eval {e.number}</strong></Link>
        <span>{e.subject} against what projects run now</span>
        <Status word={e.statusLabel} help={e.reason} />
        <span className="grow" />
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
  // What the disabled start button waits for, said beside it.
  const missing = [
    subject === '' && (varies === 'model' ? 'an AI setup' : 'a pack'),
    picked.length === 0 && (slug ? 'a Change' : 'a project'),
    !(budgetDollars > 0) && 'a budget',
  ].filter((m): m is string => !!m)
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
    <div className="evals-form">
      <h1>Start an eval</h1>
      <ReadStatus q={formQ} what="the start form" denied={EVALS_DENIED} />
      <StaleNote q={[formQ, projectsQ, changesQ]} />
      {form && (
        <>
          <section className="card mb-3" >
            <h3 className="mt-0">1. What to compare</h3>
            <div className="row small evals-choices">
              <label><input type="radio" name="varies" checked={varies === 'pack'} onChange={() => { setVaries('pack'); setConfirming(false) }} /> a pack version</label>
              <label><input type="radio" name="varies" checked={varies === 'model'} onChange={() => { setVaries('model'); setConfirming(false) }} /> an AI setup</label>
            </div>
            {varies === 'model' && (
              <label className="small evals-compare-label">Compare what projects run now with
                <select value={profile} onChange={(e) => { setProfile(e.target.value); setConfirming(false) }} className="evals-inline-field">
                  <option value="">an AI setup…</option>
                  {form.profiles.map((p) => <option key={p} value={p}>{p}</option>)}
                </select>
              </label>
            )}
            {varies === 'pack' && (
              <div className="small evals-note">
                <label>Compare what projects run now with
                  <select value={pack} onChange={(e) => { const c = form.packs.find((p) => p.name === e.target.value); setPack(e.target.value); setVersion(c?.latest ?? 0); setConfirming(false) }} className="evals-inline-field">
                    <option value="">a pack…</option>
                    {form.packs.map((p) => <option key={p.name} value={p.name}>{p.name}</option>)}
                  </select>
                </label>
                {choice && (
                  <label className="evals-inline-field">version
                    <select value={version} onChange={(e) => { setVersion(Number(e.target.value)); setConfirming(false) }} className="evals-inline-field">
                      {Array.from({ length: choice.latest }, (_, i) => choice.latest - i).map((v) => <option key={v} value={v}>v{v}{v === choice.recommended ? ' (recommended now)' : ''}</option>)}
                    </select>
                  </label>
                )}
                <div className="muted evals-note">Compare two setups on the same work, using the same AI model. Both runs check the current and proposed rules.</div>
              </div>
            )}
          </section>
          <section className="card mb-3" >
            <h3 className="mt-0">2. The work to re-run (up to {max} landed Changes)</h3>
            <ReadStatus q={projectsQ} what="your projects" />
            {projectsQ.data && (
              <select aria-label="Project" value={slug} onChange={(e) => { setSlug(e.target.value); setConfirming(false) }}>
                <option value="">a project…</option>
                {projectsQ.data.map((p) => <option key={p.slug} value={p.slug}>{p.name}</option>)}
              </select>
            )}
            {slug && <ReadStatus q={changesQ} what="its Changes" />}
            {slug && changesQ.data && landed.length === 0 && <div className="muted small evals-note">No landed Change of this project can be re-run (it needs a base commit).</div>}
            {landed.map((c) => c.id && (
              <label key={c.id} className="row small mt-1 evals-pick">
                <input type="checkbox" checked={picked.includes(c.id)} disabled={!picked.includes(c.id) && picked.length >= max} onChange={() => { toggle(c.id!); setConfirming(false) }} />
                <span>#{c.number} {c.title}</span>
              </label>
            ))}
            {picked.length> 0 && <div className="muted small evals-note">{picked.length} picked{pickedHere.length ? `: ${pickedHere.join(', ')}` : ''}</div>}
          </section>
          <section className="card mb-3" >
            <h3 className="mt-0">3. Budget</h3>
            <label className="small">Budget $ <input type="number" min="0.01" step="0.5" value={budget} onChange={(e) => { setBudget(e.target.value); setConfirming(false) }} className="evals-budget" /></label>
            <div className="muted small evals-note">Includes checks and summaries. Work already running can exceed the budget; missing prices pause further work.</div>
          </section>
          {!confirming && <div className="row">
            <button disabled={!ready} onClick={() => setConfirming(true)}>Review and start…</button>
            {!ready && missing.length > 0 && <span className="muted small evals-missing">Pick {missing.length > 1 ? `${missing.slice(0, -1).join(', ')} and ${missing.at(-1)}` : missing[0]}</span>}
          </div>}
          {confirming && (
            <section className="card evals-warning">
              <p className="mt-0">Each of the {picked.length} Change{picked.length === 1 ? '' : 's'} is re-run twice in throwaway copies: as its project runs now, and with <strong>{varies === 'model' ? `model profile ${profile}` : `pack version ${subject}`}</strong>. Copies run landing builds with no one approving them. Configured releases use the eval domain only. Copies are deleted with the eval. Budget ${budgetDollars.toFixed(2)}, checked before each arm and step.</p>
              <p><strong>Starting means: “{form.meaning}”</strong></p>
              {actErr && <div className="error">{actErr}</div>}
              <div className="row evals-actions">
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
      <ReadStatus q={q} what="this eval" denied={EVALS_DENIED} />
      <StaleNote q={q} />
      {actErr && <div className="error small">{actErr}</div>}
      {e && (
        <>
          <div className="row evals-row">
            <h1 className="evals-title">Eval {e.number}: {e.subject}</h1>
            <Status word={e.statusLabel} help={e.reason} />
          </div>
          <div className="muted small mt-1" >
            Started by {e.startedBy} {relTime(e.startedAt)}, meaning “{e.meaning}”
            {e.subjectDigest && <> · tested exactly <code title="digest of the version's resolved spec and pins at start">{e.subjectDigest}</code></>}
          </div>
          <div className="row evals-summary">
            <span>Spent <strong>{dollars(e.costMicros, e.incomplete)}</strong> of {fmtCost(e.budgetMicros)}</span>
            {e.incomplete > 0 && <span className="muted small">{e.incomplete} model {e.incomplete === 1 ? 'call' : 'calls'} spent an unknown amount</span>}
          </div>
          {e.reason && e.state !== 'held' && <div className="small mb-2" >{e.reason}</div>}
          {e.state === 'held' && (
            <div className="card mb-3 evals-warning">
              <div>Held: the next arm waits. Continuing says you answer for spend {PRODUCT} could not count; the cost stays “at least”.</div>
              <button disabled={busy}  onClick={() => run(() => post(`/evals/${e.id}/continue`))} className="evals-note">Continue</button>
            </div>
          )}
          <Tallies e={e} variant={variantWords} />
          <p className="muted small">Eval approvals are automatic. Human acceptance is not measured.</p>
          {groupByChange(e.arms).map(([change, arms]) => (
            <ChangeSection key={change} e={e} change={change} arms={arms} variant={variantWords} />
          ))}
          <ComplaintsByAbout complaints={e.complaints} arms={e.arms} />
          <div className="mt-4">
            <QuietConfirm label="Delete this eval" yes="Delete eval" no="Cancel" disabled={busy}
              ask={`Delete eval ${e.number}? Its arms stop and its copies are deleted; what it cost and what was said in it are kept.`}
              onYes={() => run(async () => { await del(`/evals/${e.id}`); nav('/evals') })} />
            <span className="muted small evals-aside">its starter, while managing packs, or an administrator</span>
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
    return <div className="muted small mb-2" >No Change has both arms finished comparably yet{e.notComparable > 0 ? `; ${e.notComparable} not comparable` : ''}.</div>
  }
  return (
    <section className="card evals-tally">
      <h3 className="mt-0">{variant} against as it runs now</h3>
      <div className="muted small">Over {e.comparable} Change{e.comparable === 1 ? '' : 's'}{e.notComparable > 0 ? ` (${e.notComparable} not comparable, left out)` : ''}. Observed in this run: a small sample, and a flaky check can move it.</div>
      <ListTable label="Measures" className="small evals-note" rows={e.tallies} rowKey={(t: Tally) => t.measure} columns={[
        { key: 'measure', label: 'Measure', phone: 'name', render: (t: Tally) => t.measure },
        { key: 'better', label: 'better', kind: 'number', render: (t: Tally) => t.better },
        { key: 'same', label: 'same', kind: 'number', render: (t: Tally) => t.same },
        { key: 'worse', label: 'worse', kind: 'number', render: (t: Tally) => t.worse },
        { key: 'unknown', label: 'unknown', kind: 'number', render: (t: Tally) => <span className="muted">{t.unknown}</span> },
      ]} />
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
    <section className="card evals-tally">
      <h3 className="mt-0">{arms[0]?.sourceChangeNumber ? <>Change <Link to={`/changes/${change}`}>{arms[0].sourceChangeNumber}</Link>, re-run</> : 'A Change, re-run'}</h3>
      <div className="eval-arms">
        <div className="eval-arm">
          <strong>The original</strong>
          <div className="muted small mt-1" >The accepted work, shown for reference; not re-run.</div>
          <div className="small mt-1" ><Link to={`/changes/${change}`}>its Change and diff</Link></div>
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
      <div className="row evals-arm-heading">
        <strong>{label}</strong>
        <span className="muted small">{a.profile}</span>
        <Status word={a.statusLabel} help={a.reason} />
        {s.notComparable && <span className="badge">not comparable</span>}
      </div>
      {a.reason && <div className="small mt-1" >{a.reason}</div>}
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
        <div className="row small mt-1 evals-arm-meta">
          <span className="muted">landing gates:</span>
          {(s.checks ?? []).map((c) => <span key={c.step}>{c.step}: <Status word={c.statusLabel} /></span>)}
        </div>
      )}
      {a.projectId ? (
        <div className="small mt-1" >
          <button className="subtle" onClick={() => setShowDiff(!showDiff)}>{showDiff ? 'Hide its diff' : 'Show its diff'}</button>
          {a.changeId ? <> · <Link to={`/changes/${a.changeId}`}>the copy's Change{a.changeNumber ? ` ${a.changeNumber}` : ''}</Link></> : null}
        </div>
      ) : null}
      {showDiff && (diff.loading ? <div className="muted small">Reading the diff…</div>
        : diff.err ? <div className="error small">The diff could not be read: {diff.err}</div>
          : <pre className="small evals-arm-output">{diff.data || '(no change from the base)'}</pre>)}
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
    <section className="mt-2">
      <h3 className="small">Checks, finding by finding, against the Change's base ({keys.length})</h3>
      <ListTable label="Checks" className="small evals-checks" rows={keys} rowKey={k => k} columns={[
        { key: 'check', label: 'Check', phone: 'name', render: k => {
          const any = (b.get(k) ?? v.get(k))!
          const only = declares(baseline, any.block, any.version) === declares(variant, any.block, any.version) ? ''
            : declares(variant, any.block, any.version) ? 'only the varied setup declares this' : 'only today\'s setup declares this'
          return <><code>{any.block} · {any.check}</code> <span className="muted">v{any.version}</span>{only && <div className="muted">{only}</div>}</>
        } },
        { key: 'base', label: 'Base', render: k => (b.get(k) ?? v.get(k))!.base },
        { key: 'now', label: 'As it runs now', render: k => <Cell r={b.get(k)} /> },
        { key: 'variant', label: variantWords, render: k => <Cell r={v.get(k)} /> },
      ]} />
    </section>
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
      <h3 className="mt-0">Complaints filed in the eval</h3>
      <p className="muted small mt-0" >Reports about {PRODUCT} also enter its queue.</p>
      {[...groups.entries()].map(([about, list]) => (
        <div key={about} className="mb-2">
          <strong className="small">About {ABOUT_WORDS[about] ?? about} ({list.length})</strong>
          {list.map((c) => <div key={c.id} className="small evals-detail">#{c.number} ({armLabel(arms, c.armId)}{c.packVersion ? `, ${c.packVersion}` : ''}): {c.text}</div>)}
        </div>
      ))}
    </section>
  )
}
