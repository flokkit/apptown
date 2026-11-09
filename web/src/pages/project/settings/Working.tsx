import { ListRow } from '../../../ListRow'
import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ProjectGuidance, post } from '../../../api'
import { fmtStamp } from '../../../format'
import { useAction, useGet } from '../../../data'
import { ReadStatus, StaleNote, type Gated } from '../../../ReadGate'
import { Group, Section } from '../../../Section'
import './working.css'
import type { AdoptionApplied, AdoptionUpliftOpened, Candidate as GeneratedCandidate, PackPick as GeneratedPackPick, ProjectAdoptionRecord, ProjectLanes, ProjectPolicyList, FleetView } from '../../../wire.gen'

// The project's adoption revision (design/subsystems/ways-of-working-versions.md
// §1.1): the exact way of working it is on, written down. Releases no
// longer move it; a person's edit here records the next revision.
type ProjectPack = { pack: string; version: number; recommended: number; platform?: boolean; reason: string }
type AdoptionView = ProjectAdoptionRecord

// AdoptionFacts: the adoption record's facts besides the pack list, inside
// the Ways of working section: what it was recorded as, the project's own
// additions and exclusions, and any condition that needs attention.
export function AdoptionFacts({ v }: { v: AdoptionView }) {
  const cur = v.current
  if (!cur) return null
  const own = v.additions.length + v.exclusions.length
  return (
    <>
      {(v.conditions ?? []).map((c, i) => (
        <div key={i} className="card warn-line small">⚠ {c.sentence}</div>
      ))}
      {own > 0 && (
        <Group label="This project's own changes">
          {v.additions.map(b => <ListRow key={b.name} className="card row wow-row small wow-list-row" identity={<span>Added <code>{b.name}@{b.version}</code></span>} />)}
          {v.exclusions.map(b => <ListRow key={b.name} className="card row wow-row small wow-list-row" identity={<span>Excluded <code>{b.name}</code></span>} />)}
        </Group>
      )}
    </>
  )
}

