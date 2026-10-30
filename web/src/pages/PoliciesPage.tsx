import { useState } from 'react'
import { Link } from 'react-router-dom'
import { del, post } from '../api'
import { OriginChip } from '../OriginChip'
import { QuietConfirm } from '../QuietConfirm'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { PolicyLibraryEntry, PolicyLibraryList, PolicyUsageList, ProjectRow } from '../wire.gen'

// The policy LIBRARY: instance-level, named policy definitions — the place
// you manage your rules, same store/pin pattern as guidance and workflows.
// Packs reference these by name; projects adopt them (copying the
// definition, origin recorded). Stock entries re-sync with the release —
// clone to customize. Per-project conformance lives on each project's
// Workflows tab; here each entry just shows who adopted it and whether
// they're currently in line.


const RULES: { rule: string; label: string; params: string[] }[] = [
  { rule: 'requireStepBefore', label: 'step X requires step Y in an earlier stage', params: ['uses', 'requires'] },
  { rule: 'requireCapabilityBound', label: 'capability must have a binding', params: ['capability'] },
  { rule: 'scanFailOnCeiling', label: 'scan failOn may not be looser than…', params: ['max'] },
]

export default function PoliciesPage() {
  const libQ = useGet<PolicyLibraryList>('/policy-library', { kind: 'config' })
  const projectsQ = useGet<ProjectRow[]>('/projects')
  const lib = libQ.data ? libQ.data.library ?? [] : null
  const [editing, setEditing] = useState<PolicyLibraryEntry | null>(null)
  // A rule's adoption counts come with the projects read, so its acts
  // refresh that too.
  const { run, busy, err: actErr } = useAction(undefined, { affects: ['/projects'] })

  const blank = (): PolicyLibraryEntry => ({ name: '', description: '', rule: 'requireStepBefore', with: {}, origin: '', adoptedBy: 0, nonConformant: 0, packs: [], reading: '', fix: '' })
  const clone = (p: PolicyLibraryEntry): PolicyLibraryEntry => ({ ...structuredClone(p), name: `${p.name}-custom`, origin: '' })

  const remove = (p: PolicyLibraryEntry) => run(() => del(`/policy-library/${p.name}`))

  // The project count says how many follow each rule, so a projects read
  // that failed is not shown as none.
  if (!lib || !projectsQ.data) return <ReadStatus q={[libQ, projectsQ]} what="the policy library" />
  const totalProjects = projectsQ.data.length

  if (editing) {
    return <PolicyEditor policy={editing} onDone={() => setEditing(null)} />
  }

  return (
    <div>
      <StaleNote q={[libQ, projectsQ]} />
      {actErr && <div className="error">{actErr}</div>}
      <div className="row" style={{ marginBottom: 12 }}>
        <span className="muted small"></span>
        <span style={{ flex: 1 }} />
        <button className="primary" onClick={() => setEditing(blank())}>New policy</button>
      </div>
      {lib.map((p) => <PolicyCard key={p.name} p={p} totalProjects={totalProjects} onClone={() => setEditing(clone(p))}
        onEdit={() => setEditing(structuredClone(p))} onRemove={() => { if (!busy) void remove(p) }} />)}
    </div>
  )
}

