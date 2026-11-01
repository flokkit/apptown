import { PRODUCT } from '../../../brand'
import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { GuidanceBlock, GuidanceImport, HttpError, ProjectGuidance, SteeringSection, del, post, put } from '../../../api'
import { fmtSize, fmtStamp, sizeTitle } from '../../../format'
import { OriginChip } from '../../../OriginChip'
import { QuietConfirm } from '../../../QuietConfirm'
import { useAction, useGet } from '../../../data'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import type { AdoptionApplied, AdoptionUpliftOpened, Candidate as GeneratedCandidate, PackPick as GeneratedPackPick, Policy, ProjectAdoptionRecord, LaneView, ProjectLanes, ProjectPolicyList, Violation, FleetView } from '../../../wire.gen'

// The project's adoption revision (design/subsystems/ways-of-working-versions.md
// §1.1): the exact way of working it is on, written down. Releases no
// longer move it; a person's edit here records the next revision.
type ProjectPack = { pack: string; version: number; recommended: number; platform?: boolean; reason: string }
type AdoptionView = ProjectAdoptionRecord

export function AdoptionLine({ slug }: { slug: string }) {
  const q = useGet<AdoptionView>(`/projects/${slug}/adoption`)
  const v = q.data
  if (!v) return <ReadStatus q={q} what="the adoption record" className="small" />
  const cur = v.current
  if (!cur) return <StaleNote q={q} />
  const when = fmtStamp(cur.createdAt)
  const packs = v.packs ?? []
  const worry = (v.conditions ?? []).length > 0
  return (
    <>
    <StaleNote q={q} />
    <div className={`card ${worry ? 'tone-warn' : ''}`} style={{ padding: '8px 12px', marginBottom: 10, maxWidth: 760 }}>
      <div style={{ marginBottom: 6 }}>
        <strong>{packs.length === 0 ? 'Follows its own guidance' : `Follows ${packs.length} ways of working`}</strong>
        {packs.length > 0 && <details><summary>Names</summary>{packs.map(pk => <div key={pk.pack}>{pk.pack} · version {pk.version}</div>)}</details>}
      </div>
      <div className="small muted">
        {(v.additions.length > 0 || v.exclusions.length > 0) && <>{v.additions.length} additions · {v.exclusions.length} excluded</>}
        {packs.some(pk => pk.recommended > 0 && pk.recommended !== pk.version) && <span> · A recommended version is available</span>}
      </div>
      {(v.additions.length > 0 || v.exclusions.length > 0) && <details style={{ marginTop: 6 }}>
        <summary>View project customisations</summary>
        {v.additions.map(b => <div key={b.name}>Added: <code>{b.name}@{b.version}</code></div>)}
        {v.exclusions.map(b => <div key={b.name}>Excluded: <code>{b.name}</code></div>)}
      </details>}
      {(v.conditions ?? []).map((c, i) => (
        <div key={i} className="small" style={{ marginBottom: 4 }}>⚠ {c.sentence}</div>
      ))}
      <details style={{ marginTop: 8 }}>
        <summary>History</summary>
        <div className="muted small">{cur.reason} · {cur.createdBy || 'author not recorded'} · {when}</div>
        <code className="muted small">{cur.digest}</code>
      </details>
      <div className="muted small" style={{ marginTop: 4 }}>
        <Link to={`/conformance/projects/${slug}`}>See whether the application meets them →</Link>
      </div>
    </div>
    </>
  )
}

// Adopting a way of working (design/subsystems/ways-of-working-versions.md §11):
// prepare says what would change and never touches the project; adopt
// moves it in one step, or opens an ordinary Change that will when it
// lands. Adoption and conformance are different facts: after adopting, the
// conformance review says what is outstanding.
type PackPick = GeneratedPackPick
type Candidate = GeneratedCandidate

