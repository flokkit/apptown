import { useState } from 'react'
import { Link } from 'react-router-dom'
import { renderMd } from '../md'
import { GuidanceBlock, del, post } from '../api'
import { fmtSize, sizeTitle } from '../format'
import { Status, STATUS_LIGHT } from '../Status'
import { OriginChip } from '../OriginChip'
import { Modal } from '../Modal'
import { QuietConfirm } from '../QuietConfirm'
import { firstErr, useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { GuidanceBlockDetail, LibraryWorkflow, PackPolicy, PackSaved, PolicyLibraryList, PackView } from '../wire.gen'
import { type Draft, draftFrom, keptAsWritten } from './packDraft'

// Ways of working: the pack store. Stock packs are read-only; a pack of
// your own may start as a copy of one. A project takes several packs and gets
// their union, so a pack of your own can also sit beside a stock one. Custom packs are built here
// from the guidance store, the workflow library and the policy library, and
// appear in the new-project wizard automatically.

// A pack as the library lists it (httpapi PackView): the contents of the
// version it recommends, and the declaration its author wrote.


type BlockMeta = { id: string; name: string; tags: string; latestVersion: number; bytes: number }

// Everything a pack would put on a project, in full: guidance block bodies,
// workflow definitions, policy rules.
export type InspectablePack = {
  name: string; description: string; origin?: string
  guidance: string[]; workflows: string[]; policies?: PackPolicy[]
}

export function PackInspect({ pack, onClose }: { pack: InspectablePack; onClose: () => void }) {
  const blocksQ = useGet<BlockMeta[]>('/guidance/blocks', { kind: 'config' })
  const workflowsQ = useGet<LibraryWorkflow[]>('/workflows/library', { kind: 'config' })
  const blocks = blocksQ.data ?? []
  const workflows = Array.isArray(workflowsQ.data) ? workflowsQ.data : []
  const loadErr = firstErr(blocksQ.err, workflowsQ.err)
  const [openBlock, setOpenBlock] = useState<string | null>(null)
  const [openWf, setOpenWf] = useState<string | null>(null)

  const showBlock = (id: string) => setOpenBlock(openBlock === id ? null : id)

  const packWf = pack.workflows.map((n) => workflows.find((w) => w.name === n) ?? { name: n, kind: '?', description: '(not in library)', stages: [], yaml: '', latestVersion: 0 })
  const packPol = pack.policies ?? []
  const packBytes = pack.guidance.reduce((n, name) => n + (blocks.find((x) => x.name === name)?.bytes ?? 0), 0)

  return (
    <Modal onClose={onClose} title={<><h2 style={{ margin: 0 }}>{pack.name}</h2><OriginChip origin={pack.origin} /></>}>
      <div>
        <div className="small" style={{ marginBottom: 12 }}>{pack.description}</div>
        {loadErr && <div className="error small" style={{ marginBottom: 12 }}>The library could not be read, so sizes and details may be missing: {loadErr}</div>}

        <div className="conn-section-head"><h2>Guidance blocks</h2><span className="muted small" title={sizeTitle(packBytes)}>{pack.guidance.length} · {fmtSize(packBytes)} per step</span></div>
        {pack.guidance.map((name) => {
          const b = blocks.find((x) => x.name === name)
          return (
            <div key={name} className="card" style={{ padding: '8px 12px' }}>
              <div className="row" style={{ cursor: b ? 'pointer' : 'default' }} onClick={() => b && showBlock(b.id)}>
                <code>{name}</code>
                {b ? <span className="muted small">v{b.latestVersion} · <span title={sizeTitle(b.bytes)}>{fmtSize(b.bytes)}</span> · {b.tags}</span>
                   : <span className="chip fail">missing from library</span>}
                <span style={{ flex: 1 }} />
                {b && <span className="muted small">{openBlock === b.id ? 'hide' : 'read'}</span>}
              </div>
              {b && openBlock === b.id && <BlockBody id={b.id} />}
            </div>
          )
        })}

        <div className="conn-section-head" style={{ marginTop: 16 }}><h2>Workflows</h2><span className="muted small">{pack.workflows.length}</span></div>
        {packWf.map((w) => (
          <div key={w.name} className="card" style={{ padding: '8px 12px' }}>
            <div className="row" style={{ cursor: 'pointer' }} onClick={() => setOpenWf(openWf === w.name ? null : w.name)}>
              <strong>{w.name}</strong>
              <span className="chip">{w.kind}</span>
              <span className="muted small">{w.stages.join(' → ')}</span>
              <span style={{ flex: 1 }} />
              <span className="muted small">{openWf === w.name ? 'hide' : 'yaml'}</span>
            </div>
            <div className="muted small" style={{ marginTop: 2 }}>{w.description}</div>
            {openWf === w.name && (
              <pre style={{ fontSize: 'var(--fs-xs)', marginTop: 8, maxHeight: 360, overflow: 'auto', borderTop: '1px solid var(--border)', paddingTop: 8 }}>{w.yaml}</pre>
            )}
          </div>
        ))}

        <div className="conn-section-head" style={{ marginTop: 16 }}><h2>Policies</h2><span className="muted small">{(pack.policies ?? []).length}</span></div>
        {packPol.map((pp) => (
          <div key={pp.name} className="card" style={{ padding: '8px 12px' }}>
            <div className="row">
              <strong>{pp.name}</strong>
              <span className="chip">{pp.rule}</span>
              <span className="muted small">{Object.entries(pp.with ?? {}).map(([k, v]) => `${k}=${String(v)}`).join(' · ')}</span>
            </div>
            <div className="muted small" style={{ marginTop: 2 }}>{pp.description}</div>
          </div>
        ))}
      </div>
    </Modal>
  )
}

export default function Packs() {
  const packsQ = useGet<PackView[]>('/packs', { kind: 'config' })
  const packs = packsQ.data ?? []
  const [editing, setEditing] = useState<{ draft: Draft; existing: boolean } | null>(null)
  const [inspecting, setInspecting] = useState<PackView | null>(null)
  const { run, busy, err: actErr } = useAction()
  const [msg, setMsg] = useState('')
  const [showAll, setShowAll] = useState(false)

  const blank = (): Draft => draftFrom('', '', { guidance: [], workflows: [], policies: [] })
  const copyOf = (p: PackView): Draft => draftFrom(`${p.name}-ours`, '', p.declaration)
  const declared = (p: PackView): Draft => draftFrom(p.name, p.description, p.declaration)

  // The instance's switch: an off pack stays here but is not offered when
  // a project is made. Stock packs cannot be deleted; they can be turned off.
  const setOn = (p: PackView, enabled: boolean) => run(() => post(`/packs/${p.name}/enabled`, { enabled }))
  const remove = (p: PackView) => run(() => del(`/packs/${p.name}`))

  // The packs read failed or is on its way: say so, never an empty grid.
  if (!packsQ.data) return <ReadStatus q={packsQ} what="the packs" />

  if (editing) {
    return <PackEditor draft={editing.draft} existing={editing.existing} onDone={(saved) => {
      setEditing(null)
      if (saved) setMsg(`saved ${saved}`)
    }} />
  }

  return (
    <div>
      <div className="row" style={{ marginBottom: 12 }}>
        <span style={{ flex: 1 }} />
        <button className="primary" onClick={() => setEditing({ draft: blank(), existing: false })}>New pack</button>
      </div>
      {msg && <div className="muted small" style={{ marginBottom: 8 }}>{msg}</div>}
      <StaleNote q={packsQ} />
      {actErr && <div className="error">{actErr}</div>}
      {inspecting && <PackInspect pack={inspecting} onClose={() => setInspecting(null)} />}

      <div className="pack-grid">
        {(showAll ? packs : packs.slice(0, 8)).map((p) => (
          <div key={p.name} className="card" style={{ margin: 0, opacity: p.enabled === false ? 0.6 : 1 }}>
            <div className="row">
              <Link to={`/packs/${p.name}`}><strong>{p.name}</strong></Link>
              {p.origin !== 'stock' && <span className="chip">Custom</span>}
              {p.version ? <span className="chip" title="New projects get this version">v{p.version}</span> : null}
              <span className="chip">{p.enabled === false ? 'Hidden' : 'Available'}</span>
            </div>

            {p.requires.some(r => !r.bound) && (
              <div className="small" style={{ marginTop: 10 }}>
                <Status word="waiting" tone={STATUS_LIGHT.waiting} text="Setup needed" /><details><summary>What needs setting up?</summary>Missing: {p.requires.filter(r => !r.bound).map(r => r.capability).join(', ')}</details>
              </div>
            )}
            <div className="row pack-actions" aria-label={`Actions for ${p.name}`}>
              <button className="link" onClick={() => setInspecting(p)}>Contents</button>
              <button className="link" onClick={() => setEditing({ draft: copyOf(p), existing: false })} title="a pack of your own that starts as a copy of this one">Copy</button>
              {p.enabled === false
                ? <button className="link" disabled={busy} onClick={() => void setOn(p, true)}>Make available</button>
                : <button className="link" disabled={busy} onClick={() => void setOn(p, false)}>Hide</button>}
              {p.origin !== 'stock' && (
                <>
                  <button className="link" onClick={() => setEditing({ draft: declared(p), existing: true })}>Edit</button>
                  <QuietConfirm className="link danger" label="Delete" ask={`Delete pack "${p.name}"? Projects that applied it keep the versions they applied.`} onYes={() => void remove(p)} />
                </>
              )}
            </div>
          </div>
        ))}
      </div>
      {!showAll && packs.length > 8 && <button onClick={() => setShowAll(true)}>Show all packs ({packs.length})</button>}
    </div>
  )
}

function PackEditor({ draft, existing, onDone }: { draft: Draft; existing: boolean; onDone: (savedName?: string) => void }) {
  const [p, setP] = useState<Draft>(draft)
  const guidanceQ = useGet<GuidanceBlock[]>('/guidance/blocks', { kind: 'config' })
  const workflowsQ = useGet<LibraryWorkflow[]>('/workflows/library', { kind: 'config' })
  const policiesQ = useGet<PolicyLibraryList>('/policy-library', { kind: 'config' })
  const allGuidance = guidanceQ.data ?? [], allWorkflows = workflowsQ.data ?? [], allPolicies = policiesQ.data?.library ?? []
  const { run, busy, err: actErr } = useAction()
  const err = firstErr(actErr, guidanceQ.err, workflowsQ.err, policiesQ.err)
  const [warnings, setWarnings] = useState<string[]>([])

  const toggle = (list: string[], v: string): string[] =>
    list.includes(v) ? list.filter((x) => x !== v) : [...list, v]

  const save = () => run(async () => {
    const r = await post<PackSaved>('/packs', p)
    if (r.warnings?.length) { setWarnings(r.warnings); return }
    onDone(p.name)
  })

  return (
    <div style={{ maxWidth: 860 }}>
      <h2>{existing ? `Edit pack: ${draft.name}` : 'New pack'}</h2>
      <div className="form-grid" style={{ marginBottom: 12 }}>
        <label>Name</label>
        <input value={p.name} onChange={(e) => setP({ ...p, name: e.target.value })} placeholder="my-way" autoFocus disabled={existing} />
        <label>Description</label>
        <input value={p.description} onChange={(e) => setP({ ...p, description: e.target.value })} />
        <label>Version name</label>
        <input value={p.versionName} onChange={(e) => setP({ ...p, versionName: e.target.value })} placeholder="optional, e.g. stricter deps" />
      </div>
      {existing && <div className="muted small" style={{ marginBottom: 8 }}>Saving creates a version. Recommend it on the pack page.</div>}
      {keptAsWritten(p).length > 0 && <div className="muted small" style={{ marginBottom: 8 }}>Kept as written, not edited here: {keptAsWritten(p).join('; ')}.</div>}

      <h2>Workflows ({p.workflows.length})</h2>
      <div className="row" style={{ gap: 6 }}>
        {allWorkflows.map((w) => (
          <span key={w.name} className={`chip ${p.workflows.includes(w.name) ? 'pass' : ''}`}
            style={{ cursor: 'pointer' }} title={w.description}
            onClick={() => setP({ ...p, workflows: toggle(p.workflows, w.name) })}>
            {p.workflows.includes(w.name) ? '✓ ' : ''}{w.name}
          </span>
        ))}
      </div>

      <h2>Guidance blocks ({p.guidance.length}, {fmtSize(allGuidance.filter((g) => p.guidance.includes(g.name)).reduce((n, g) => n + (g.bytes ?? 0), 0))})</h2>
      <div className="muted small" style={{ marginBottom: 6 }}>
        Selected blocks go to every project agent. Sizes show their context cost.
      </div>
      <div className="row" style={{ gap: 6 }}>
        {allGuidance.map((g) => (
          <span key={g.name} className={`chip ${p.guidance.includes(g.name) ? 'pass' : ''}`}
            style={{ cursor: 'pointer' }} title={`${g.tags ? g.tags + ' · ' : ''}${sizeTitle(g.bytes ?? 0)}`}
            onClick={() => setP({ ...p, guidance: toggle(p.guidance, g.name) })}>
            {p.guidance.includes(g.name) ? '✓ ' : ''}{g.name} <span className="muted">{fmtSize(g.bytes ?? 0)}</span>
          </span>
        ))}
      </div>

      <h2>Policies ({p.policies.length})</h2>
      <div className="muted small" style={{ marginBottom: 6 }}>
        From the policy library (add or clone policies on the Policies tab).
      </div>
      <div className="row" style={{ gap: 6 }}>
        {allPolicies.map((pol) => {
          const on = p.policies.some((x) => x.name === pol.name)
          return (
            <span key={pol.name} className={`chip ${on ? 'pass' : ''}`}
              style={{ cursor: 'pointer' }} title={pol.description}
              onClick={() => setP({
                ...p,
                policies: on
                  ? p.policies.filter((x) => x.name !== pol.name)
                  : [...p.policies, { name: pol.name, description: '', rule: '', with: {} }],
              })}>
              {on ? '✓ ' : ''}{pol.name}
            </span>
          )
        })}
      </div>

      {warnings.length > 0 && (
        <div className="card tone-warn" style={{ marginTop: 12 }}>
          <div className="small">Saved with warnings:</div>
          {warnings.map((w, i) => <div key={i} className="muted small">⚠ {w}</div>)}
          <button className="subtle" style={{ marginTop: 6 }} onClick={() => onDone(p.name)}>OK</button>
        </div>
      )}
      {err && <div className="error">{err}</div>}
      <div className="row" style={{ marginTop: 16 }}>
        <button className="primary" onClick={() => void save()} disabled={busy || !p.name.trim()}>Save pack</button>
        <button onClick={() => onDone()}>Cancel</button>
      </div>
    </div>
  )
}

// BlockBody reads one guidance block's body through the shared read, so
// opening it again, or elsewhere, asks once.
function BlockBody({ id }: { id: string }) {
  const q = useGet<GuidanceBlockDetail>(`/guidance/blocks/${id}`, { kind: 'config' })
  if (!q.data) return <ReadStatus q={q} what="the block" />
  return (
    <>
      <StaleNote q={q} />
      <div className="md-doc" style={{ marginTop: 8, borderTop: '1px solid var(--border)', paddingTop: 8 }}
        dangerouslySetInnerHTML={{ __html: renderMd(q.data.block.bodyMd || '(empty)') }} />
    </>
  )
}