// The adoption record's latest revision in one muted line: why, who, when.
function recordedLine(v: AdoptionView) {
  const cur = v.current
  if (!cur) return undefined
  return <span title={`record ${cur.digest}`}>Recorded {fmtStamp(cur.createdAt)} by {cur.createdBy || 'an author not recorded'} · {cur.reason}</span>
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
  const diff = cand ? [
    ['Guidance added', cand.added], ['Definitions updated', cand.changed], ['Older versions', cand.older],
    ['Guidance removed', cand.removed], ['Workflows added', cand.lanesAdded], ['Workflows removed', cand.lanesRemoved],
  ] as const : []
  return (
    <>
      {opened && (
        <div className="card warn small" role="status">
          <Link to={`/changes/${opened.changeId}`}>The adoption Change</Link> is open, but{' '}
          {opened.runError
            ? <>its working run did not get going: {opened.runError}. Open the Change to see the run.</>
            : <>no working run was started: {opened.notStarted}. Start a workflow from the Change.</>}
        </div>
      )}
      <ReadStatus q={libraryQ} what="the pack library" className="small" />
      <StaleNote q={libraryQ} />
      {/* One row per pack the project is on: its name, its version, and the
          one thing to do with it (review a recommended version). */}
      {packs.map((p) => {
        const update = p.recommended > 0 && p.recommended !== p.version
        return (
          <ListRow key={p.pack} className="card row wow-row wow-list-row" identity={<>
            <strong>{p.pack}</strong>
            <span className="muted small">version {p.version}{update ? ` · version ${p.recommended} recommended` : ''}</span>
            </>} action={update && !cand && (
              <span className="row-action">
                <button className="subtle" disabled={busy} onClick={() => setVersion(p.pack, p.recommended)} title="see what moving would change; nothing moves yet">
                  Review {p.pack} v{p.version} → v{p.recommended}
                </button>
              </span>
            )} />
        )
      })}
      {notOn.length > 0 && !cand && (
        <div className="card row wow-row small">
          <label htmlFor={`adopt-pick-${slug}`} className="muted">Add a way of working</label>
          <select id={`adopt-pick-${slug}`} value="" onChange={(e) => { const l = library.find((x) => x.pack === e.target.value); if (l) setVersion(l.pack, l.recommended) }}>
            <option value="">choose a pack…</option>
            {notOn.map((l) => <option key={l.pack} value={l.pack}>{l.pack} v{l.recommended}</option>)}
          </select>
        </div>
      )}
      {err && !cand && <div className="error small">{err}</div>}
      {cand && (
        <Group label="Prepared, not adopted yet">
          <div className="card row wow-row small">
            <strong>{cand.packs.map((p) => `${p.pack} v${p.version}`).join(' · ') || 'no pack'}</strong>
            {cand.additions.length > 0 && <span className="muted">+ {cand.additions.length} of its own</span>}
            <span className="grow" />
            {cand.packs.map((p) => <button key={p.pack} className="subtle" disabled={busy} onClick={() => drop(p.pack)} title={`prepare without ${p.pack}`}>− {p.pack}</button>)}
            <button className="subtle" onClick={() => { setCand(null); setPicks(null); setErr('') }}>Cancel</button>
          </div>
          <div className="wow-prepared small">
            {cand.problems.map((p, i) => <div key={i}>⚠ {p}</div>)}
            {cand.weakens.length > 0 && <div>⚠ drops {cand.weakens.join(', ')}, which a selected pack asks of this project · that takes the standards authority</div>}
            {cand.moves.some((m) => m.to === 0 || m.to < m.from) && <div className="warn-line">Moves back: {cand.moves.filter((m) => m.from > 0 && (m.to === 0 || m.to < m.from)).map((m) => m.to === 0 ? `removes ${m.rule} (v${m.from})` : `${m.rule} v${m.from}→v${m.to}`).join(', ')}</div>}
            {cand.kept.length > 0 && <div>Still on this project after adopting: {cand.kept.join(', ')}. The new version no longer carries them; remove them here if you want them gone.</div>}
            {diff.filter(([, xs]) => xs.length > 0).map(([label, xs]) => <div key={label}>{label}: <span className="muted">{xs.join(', ')}</span></div>)}
            {diff.every(([, xs]) => xs.length === 0) && <div className="muted">Nothing in the guidance or workflows would change.</div>}
            {cand.newChecks.length > 0 && <div>{cand.newChecks.length} new check{cand.newChecks.length === 1 ? '' : 's'}: <span className="muted">{cand.newChecks.map((c) => c.title || c.expects || c.check).join(', ')}</span></div>}
            <div className="muted">Adopting selects these standards. A conformance review establishes whether the application meets them.</div>
            {cand.problems.length === 0 && (
              <div className="row mt-2" >
                <button className="primary" disabled={busy} onClick={() => adopt(false)} title="the project moves now; the conformance review then says what is outstanding">Adopt now</button>
                {needsWork && <button disabled={busy} onClick={() => adopt(true)} title="an ordinary change carries the target; the project moves when it lands, and its gate must pass the new checks">Adopt through a change</button>}
              </div>
            )}
            {err && <div className="error">{err}</div>}
          </div>
        </Group>
      )}
    </>
  )
}