export function AdoptPanel({ slug, packs, onChanged }: { slug: string; packs: ProjectPack[]; onChanged: () => void }) {
  const nav = useNavigate()
  const libraryQ = useGet<FleetView>('/wow')
  const library = libraryQ.data?.packs ?? []
  const [picks, setPicks] = useState<PackPick[] | null>(null)
  const [cand, setCand] = useState<Candidate | null>(null)
  // opened is an adoption Change whose working run did not get going: the
  // Change stands, and the reason is said here rather than lost by moving
  // to the Change's page.
  const [opened, setOpened] = useState<{ changeId: string; runError?: string; notStarted?: string } | null>(null)
  // Adopting moves the project's guidance and the library's usage counts as
  // well as the project's own reads.
  const { run, busy, err, setErr } = useAction(undefined, { affects: ['/guidance', '/wow'] })
  useEffect(() => { setPicks(null); setCand(null) }, [slug])
  const current = picks ?? packs.map((p) => ({ pack: p.pack, version: p.version }))
  const updates = packs.filter((p) => p.recommended > 0 && p.recommended !== p.version)
  const notOn = library.filter((l) => l.enabled && !l.platform && l.recommended > 0 && !packs.some((p) => p.pack === l.pack))
  const prepare = async (next: PackPick[]) => {
    setPicks(next)
    const c = await run(() => post<Candidate>(`/projects/${slug}/adoption/prepare`, { packs: next }))
    if (c) setCand(c)
  }
  const setVersion = (pack: string, version: number) => prepare([...current.filter((p) => p.pack !== pack), { pack, version }])
  const drop = (pack: string) => prepare(current.filter((p) => p.pack !== pack))
  const adopt = async (viaChange: boolean) => {
    if (!cand) return
    const r = await run(() => post<AdoptionUpliftOpened | AdoptionApplied>(`/projects/${slug}/adoption/adopt`, { expectedRevision: cand.expectedRevision, packs: cand.packs, viaChange }))
    if (!r) return
    if ('changeId' in r) {
      if (r.notStarted || r.runError) setOpened({ changeId: r.changeId ?? '', runError: r.runError, notStarted: r.notStarted })
      else nav(`/changes/${r.changeId}`)
    }
    else { setCand(null); setPicks(null); onChanged() }
  }
  const needsWork = cand ? cand.newChecks.length > 0 : false
  return (
    <div style={{ marginBottom: 12, maxWidth: 760 }}>
      {opened && (
        <div className="warn small" role="status" style={{ marginBottom: 6 }}>
          <Link to={`/changes/${opened.changeId}`}>The adoption Change</Link> is open, but{' '}
          {opened.runError
            ? <>its working run did not get going: {opened.runError}. Open the Change to see the run.</>
            : <>no working run was started: {opened.notStarted}. Start a workflow from the Change.</>}
        </div>
      )}
      <ReadStatus q={libraryQ} what="the pack library" className="small" />
      <StaleNote q={libraryQ} />
      {updates.length > 0 && !cand && (
        <div className="row small" style={{ marginBottom: 6 }}>
          <span className="muted">Recommended:</span>
          {updates.map((u) => (
            <button key={u.pack} className="subtle" disabled={busy} onClick={() => setVersion(u.pack, u.recommended)} title="see what moving would change; nothing moves yet">
              Review {u.pack} v{u.version} → v{u.recommended}
            </button>
          ))}
        </div>
      )}
      {notOn.length > 0 && !cand && (
        <details><summary>Add a way of working</summary><div className="row small" style={{ marginBottom: 6 }}>
          <span className="muted">Choose:</span>
          <select value="" onChange={(e) => { const l = library.find((x) => x.pack === e.target.value); if (l) setVersion(l.pack, l.recommended) }}>
            <option value="">a pack…</option>
            {notOn.map((l) => <option key={l.pack} value={l.pack}>{l.pack} v{l.recommended}</option>)}
          </select>
          {err && <span className="error small">{err}</span>}
        </div></details>
      )}
      {cand && (
        <div className="card" style={{ padding: '8px 12px' }}>
          <div className="row small" style={{ marginBottom: 4 }}>
            <b>Prepared:</b> {cand.packs.map((p) => `${p.pack} v${p.version}`).join(' · ') || 'no pack'}
            {cand.additions.length > 0 && <span className="muted">+ {cand.additions.length} of its own</span>}
            <span style={{ flex: 1 }} />
            {cand.packs.map((p) => <button key={p.pack} className="subtle" disabled={busy} onClick={() => drop(p.pack)} title={`prepare without ${p.pack}`}>− {p.pack}</button>)}
            <button className="subtle" onClick={() => { setCand(null); setPicks(null); setErr('') }}>cancel</button>
          </div>
          {cand.problems.length > 0 && cand.problems.map((p, i) => <div key={i} className="small">⚠ {p}</div>)}
          {cand.weakens.length > 0 && <div className="small">⚠ drops {cand.weakens.join(', ')}, which a selected pack asks of this project · that takes the standards authority</div>}
          {cand.moves.some((m) => m.to === 0 || m.to < m.from) && <div className="small warn-line">Moves back: {cand.moves.filter((m) => m.from > 0 && (m.to === 0 || m.to < m.from)).map((m) => m.to === 0 ? `removes ${m.rule} (v${m.from})` : `${m.rule} v${m.from}→v${m.to}`).join(', ')}</div>}
          {cand.kept.length > 0 && <div className="small">Still on this project after adopting: {cand.kept.join(', ')}. The new version no longer carries them; remove them here if you want them gone.</div>}
          <p className="small">
            {cand.added.length} guidance blocks added · {cand.changed.length} definitions updated · {cand.removed.length} removed.
            {' '}{cand.lanesAdded.length} workflows added · {cand.lanesRemoved.length} removed.
          </p>
          {cand.newChecks.length > 0 && <details>
            <summary>{cand.newChecks.length} new checks in this update</summary>
            {cand.newChecks.map(c => <div key={`${c.block}:${c.check}`} className="card" style={{ padding: 10, marginTop: 6 }}>
              <strong className="small">{c.title || c.expects || c.check}</strong>
              {c.title && c.expects && <div className="small">{c.expects}</div>}
              <code className="muted small">{c.block}{c.version ? `@${c.version}` : ''} · {c.check}</code>
            </div>)}
          </details>}
          <details style={{ marginTop: 8 }}>
            <summary>View exact changes</summary>
            {cand.added.length > 0 && <div>Guidance added: {cand.added.join(', ')}</div>}
            {cand.changed.length > 0 && <div>Definitions updated: {cand.changed.join(', ')}</div>}
            {cand.older.length > 0 && <div>Older versions: {cand.older.join(', ')}</div>}
            {cand.removed.length > 0 && <div>Guidance removed: {cand.removed.join(', ')}</div>}
            {cand.lanesAdded.length > 0 && <div>Workflows added: {cand.lanesAdded.join(', ')}</div>}
            {cand.lanesRemoved.length > 0 && <div>Workflows removed: {cand.lanesRemoved.join(', ')}</div>}
            {cand.added.length + cand.changed.length + cand.older.length + cand.removed.length + cand.lanesAdded.length + cand.lanesRemoved.length === 0 && <div>Nothing would change.</div>}
          </details>
          <p className="small muted">Adopting selects these standards. A conformance review establishes whether the application meets them.</p>
          {cand.problems.length === 0 && (
            <div className="row small" style={{ marginTop: 6 }}>
              <button className="primary" disabled={busy} onClick={() => adopt(false)} title="the project moves now; the conformance review then says what is outstanding">Adopt now</button>
              {needsWork && <button disabled={busy} onClick={() => adopt(true)} title="an ordinary change carries the target; the project moves when it lands, and its gate must pass the new checks">Adopt through a change</button>}
              {err && <span className="error small">{err}</span>}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

export function GuidanceTab({ slug }: { slug: string }) {
  const guidanceQ = useGet<ProjectGuidance>(`/projects/${slug}/guidance`)
  const libraryQ = useGet<GuidanceBlock[]>('/guidance/blocks', { kind: 'config' })
  const adoptionQ = useGet<AdoptionView>(`/projects/${slug}/adoption`)
  const library = libraryQ.data ?? []
  const adoptionPacks = adoptionQ.data?.packs ?? []
  // The block list is edited here before it is saved: seeded from the server,
  // and reseeded only while nothing is unsaved.
  const [imports, setImports] = useState<GuidanceImport[]>([])
  const [effective, setEffective] = useState<ProjectGuidance['effective'] | null>(null)
  const [generated, setGenerated] = useState<SteeringSection[]>([])
  const [dirty, setDirty] = useState(false)
  // Until the list is seeded it is empty, and saving an empty list with one
  // block added would drop every block the project had: nothing is editable
  // until this page has read the project's guidance itself.
  const [seeded, setSeeded] = useState(false)
  // The adoption revision the list was read at; a save sends it, and the
  // server refuses it if the way of working moved on meanwhile.
  const [revision, setRevision] = useState(0)
  const [conflict, setConflict] = useState(false)
  const guidance = guidanceQ.data, guidanceCurrent = guidanceQ.current
  useEffect(() => {
    // Only from an answer this page read itself (see useGet's current).
    if (!guidance || !guidanceCurrent || dirty) return
    setImports(guidance.imports); setEffective(guidance.effective); setGenerated(guidance.generated ?? []); setRevision(guidance.revision); setSeeded(true)
  }, [guidance, guidanceCurrent, dirty])
  const [pick, setPick] = useState('')
  const [msg, setMsg] = useState('')
  // A save moves the effective guidance the instance's pages show too.
  const { run, busy: saving, err: saveErr, failure, setErr } = useAction(undefined, { affects: ['/guidance', '/wow'] })
  const moved = failure instanceof HttpError && failure.code === 'settings-moved'
  const err = moved ? '' : saveErr
  const reloadGuidance = guidanceQ.reload
  useEffect(() => { if (moved) { setConflict(true); void reloadGuidance() } }, [moved, reloadGuidance])

  const mutate = (fn: (list: GuidanceImport[]) => GuidanceImport[]) => {
    setImports((l) => fn([...l])); setDirty(true); setMsg('')
  }
  const move = (i: number, d: number) => mutate((l) => {
    const j = i + d
    if (j < 0 || j >= l.length) return l
    ;[l[i], l[j]] = [l[j], l[i]]
    return l
  })

  const save = async () => {
    setMsg(''); setConflict(false)
    const g = await run(() => put<ProjectGuidance>(`/projects/${slug}/guidance`, {
      expectedRevision: revision,
      imports: imports.map((im) => ({ name: im.name, version: im.version, disabled: im.disabled })),
    }))
    if (!g) return
    setImports(g.imports); setEffective(g.effective); setGenerated(g.generated ?? []); setRevision(g.revision); setDirty(false)
    setMsg(`saved · effective hash ${g.effective.hash.slice(0, 12)}…`)
  }
  // Keep my list: it replaces the list as it is now, at the current revision.
  const keepMine = () => { if (guidance) setRevision(guidance.revision); setConflict(false); setErr('') }
  const discardMine = () => { setDirty(false); setConflict(false); setErr('') }

  const notImported = library.filter((b) => !imports.some((im) => im.name === b.name))
  const enabled = imports.filter((im) => !im.disabled)
  const enabledBytes = enabled.reduce((n, im) => n + (im.bytes ?? 0), 0)
  const generatedAlways = generated.filter((g) => g.always).reduce((n, g) => n + g.bytes, 0)
  const generatedConditional = generated.filter((g) => !g.always).reduce((n, g) => n + g.bytes, 0)

  return (
    <div>
      <AdoptionLine slug={slug} />
      <AdoptPanel slug={slug} packs={adoptionPacks} onChanged={() => setDirty(false)} />
      <details style={{ marginTop: 14 }}>
        <summary>View and edit workflows</summary>
        <WorkflowsTab slug={slug} />
      </details>
      <details style={{ marginTop: 14 }}>
        <summary>Edit guidance{dirty ? ' · unsaved changes' : ''}</summary>
      <details className="small"><summary>About adoption</summary><p className="muted small" style={{ maxWidth: 640, marginTop: 0 }}>
        What agents on this project are told, in order · later blocks override earlier ones.
        Pick from the instance's <Link to="/guidance">guidance</Link>; every run records exactly which version it was given.
      </p></details>
      <table className="stack" style={{ maxWidth: 760 }}>
        <thead><tr><th></th><th>Block</th><th>Pinned</th><th title="what the block adds to an agent's context">Size</th><th>Enabled</th><th></th></tr></thead>
        <tbody>
          {imports.map((im, i) => (
            <tr key={im.name} style={im.disabled ? { opacity: 0.5 } : undefined}>
              <td>
                <button className="subtle" aria-label="move up" title="move up" onClick={() => move(i, -1)} disabled={i === 0}>↑</button>
                <button className="subtle" aria-label="move down" title="move down" onClick={() => move(i, 1)} disabled={i === imports.length - 1}>↓</button>
              </td>
              <td className="stack-lead"><Link to={`/guidance/${im.blockId}`}><code>{im.name}</code></Link>{im.withdrawn && <> <span className="badge" title={`${PRODUCT} no longer ships this block; this project keeps its version until it removes it`}>No longer shipped</span></>}</td>
              <td data-label="pinned">
                v{im.version}
                {im.version < im.latestVersion && (
                  <button className="subtle" onClick={() => mutate((l) => { l[i] = { ...l[i], version: l[i].latestVersion }; return l })}>
                    bump to v{im.latestVersion}
                  </button>
                )}
              </td>
              <td className="muted small" title={sizeTitle(im.bytes ?? 0)}>{fmtSize(im.bytes ?? 0)}</td>
              <td data-label="enabled">
                <label className="check-tap" title={`give agents ${im.name}`}>
                  <input type="checkbox" checked={!im.disabled} aria-label={`enabled: ${im.name}`}
                    onChange={(e) => mutate((l) => { l[i] = { ...l[i], disabled: !e.target.checked }; return l })} />
                </label>
              </td>
              <td><button className="subtle" onClick={() => mutate((l) => l.filter((_, j) => j !== i))}>remove</button></td>
            </tr>
          ))}
        </tbody>
      </table>
      {seeded && imports.length === 0 && <div className="muted" style={{ padding: 12 }}>No guidance imported yet · agents on this project work without the house rules.</div>}
      {imports.length > 0 && (
        <div className="muted small" style={{ marginTop: 6 }}>
          {enabled.length} of {imports.length} enabled, <span title={sizeTitle(enabledBytes)}>{fmtSize(enabledBytes)}</span> of blocks.
          {generatedAlways > 0 && <> {PRODUCT} adds <span title={sizeTitle(generatedAlways)}>{fmtSize(generatedAlways)}</span> of generated sections to every agent step, <strong title={sizeTitle(enabledBytes + generatedAlways)}>{fmtSize(enabledBytes + generatedAlways)}</strong> all together</>}
          {generatedConditional > 0 && <>; some steps get up to <span title={sizeTitle(generatedConditional)}>{fmtSize(generatedConditional)}</span> more</>}.
        </div>
      )}
      {generated.length > 0 && (
        <details style={{ marginTop: 6 }}>
          <summary className="muted small" style={{ cursor: 'pointer' }}>The generated sections, one by one</summary>
          <table style={{ maxWidth: 760, marginTop: 6 }}>
            <thead><tr><th>Section</th><th>When</th><th>Size</th><th>What it is</th></tr></thead>
            <tbody>
              {generated.map((g) => (
                <tr key={g.name}>
                  <td><code>{g.name}</code></td>
                  <td><span className="badge">{g.always ? 'every step' : 'some steps'}</span></td>
                  <td className="muted small" title={sizeTitle(g.bytes)}>{fmtSize(g.bytes)}</td>
                  <td className="muted small">{g.note}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="muted small" style={{ marginTop: 4 }}>Read them in full under <Link to="/guidance">Ways of working › Guidance › Generated</Link>.</div>
        </details>
      )}
      <div className="row" style={{ marginTop: 8 }}>
        <select value={pick} onChange={(e) => setPick(e.target.value)}>
          <option value="">import block…</option>
          {notImported.map((b) => <option key={b.id} value={b.name}>{b.name} (v{b.latestVersion}, {fmtSize(b.bytes ?? 0)})</option>)}
        </select>
        <button disabled={!pick || !seeded} onClick={() => {
          const b = library.find((x) => x.name === pick)
          if (!b) return
          mutate((l) => [...l, { position: l.length, blockId: b.id, name: b.name, version: b.latestVersion, latestVersion: b.latestVersion, disabled: false, bytes: b.bytes ?? 0, origin: b.origin }])
          setPick('')
        }}>Add</button>
        <button className="primary" onClick={save} disabled={!dirty || !seeded || conflict || saving}>Save</button>
        {!seeded && <span className="muted small">reading the current guidance before editing…</span>}
        {msg && <span className="muted small">{msg}</span>}
      </div>
      {conflict && (
        <div className="card tone-warn" style={{ marginTop: 8 }}>
          <div>The project's way of working changed after you started (another edit, a pack pulled or a workflow selected). Your list is kept here, unsaved.</div>
          {guidance && <div className="muted small" style={{ marginTop: 4 }}>The list is now: {guidance.imports.map((im) => im.name).join(', ') || '(empty)'}.</div>}
          <div className="row" style={{ marginTop: 8 }}>
            <button onClick={keepMine}>Keep my list over it</button>
            <button className="subtle" onClick={discardMine}>Discard my edits</button>
          </div>
        </div>
      )}
      <ReadStatus q={[guidanceQ, libraryQ]} what="the guidance" />
      <StaleNote q={[guidanceQ, libraryQ]} />
      {err && <div className="error">{err}</div>}
      </details>

      {effective && (
        <div>
          <details style={{ marginTop: 10 }}>
            <summary className="muted small" style={{ cursor: 'pointer' }}>Full guidance</summary>
            {dirty && <div className="muted small">showing the last saved version · save to refresh</div>}
            <pre className="log" style={{ maxHeight: 400 }}>{effective.text || '(empty)'}</pre>
          </details>
        </div>
      )}
    </div>
  )
}

// Policies (design/subsystems/policy.md): assertions over the workflow set, enforced at
// save time. This card shows the live conformance matrix.
export type PolicyT = Policy

export type ViolationT = Violation

export function PoliciesCard({ slug }: { slug: string }) {
  const q = useGet<ProjectPolicyList>(`/projects/${slug}/policies`)
  const { data, reload } = q
  const { run, err } = useAction(reload)

  const remove = (p: PolicyT) => run(() => del(`/policies/${p.id}`))

  if (!data) return <ReadStatus q={q} what="the rules" className="small" />
  // An empty answer that may be old says so too: someone may have adopted
  // rules since.
  if (data.policies.length === 0) return <><StaleNote q={q} /><div className="muted small">No rules adopted · a pack usually brings some.</div></>
  return (
    <div className="card" style={{ marginBottom: 14 }}>
      <StaleNote q={q} />
      {err && <div className="error">{err}</div>}
      <div className="muted small">Checked every time a workflow is saved; older workflows that break a rule warn on each run.</div>
      {data.policies.map((p) => {
        const vs = data.violations.filter((v) => v.policy === p.name)
        return (
          <div className="row" key={p.id} style={{ marginTop: 6 }}>
            <span className="badge">
              {vs.length === 0 ? '✓' : `${vs.length} warning${vs.length > 1 ? 's' : ''}`}
            </span>
            <strong className="small">{p.name}</strong>
            <span className="muted small">{p.description || p.rule}{p.origin ? ` · ${p.origin}` : ''}</span>
            <span style={{ flex: 1 }} />
            <QuietConfirm label="remove" ask={`Remove policy "${p.name}"? Removal is audited.`} onYes={() => remove(p)} />
          </div>
        )
      })}
      {data.violations.map((v, i) => (
        <div key={i} className="muted small" style={{ marginTop: 4 }}>
          ⚠ {v.workflow}: {v.detail} <span className="muted">({v.policy})</span>
        </div>
      ))}
    </div>
  )
}

// Way of working › Workflows (design/decisions/workflows-as-lanes.md): the project
// SELECTS lanes from the library — a checklist, nothing copied. Core is the
// short list, Extras the longer tail; System is what the apptown runs for
// every project, shown so nobody wonders where landing lives.
export type Lane = LaneView

export function WorkflowsTab({ slug }: { slug: string }) {
  const q = useGet<ProjectLanes>(`/projects/${slug}/lanes`)
  const data = q.data
  const [busy, setBusy] = useState('')
  // Selecting a workflow is a /projects write, so the project's reads on
  // screen (the adoption line, the guidance) refresh with it.
  const { run, err } = useAction()

  const toggle = async (l: Lane) => {
    setBusy(l.name)
    await run(() => put(`/projects/${slug}/lanes`, { name: l.name, enabled: !l.selected }))
    setBusy('')
  }
  if (!data) return <ReadStatus q={q} what="the workflows" className="small" />
  const { lanes, system } = data

  const TIERS: [Lane['tier'], string, string][] = [
    ['core', 'Core', 'the short list · what most projects use'],
    ['extra', 'Extras', 'useful in particular situations'],
  ]
  const row = (l: Lane, selectable: boolean) => (
    <label key={l.name} className="card" style={{ display: 'flex', gap: 12, alignItems: 'flex-start', marginBottom: 8, cursor: selectable ? 'pointer' : 'default', opacity: l.deprecated ? 0.6 : 1 }}>
      {selectable
        ? <input type="checkbox" checked={l.selected} disabled={busy === l.name} onChange={() => toggle(l)} style={{ marginTop: 4 }} />
        : <span className="badge" style={{ marginTop: 2 }}>{PRODUCT} runs it</span>}
      <span style={{ flex: 1, minWidth: 0 }}>
        <span className="row" style={{ gap: 8 }}>
          <strong>{l.name}</strong>
          <OriginChip origin={l.origin} />
          <span className="badge">v{l.version}</span>
          {l.summary && <span className="badge" title={l.summary.pacing === 'paced by people'
            ? 'stops at gates and waits for a person' : 'advances by itself when checks pass'}>{l.summary.pacing}</span>}
          {l.deprecated && <span className="badge">deprecated</span>}
          {l.withdrawn && <span className="badge" title={`${PRODUCT} no longer ships this workflow; this project keeps it until it unselects it`}>No longer shipped</span>}
          <span className="muted small">{l.runsCount > 0 ? `used ${l.runsCount}× here` : 'never used here'}</span>
        </span>
        <span className="muted small" style={{ display: 'block', marginTop: 4 }}>{l.description}</span>
        {l.summary && <span className="small" style={{ display: 'block', marginTop: 4, opacity: 0.85 }}>{l.summary.flow}</span>}
      </span>
    </label>
  )
  return (
    <div>
      <StaleNote q={q} />
      <details className="small"><summary>About adoption</summary><p className="muted small" style={{ maxWidth: 640, marginTop: 0 }}>
        Select the workflows this project uses. Every run takes the version recorded in the project's adoption record above and
        records which one ran. To change how a workflow works, clone it in <Link to="/workflows">Ways of working</Link> and tick the clone here.
      </p></details>
      {TIERS.map(([k, label, gloss]) => (
        <div key={k}>
          <h3 style={{ margin: '10px 0 6px' }}>{label} <span className="muted small" style={{ fontWeight: 400 }}>— {gloss}</span></h3>
          {lanes.filter((l) => l.tier === k).map((l) => row(l, true))}
          {lanes.filter((l) => l.tier === k).length === 0 && <div className="muted small">none in the library</div>}
        </div>
      ))}
      <h3 style={{ margin: '10px 0 6px' }}>System <span className="muted small" style={{ fontWeight: 400 }}>— {PRODUCT} runs these for every project</span></h3>
      {system.map((l) => row(l, false))}
      {err && <div className="error">{err}</div>}
      <h2>Rules</h2>
      <PoliciesCard slug={slug} />
    </div>
  )
}
