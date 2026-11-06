import { Link } from 'react-router-dom'
import './Packs.css'
import { LibraryRow, LibraryRows, firstSentence } from '../LibraryRow'
import { Section } from '../Section'
import { MoreMenu } from '../MoreMenu'
import { useState } from 'react'
import { GuidanceBlock, post } from '../api'
import { fmtSize, sizeTitle } from '../format'
import { Status } from '../Status'
import { OriginChip } from '../OriginChip'
import { Modal } from '../Modal'
import { firstErr, useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { LibraryWorkflow, PackPolicy, PackSaved, PolicyLibraryList, PackView } from '../wire.gen'
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
  const packWf = pack.workflows.map((n) => workflows.find((w) => w.name === n) ?? { name: n, kind: '?', description: '(not in library)', stages: [], yaml: '', latestVersion: 0 })
  const packPol = pack.policies ?? []
  const packBytes = pack.guidance.reduce((n, name) => n + (blocks.find((x) => x.name === name)?.bytes ?? 0), 0)

  const body = (
      <div>
        <div className="small mb-3" >{pack.description}</div>
        {loadErr && <div className="error small mb-3" >The library could not be read, so sizes and details may be missing: {loadErr}</div>}

        <div className="conn-section-head"><h2>Guidance blocks</h2><span className="muted small" title={sizeTitle(packBytes)}>{pack.guidance.length} · {fmtSize(packBytes)} per step</span></div>
        {pack.guidance.map((name) => {
          const b = blocks.find((x) => x.name === name)
          return (
            <div key={name} className="card packs-entry">
              <div className="row">
                <code>{name}</code>
                {b ? <span className="muted small">v{b.latestVersion} · <span title={sizeTitle(b.bytes)}>{fmtSize(b.bytes)}</span> · {b.tags}</span>
                   : <Status word="failed" help="This adopted pack is missing from the library. Ask an administrator to restore the pack or change the project’s adoption." />}
                <span className="grow" />
                {b && <Link to={`/guidance/${b.id}`}>Read</Link>}
              </div>
            </div>
          )
        })}

        <div className="conn-section-head mt-4" ><h2>Workflows</h2><span className="muted small">{pack.workflows.length}</span></div>
        {packWf.map((w) => (
          <div key={w.name} className="card packs-entry">
            <div className="row packs-entry-action">
              <strong>{w.name}</strong>
              <span className="badge">{w.kind}</span>
              <span className="muted small">{w.stages.join(' → ')}</span>
              <span className="grow" />
              <Link to={`/workflows/${encodeURIComponent(w.name)}`}>Definition</Link>
            </div>
            <div className="muted small packs-entry-note">{w.description}</div>
          </div>
        ))}

        <div className="conn-section-head mt-4" ><h2>Policies</h2><span className="muted small">{(pack.policies ?? []).length}</span></div>
        {packPol.map((pp) => (
          <div key={pp.name} className="card packs-entry">
            <div className="row">
              <strong>{pp.name}</strong>
              <span className="badge">{pp.rule}</span>
              <span className="muted small">{Object.entries(pp.with ?? {}).map(([k, v]) => `${k}=${String(v)}`).join(' · ')}</span>
            </div>
            <div className="muted small packs-entry-note">{pp.description}</div>
          </div>
        ))}
      </div>
  )
  return (
    <Modal onClose={onClose} title={<><h2 className="packs-title">{pack.name}</h2><OriginChip origin={pack.origin} /></>}>{body}</Modal>
  )
}

export default function Packs() {
  const packsQ = useGet<PackView[]>('/packs', { kind: 'config' })
  const packs = packsQ.data ?? []
  const [editing, setEditing] = useState<{ draft: Draft; existing: boolean } | null>(null)
  const [msg, setMsg] = useState('')

  const blank = (): Draft => draftFrom('', '', { guidance: [], workflows: [], policies: [] })
  const copyOf = (p: PackView): Draft => draftFrom(`${p.name}-ours`, '', p.declaration)

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
      {msg && <div className="muted small mb-2" >{msg}</div>}
      <StaleNote q={packsQ} />

      <Section title="Packs" count={packs.length} action={<button className="primary" onClick={() => setEditing({ draft: blank(), existing: false })}>New pack</button>}>
      <LibraryRows>
        {packs.map((p) => {
          const missing = p.requires.filter(r => !r.bound).map(r => r.capability)
          return (
          // A pack is too large to open in place: the row goes to its page,
          // which holds its actions. Copy starts a new pack from this list.
          <LibraryRow key={p.name} name={p.name} to={`/packs/${p.name}`}
            // What is unusual about a pack sits with its name; the normal case says nothing.
            badges={<>
              {experimental(p) && <span className="badge">experimental</span>}
              {p.origin !== 'stock' && <span className="badge">yours</span>}
              {p.enabled === false && <span className="badge">hidden</span>}
            </>}
            purpose={purpose(p)}
            state={missing.length > 0 && <Status word="not set up" help={`Missing: ${missing.join(', ')}`} />}
            menu={<MoreMenu label={p.name} items={[
                { label: 'Copy', hint: 'Make a pack of your own from this one.', onClick: () => setEditing({ draft: copyOf(p), existing: false }) },
            ]} />}
            >
          </LibraryRow>
          )
        })}
      </LibraryRows>
      </Section>
    </div>
  )
}