// A project's way of working is one choice: its packs. Everything they give
// it (guidance, rules, checks, workflows) is shown read-only, one line each,
// with what is in it one click away; changing it means changing the packs.
export function GuidanceTab({ slug }: { slug: string }) {
  const adoptionQ = useGet<AdoptionView>(`/projects/${slug}/adoption`)
  const guidanceQ = useGet<ProjectGuidance>(`/projects/${slug}/guidance`)
  const policiesQ = useGet<ProjectPolicyList>(`/projects/${slug}/policies`)
  const lanesQ = useGet<ProjectLanes>(`/projects/${slug}/lanes`)
  const [changing, setChanging] = useState(false)
  const adoption = adoptionQ.data
  const packs = adoption?.packs ?? []
  // The platform pack every project carries is not a choice; it is not listed.
  const chosen = packs.filter((p) => !p.platform)
  const guidance = (guidanceQ.data?.imports ?? []).filter((im) => !im.disabled)
  const policies = policiesQ.data?.policies ?? []
  const warnings = policiesQ.data?.violations ?? []
  // Every core and extra workflow the project can run; the system ones run themselves.
  const workflows = (lanesQ.data?.lanes ?? []).filter((l) => !l.withdrawn).sort((a, b) => Number(b.tier === 'core') - Number(a.tier === 'core'))

  return (
    <div className="wow-page">
      <Section title="Packs" note={adoption ? recordedLine(adoption) : undefined}
        action={adoption && <button onClick={() => setChanging(!changing)} aria-expanded={changing}>{changing ? 'Close' : 'Change…'}</button>}>
        <ReadStatus q={adoptionQ} what="the way of working" className="small" />
        <StaleNote q={adoptionQ} />
        {adoption?.current && (chosen.length === 0
          ? <p className="muted">No packs chosen yet: choose them with Change….</p>
          : <p>This project uses: <strong>{chosen.map((p) => p.pack).join(' · ')}</strong></p>)}
        {adoption && <AdoptionFacts v={adoption} />}
        {changing && adoption && <AdoptPanel slug={slug} packs={packs} onChanged={() => setChanging(false)} />}
      </Section>

      <Section title="What that gives it">
        <div role="list">
          <GivesRow title="Guidance" q={guidanceQ} what="the guidance"
            summary={`${guidance.length} instruction${guidance.length === 1 ? '' : 's'} its coding agents follow`}
            items={guidance.map((im) => ({ key: im.name, label: im.name, note: im.summary, to: `/guidance/${encodeURIComponent(im.name)}` }))} />
          <GivesRow title="Rules" q={policiesQ} what="the rules"
            summary={policies.length === 0 ? 'No rules' : `${policies.length} polic${policies.length === 1 ? 'y' : 'ies'}${warnings.length ? ` · ${warnings.length} warning${warnings.length === 1 ? '' : 's'}` : ''}`}
            items={[...policies.map((p) => ({ key: p.id ?? p.name, label: p.name, note: p.description || p.rule })), ...warnings.map((v, i) => ({ key: `w${i}`, label: `⚠ ${v.workflow}`, note: v.detail }))]} />
          <ListRow listItem className="card row wow-gives wow-list-row" identity={<>
            <strong>Checks</strong>
            <span className="muted small">Judged when a change lands and in reviews</span>
            </>} action={<span className="row-action"><Link to={`/conformance/projects/${slug}`}>Does it meet them? →</Link></span>} />
          <GivesRow title="Workflows" q={lanesQ} what="the workflows"
            summary={workflows.length <= 3 ? workflows.map((l) => l.name).join(', ') : `${workflows.slice(0, 3).map((l) => l.name).join(', ')} and ${workflows.length - 3} more`}
            items={workflows.map((l) => ({ key: l.name, label: l.name, note: l.summary?.description || l.description, to: `/workflows?open=${encodeURIComponent(l.name)}` }))} />
        </div>
      </Section>
    </div>
  )
}

// GivesRow: one thing the packs give the project, as a count or a short
// list, with the full list opening in place.
function GivesRow({ title, summary, items, q, what }: {
  title: string; summary: string; items: { key: string; label: string; note?: string; to?: string }[]
  q: Gated & { data?: unknown }; what: string
}) {
  return (
    <ListRow listItem className="card wow-gives-item wow-list-row" identity={<>
      <div className="row wow-gives">
        <strong>{title}</strong>
        <span className="muted small">{q.data ? summary : <ReadStatus q={q} what={what} className="small" />}</span>
      </div>
      </>} detail={items.length > 0 && <ul className="wow-gives-list">
        {items.map((it) => <li key={it.key}>
          {it.to ? <Link to={it.to}>{it.label}</Link> : <span>{it.label}</span>}
          {it.note && <span className="muted small"> · {it.note}</span>}
        </li>)}
      </ul>} />
  )
}

// PoliciesCard: the project's rules, read-only; rules come with its packs.
export function PoliciesCard({ slug }: { slug: string }) {
  const q = useGet<ProjectPolicyList>(`/projects/${slug}/policies`)
  const { data } = q
  if (!data) return <ReadStatus q={q} what="the rules" className="small" />
  // An empty answer that may be old says so too: someone may have adopted
  // rules since.
  if (data.policies.length === 0) return <><StaleNote q={q} /><p className="muted">No rules adopted · a pack usually brings some.</p></>
  return <><StaleNote q={q} />{data.policies.map((p) => <ListRow className="card row wow-row wow-list-row" key={p.id} identity={<><strong className="small">{p.name}</strong><span className="muted small wow-note">{p.description || p.rule}</span></>} />)}</>
}