// The three layers of a rule (design/decisions/policy-v2.md): the SENTENCE leads,
// big — a rule you can say in one sentence. Open the card for the precise
// reading, the fix, and standing: who follows the rule right now, and for
// anyone who doesn't, exactly why and what to do about it.
function PolicyCard({ p, totalProjects, onClone, onEdit, onRemove }: {
  p: PolicyLibraryEntry; totalProjects: number; onClone: () => void; onEdit: () => void; onRemove: () => void
}) {
  const [open, setOpen] = useState(false)
  // Exceptions only: the per-project fetch exists to NAME violators, so
  // it only happens when there are any (it does not list everyone).
  const usageQ = useGet<PolicyUsageList>(open && p.nonConformant > 0 ? `/policy-library/${p.name}/usage` : null)
  const usage = usageQ.data ? usageQ.data.usedBy ?? [] : null
  const usageErr = usageQ.err
  const scope = p.adoptedBy >= totalProjects && totalProjects > 0 ? 'all projects'
    : `${p.adoptedBy} project${p.adoptedBy === 1 ? '' : 's'} (${p.packs.length > 0 ? 'through packs' : 'adopted directly'})`
  return (
    <div className="card">
      <div className="row" style={{ cursor: 'pointer' }} onClick={() => setOpen(!open)}>
        <strong style={{ fontSize: 'var(--fs-base)' }}>{p.description || p.name}</strong>
        <span style={{ flex: 1 }} />
        {p.adoptedBy > 0 && (
          <span className={`chip ${p.nonConformant > 0 ? 'pending' : 'pass'}`}>
            {p.nonConformant > 0 ? `${p.nonConformant} not following it` : `everyone follows it (${p.adoptedBy})`}
          </span>
        )}
        {p.packs.length === 0 && p.adoptedBy === 0 && <span className="chip">unused</span>}
        <span className="muted small">{open ? '▴' : '▾'}</span>
      </div>
      {open && (
        <div style={{ marginTop: 10 }}>
          <div className="muted small">{p.name} · <OriginChip origin={p.origin} />
            {p.packs.length > 0 && <> · in packs: {p.packs.join(', ')}</>}</div>
          <div style={{ margin: '8px 0' }}>
            <div className="muted small" style={{ marginBottom: 2 }}>The rule</div>
            {p.reading}
          </div>
          {p.fix && (
            <div style={{ margin: '8px 0' }}>
              <div className="muted small" style={{ marginBottom: 2 }}>If it blocks you</div>
              {p.fix}
            </div>
          )}
          <details style={{ margin: '8px 0' }}>
            <summary className="muted small" style={{ cursor: 'pointer' }}>definition</summary>
            <pre style={{ margin: '6px 0 0', padding: 8, background: 'var(--code-bg)', borderRadius: 'var(--radius-sm)', overflowX: 'auto' }}>
{['rule: ' + p.rule,
  ...(Object.keys(p.with ?? {}).length
    ? ['with:', ...Object.entries(p.with).map(([k, v]) => `  ${k}: ${JSON.stringify(v)}`)]
    : ['with: {}'])].join('\n')}
            </pre>
          </details>
          {p.adoptedBy > 0 && (
            <div style={{ margin: '8px 0' }}>
              <div className="muted small" style={{ marginBottom: 4 }}>Who it applies to</div>
              {p.nonConformant === 0
                ? <div>✓ {scope}</div>
                : <>
                    <div>applies to {scope} — {p.nonConformant} not following it:</div>
                    {usageErr ? <div className="error small">Could not list which projects: {usageErr}</div>
                      : usage === null && <div className="muted small">checking…</div>}
                    {usage?.filter((u) => !u.conformant).map((u) => (
                      <div key={u.slug} style={{ padding: '3px 0' }}>
                        ✗ <Link to={`/projects/${u.slug}`}>{u.project}</Link>
                        {u.violations.map((v, i) => (
                          <div key={i} className="small" style={{ marginLeft: 18, color: 'var(--bad)' }}>
                            workflow “{v.workflow}” {v.detail} — <span className="muted">{p.fix}</span>
                          </div>
                        ))}
                      </div>
                    ))}
                  </>}
            </div>
          )}
          <div className="row" style={{ marginTop: 8 }}>
            <button className="subtle" onClick={onClone}>Clone…</button>
            {p.origin !== 'stock' && (
              <>
                <button className="subtle" onClick={onEdit}>Edit</button>
                <QuietConfirm label="Delete" ask={`Delete "${p.name}" from the library? Projects that adopted it keep their copies.`} onYes={onRemove} />
              </>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function PolicyEditor({ policy, onDone }: { policy: PolicyLibraryEntry; onDone: (saved?: boolean) => void }) {
  const [p, setP] = useState(policy)
  const { run, busy, err } = useAction(() => onDone(true), { affects: ['/projects'] })
  const form = RULES.find((r) => r.rule === p.rule) ?? RULES[0]

  const save = () => run(() => post('/policy-library', { name: p.name, description: p.description, rule: p.rule, with: p.with }))

  return (
    <div style={{ maxWidth: 640 }}>
      <h2>{policy.name && policy.origin === '' && policy.name === p.name ? `Edit: ${policy.name}` : 'New policy'}</h2>
      <div className="form-grid" style={{ marginBottom: 12 }}>
        <label>Name</label>
        <input value={p.name} onChange={(e) => setP({ ...p, name: e.target.value })} placeholder="my-rule" autoFocus />
        <label>Description</label>
        <input value={p.description} onChange={(e) => setP({ ...p, description: e.target.value })} />
        <label>Rule</label>
        <select value={p.rule} onChange={(e) => setP({ ...p, rule: e.target.value, with: {} })}>
          {RULES.map((r) => <option key={r.rule} value={r.rule}>{r.label}</option>)}
        </select>
        {form.params.map((param) => (
          <span key={param} style={{ display: 'contents' }}>
            <label>{param}</label>
            <input value={String(p.with?.[param] ?? '')}
              onChange={(e) => setP({ ...p, with: { ...p.with, [param]: e.target.value } })} />
          </span>
        ))}
      </div>
      {err && <div className="error">{err}</div>}
      <div className="row">
        <button className="primary" onClick={() => void save()} disabled={busy || !p.name.trim()}>Save</button>
        <button onClick={() => onDone()}>Cancel</button>
      </div>
    </div>
  )
}