export function PackEditor({ draft, existing, onDone }: { draft: Draft; existing: boolean; onDone: (savedName?: string) => void }) {
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
    <div className="packs-editor">
      <h2>{existing ? `Edit pack: ${draft.name}` : 'New pack'}</h2>
      <div className="form-grid mb-3" >
        <label>Name</label>
        <input aria-label="Name" value={p.name} onChange={(e) => setP({ ...p, name: e.target.value })} placeholder="my-way" autoFocus disabled={existing} />
        <label>Description</label>
        <input aria-label="Description" value={p.description} onChange={(e) => setP({ ...p, description: e.target.value })} />
        <label>Version name</label>
        <input aria-label="Version name" value={p.versionName} onChange={(e) => setP({ ...p, versionName: e.target.value })} placeholder="optional, e.g. stricter deps" />
      </div>
      {existing && <div className="muted small mb-2" >Saving creates a version. Recommend it on the pack page.</div>}
      {keptAsWritten(p).length > 0 && <div className="muted small mb-2" >Kept as written, not edited here: {keptAsWritten(p).join('; ')}.</div>}

      <h2>Workflows ({p.workflows.length})</h2>
      <div className="row packs-editor-actions">
        {allWorkflows.map((w) => (
          <span key={w.name} className="badge packs-entry-action"
             title={w.description}
            onClick={() => setP({ ...p, workflows: toggle(p.workflows, w.name) })}>
            {p.workflows.includes(w.name) ? '✓ ' : ''}{w.name}
          </span>
        ))}
      </div>

      <h2>Guidance blocks ({p.guidance.length}, {fmtSize(allGuidance.filter((g) => p.guidance.includes(g.name)).reduce((n, g) => n + (g.bytes ?? 0), 0))})</h2>
      <div className="muted small packs-editor-label">
        Selected blocks go to every project agent. Sizes show their context cost.
      </div>
      <div className="row packs-editor-actions">
        {allGuidance.map((g) => (
          <span key={g.name} className="badge packs-entry-action"
             title={`${g.tags ? g.tags + ' · ' : ''}${sizeTitle(g.bytes ?? 0)}`}
            onClick={() => setP({ ...p, guidance: toggle(p.guidance, g.name) })}>
            {p.guidance.includes(g.name) ? '✓ ' : ''}{g.name} <span className="muted">{fmtSize(g.bytes ?? 0)}</span>
          </span>
        ))}
      </div>

      <h2>Policies ({p.policies.length})</h2>
      <div className="muted small packs-editor-label">
        From the policy library (add or clone policies on the Policies tab).
      </div>
      <div className="row packs-editor-actions">
        {allPolicies.map((pol) => {
          const on = p.policies.some((x) => x.name === pol.name)
          return (
            <span key={pol.name} className="badge packs-entry-action"
               title={pol.description}
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
        <div className="card tone-warn mt-3" >
          <div className="small">Saved with warnings:</div>
          {warnings.map((w, i) => <div key={i} className="muted small">⚠ {w}</div>)}
          <button className="subtle packs-add"  onClick={() => onDone(p.name)}>OK</button>
        </div>
      )}
      {err && <div className="error">{err}</div>}
      <div className="row mt-4" >
        <button className="primary" onClick={() => void save()} disabled={busy || !p.name.trim()}>Save pack</button>
        <button onClick={() => onDone()}>Cancel</button>
      </div>
    </div>
  )
}

const experimental = (p: { description?: string }) => /^experimental\b/i.test((p.description ?? '').trim())

// purpose is the first sentence, without a leading "Experimental": the chip
// beside the name already says it.
function purpose(p: { description?: string }): string {
  const first = firstSentence(p.description)
  return experimental(p) ? first.replace(/^experimental[:\s]+/i, '').replace(/^./, (c) => c.toUpperCase()) : first
}
