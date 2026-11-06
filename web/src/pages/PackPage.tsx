import { useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { del, post } from '../api'
import { Section } from '../Section'
import { QuietConfirm } from '../QuietConfirm'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { relTime } from '../format'
import type { PackHistory, Publication, PackView, WhatIf } from '../wire.gen'
import { PackEditor } from './Packs'
import { draftFrom } from './packDraft'
import { useHolds } from '../instancePages'
import { WhatIfPanel } from './PackWhatIf'

// One pack: what its author
// wrote, every version with where it came from, and which version is
// recommended — what new projects get and what projects on other versions
// are offered. Moving the recommendation is a person's act; it moves no
// project.

export default function PackPage() {
  const { name = '' } = useParams()
  const nav = useNavigate()
  const [params] = useSearchParams()
  const whatIfId = params.get('whatIf')
  const whatIfQ = useGet<WhatIf>(whatIfId ? `/conformance/what-if/${whatIfId}` : null)
  const packsQ = useGet<PackView[]>('/packs', { kind: 'config' })
  const canManage = useHolds('standards:manage-packs')
  const [editing, setEditing] = useState(false)
  const q = useGet<PackHistory>(`/packs/${name}`, { kind: 'config' })
  const { run, busy, err: actErr } = useAction(q.reload)
  const [open, setOpen] = useState<number | null>(null)
  const h = q.data
  if (!h) return <ReadStatus q={q} what="the pack" />
  const recommend = (version: number) => run(() => post(`/packs/${name}/recommended`, { version, ...(whatIfQ.data?.pack === name && whatIfQ.data.version === version ? { whatIf: whatIfQ.data.id } : {}) }))
  const enabled = packsQ.data?.find(p => p.name === name)?.enabled
  const d = h.declaration
  if (editing) return <PackEditor draft={draftFrom(h.name, h.description, d)} existing onDone={() => { setEditing(false); void q.reload() }} />

  return (
    <div>
      <div className="page-area"><Link to="/packs">Packs</Link></div>
      <div className="row" style={{ flexWrap: 'wrap', gap: 8 }}>
        <h1 style={{ margin: 0 }}>{h.name}</h1>
        {/* The same tags as the list: only what is unusual, and the version new projects get. */}
        {/^experimental\b/i.test(h.description.trim()) && <span className="badge">experimental</span>}
        {h.origin !== 'stock' && <span className="badge">yours</span>}
        <span className="badge" title="New projects get this version; projects on other versions are offered it">v{h.recommended} · recommended</span>
        {h.latest !== h.recommended && <span className="badge" title="The newest version is not recommended yet">v{h.latest} available</span>}
      </div>
      {h.description && <p className="muted">{h.description}</p>}
      {d.toolchain && <p className="small">Toolchain: <code>{d.toolchain}</code></p>}
      <StaleNote q={[q, packsQ]} />
      <ReadStatus q={packsQ} what="pack availability" className="small" />
      {whatIfId && <ReadStatus q={whatIfQ} what="the assessed what-if" className="small" />}
      {actErr && <div className="error">{actErr}</div>}

      {canManage && <div className="row mb-3">
        <button disabled={busy || !packsQ.current || enabled === undefined} onClick={() => void run(() => post(`/packs/${name}/enabled`, { enabled: enabled === false }))}>{enabled === false ? 'Make available' : 'Hide'}</button>
        {h.origin !== 'stock' && <>
          <button disabled={busy || !q.current} onClick={() => setEditing(true)}>Edit</button>
          <QuietConfirm label="Delete…" disabled={busy || !q.current} ask={`Delete pack “${name}”? Projects keep the versions they applied.`}
            onYes={() => run(async () => { await del(`/packs/${name}`); nav('/packs') })} />
        </>}
      </div>}
      {/* An empty part says nothing: the count line above the parts is enough. */}
      {d.guidance.length > 0 && <Section title="Guidance" count={d.guidance.length}>
        {d.guidance.map((g) => (
          <div key={g} className="card row"><span>{g.split('/').at(-1)!.replaceAll('-', ' ')}</span><span className="muted small">{g}</span></div>
        ))}
      </Section>}
      {d.workflows.length > 0 && <Section title="Workflows" count={d.workflows.length}>
        {d.workflows.map((w) => (
          <div key={w} className="card row"><span>{w}</span></div>
        ))}
      </Section>}
      {d.policies.length > 0 && <Section title="Policies" count={d.policies.length}>
        {d.policies.map((pp) => (
          <div key={pp.name} className="card row" style={{ flexWrap: 'wrap' }}>
            <span>{pp.description || pp.name}</span>
            {pp.description && <span className="muted small">{pp.name}</span>}
          </div>
        ))}
      </Section>}

      <Section title="Versions" count={h.versions.length} note="The recommended version is what new projects get. Existing projects adopt separately.">
        {h.versions.map((v) => (
          <div key={v.version} className="card">
            <div className="row" style={{ flexWrap: 'wrap', gap: 8 }}>
              <strong>v{v.version}</strong>
              {v.name && <span>“{v.name}”</span>}
              {v.version === h.recommended && <span className="badge">recommended</span>}
              <span className="muted small">{lineage(v)}</span>
              <span className="grow" />
              <span className="muted small" title={v.at}>{relTime(v.at)}</span>
              <button className="subtle" aria-expanded={open === v.version} onClick={() => setOpen(open === v.version ? null : v.version)}>{open === v.version ? 'Hide contents' : 'Contents'}</button>
              {canManage && v.version !== h.recommended && (
                <QuietConfirm label={`Recommend v${v.version}`} className="subtle" yes="Recommend it" disabled={busy || !q.current || !!whatIfId && (!whatIfQ.current || whatIfQ.data?.pack !== name)}
                  ask={`New projects will get v${v.version}, and projects on other versions will be offered it. No project moves.`}
                  onYes={() => recommend(v.version)} />
              )}
            </div>
            {v.note && v.note !== 'published contents' && v.note !== v.name && <div className="small mt-1" >{v.note}</div>}
            {open === v.version && <Contents v={v} />}
            {open === v.version && <Limits v={v} base={h.versions.find((b) => b.version === v.basedOn)} />}
            {open === v.version && <WhatIfPanel pack={h.name} version={v.version} />}
          </div>
        ))}
      </Section>
    </div>
  )
}

function lineage(v: Publication): string {
  const parts: string[] = []
  if (v.basedOn) parts.push(`from v${v.basedOn}`)
  return parts.join(' · ')
}

// Contents is what a version installs: the resolved items at their pins.
function Contents({ v }: { v: Publication }) {
  const s = v.spec
  return (
    <div className="small mt-2" style={{ borderTop: '1px solid var(--border)', paddingTop: 8 }}>
      <div><strong>Guidance</strong> ({s.guidance.length}): {s.guidance.map((g, i) => <span key={g}>{i > 0 ? ', ' : ''}<code>{g}</code>{v.pins.blocks[g] ? ` v${v.pins.blocks[g]}` : ''}</span>)}</div>
      <div><strong>Workflows</strong> ({s.workflows.length}): {s.workflows.map((w, i) => <span key={w}>{i > 0 ? ', ' : ''}<code>{w}</code>{v.pins.workflows[w] ? ` v${v.pins.workflows[w]}` : ''}</span>)}</div>
      <div><strong>Policies</strong> ({s.policies.length}): {s.policies.map((p, i) => <span key={p.name}>{i > 0 ? ', ' : ''}<code>{p.name}</code></span>)}</div>
      {s.toolchain && <div><strong>Toolchain</strong>: <code>{s.toolchain}</code></div>}
      {(s.extensions ?? []).length > 0 && <div><strong>Extensions</strong>: {(s.extensions ?? []).join(', ')}</div>}
    </div>
  )
}

// Limits are what adopting a version does not do, stated where a person
// decides whether to recommend it: a project keeps the workflows and
// policies it has, and a named policy is the library's text when it is
// installed.
function Limits({ v, base }: { v: Publication; base?: Publication }) {
  const kept = base ? [
    ...base.spec.workflows.filter((w) => !v.spec.workflows.includes(w)).map((w) => `workflow ${w}`),
    ...base.spec.policies.filter((p) => !v.spec.policies.some((q) => q.name === p.name)).map((p) => `policy ${p.name}`),
  ] : []
  return (
    <div className="small muted mt-2" style={{ borderTop: '1px solid var(--border)', paddingTop: 8 }}>
      <strong>When a project adopts it.</strong>{' '}
      {kept.length > 0
        ? <>Projects on v{base?.version} keep {kept.map((k, i) => <span key={k}>{i > 0 ? ', ' : ''}<code>{k}</code></span>)} after adopting, though this version no longer carries {kept.length === 1 ? 'it' : 'them'}; removing {kept.length === 1 ? 'it' : 'them'} is an edit on each project.</>
        : <>Adoption keeps existing workflows and policies. Remove them in project settings.</>}
      {' '}Policies are named: a project gets the policy library's text when it installs this version, so two projects can hold different text for one name.
    </div>
  )
}
