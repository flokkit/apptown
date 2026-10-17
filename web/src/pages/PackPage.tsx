import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { post } from '../api'
import { OriginChip } from '../OriginChip'
import { QuietConfirm } from '../QuietConfirm'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { relTime } from '../format'
import type { PackHistory, PackSpec, Publication } from '../wire.gen'
import { WhatIfPanel } from './PackWhatIf'

// One pack: what its author
// wrote, every version with where it came from, and which version is
// recommended — what new projects get and what projects on other versions
// are offered. Moving the recommendation is a person's act; it moves no
// project.

export default function PackPage() {
  const { name = '' } = useParams()
  const q = useGet<PackHistory>(`/packs/${name}`, { kind: 'config' })
  const { run, busy, err: actErr } = useAction(q.reload)
  const [open, setOpen] = useState<number | null>(null)
  const h = q.data
  if (!h) return <ReadStatus q={q} what="the pack" />
  const recommend = (version: number) => run(() => post(`/packs/${name}/recommended`, { version }))
  const shown = open ?? h.recommended

  return (
    <div style={{ maxWidth: 900 }}>
      <div className="muted small"><Link to="/packs">Packs</Link></div>
      <div className="row" style={{ flexWrap: 'wrap' }}>
        <h1 style={{ marginBottom: 0 }}>{h.name}</h1>
        <OriginChip origin={h.origin} />
        <span className="chip pass" title="New projects get this version; projects on other versions are offered it">recommended v{h.recommended}</span>
        {h.latest !== h.recommended && <span className="chip" title="The newest version is not recommended yet">newest v{h.latest}</span>}
      </div>
      <p className="small" style={{ margin: '6px 0 12px' }}>{h.description}</p>
      <StaleNote q={q} />
      {actErr && <div className="error">{actErr}</div>}
      {h.followRefused && (
        <div className="card tone-warn small" style={{ marginBottom: 12 }}>
          The included pack moved, but this pack could not follow it: {h.followRefused}. Edit the pack to settle it.
        </div>
      )}

      <h2>What it says</h2>
      <Declaration d={h.declaration} />

      <h2 style={{ marginTop: 20 }}>Versions</h2>
      <div className="muted small" style={{ marginBottom: 8 }}>
        Recommending a version changes what new projects get and what the others are offered. No project moves until its owner adopts it.
      </div>
      {h.versions.map((v) => (
        <div key={v.version} className={`card ${v.version === h.recommended ? 'tone-ok' : ''}`} style={{ padding: '8px 12px' }}>
          <div className="row" style={{ flexWrap: 'wrap', gap: 6 }}>
            <strong>v{v.version}</strong>
            {v.name && <span>“{v.name}”</span>}
            {v.version === h.recommended && <span className="chip pass">recommended</span>}
            <span className="muted small">{lineage(v)}</span>
            <span style={{ flex: 1 }} />
            <span className="muted small" title={v.at}>{relTime(v.at)}</span>
            <button className="subtle" onClick={() => setOpen(shown === v.version ? -1 : v.version)}>{shown === v.version ? 'hide' : 'contents'}</button>
            {v.version !== h.recommended && (
              <QuietConfirm label="Recommend" className="subtle" yes="Recommend it" disabled={busy}
                ask={`New projects will get v${v.version}, and projects on other versions will be offered it. No project moves.`}
                onYes={() => recommend(v.version)} />
            )}
          </div>
          {v.note && v.note !== 'published contents' && v.note !== v.name && <div className="small" style={{ marginTop: 4 }}>{v.note}</div>}
          {shown === v.version && <Contents v={v} />}
          {shown === v.version && <Limits v={v} base={h.versions.find((b) => b.version === v.basedOn)} />}
          {shown === v.version && <WhatIfPanel pack={h.name} version={v.version} />}
        </div>
      ))}
    </div>
  )
}

function lineage(v: Publication): string {
  const parts: string[] = []
  if (v.basedOn) parts.push(`from v${v.basedOn}`)
  if (v.includedPack) parts.push(`includes ${v.includedPack} v${v.includedVersion}`)
  return parts.join(' · ')
}

// Declaration is the author's words: the pack it includes and what it
// changes about it, then its own items.
function Declaration({ d }: { d: PackSpec }) {
  const replaces = Object.entries(d.replaces ?? {})
  const own = [...d.guidance.map((g) => `guidance ${g}`), ...d.workflows.map((w) => `workflow ${w}`), ...d.policies.map((p) => `policy ${p.name}`)]
  return (
    <div className="card small" style={{ padding: '8px 12px' }}>
      {d.includes
        ? <div>Includes <Link to={`/packs/${d.includes.split('@')[0]}`}><code>{d.includes.split('@')[0]}</code></Link>{d.includes.includes('@') ? `, pinned at v${d.includes.split('@')[1]}` : ', following its recommended version'}.</div>
        : <div>Includes no other pack.</div>}
      {replaces.map(([from, to]) => <div key={from}>Replaces <code>{from}</code> with <code>{to}</code></div>)}
      {(d.drops ?? []).map((x) => <div key={x.item}>Drops <code>{x.item}</code>{x.reason ? ` — ${x.reason}` : ''}</div>)}
      {(d.clears ?? []).map((c) => <div key={c}>Sets no {c}</div>)}
      <div style={{ marginTop: 4 }}>{own.length > 0 ? <>Adds: {own.map((o, i) => <span key={o}>{i > 0 ? ', ' : ''}<code>{o}</code></span>)}</> : 'Adds nothing of its own.'}</div>
      {d.toolchain && <div>Toolchain: <code>{d.toolchain}</code></div>}
    </div>
  )
}

// Contents is what a version installs: the resolved items at their pins.
function Contents({ v }: { v: Publication }) {
  const s = v.spec
  return (
    <div className="small" style={{ marginTop: 8, borderTop: '1px solid var(--border)', paddingTop: 8 }}>
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
    <div className="small muted" style={{ marginTop: 8, borderTop: '1px solid var(--border)', paddingTop: 8 }}>
      <strong>When a project adopts it.</strong>{' '}
      {kept.length > 0
        ? <>Projects on v{base?.version} keep {kept.map((k, i) => <span key={k}>{i > 0 ? ', ' : ''}<code>{k}</code></span>)} after adopting, though this version no longer carries {kept.length === 1 ? 'it' : 'them'}; removing {kept.length === 1 ? 'it' : 'them'} is an edit on each project.</>
        : <>A project keeps any workflow or policy it already has that this version does not carry; removing one is an edit on the project.</>}
      {' '}Policies are named: a project gets the policy library's text when it installs this version, so two projects can hold different text for one name.
    </div>
  )
}
