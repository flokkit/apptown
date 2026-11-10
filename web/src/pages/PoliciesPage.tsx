import { useParams } from 'react-router-dom'
import { Ident } from '../Ident'
import { ProjectCount } from './DependentProjects'
import './PoliciesPage.css'
import { Fragment, useState } from 'react'
import { del, post } from '../api'
import { OriginChip } from '../OriginChip'
import { Section } from '../Section'
import { LibraryRow, LibraryRows } from '../LibraryRow'
import { MoreMenu } from '../MoreMenu'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { PolicyLibraryEntry, PolicyLibraryList, ProjectRow } from '../wire.gen'

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

export default function PoliciesPage({ page = false }: { page?: boolean }) {
  const { name } = useParams()
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
    return <>{page && <h1>Edit {name}</h1>}<PolicyEditor policy={editing} onDone={() => setEditing(null)} /></>
  }

  return (
    <div>
      {page && <h1>{name}</h1>}
      <StaleNote q={[libQ, projectsQ]} />
      {actErr && <div className="error">{actErr}</div>}
      {page && !lib.some(p => p.name === name) && <p>This policy is no longer in the library.</p>}
      <Section title={page ? '' : 'Policies'} count={page ? undefined : lib.length} action={!page && <button className="primary" onClick={() => setEditing(blank())}>New policy</button>}>
        {lib.length === 0 ? <p className="muted">No policies yet.</p> : <LibraryRows>{lib.filter(p => !page || p.name === name).map((p) => <PolicyCard page={page} key={p.name} p={p} totalProjects={totalProjects} onClone={() => setEditing(clone(p))}
          onEdit={() => setEditing(structuredClone(p))} onRemove={() => { if (!busy) void remove(p) }} />)}</LibraryRows>}
      </Section>
    </div>
  )
}

// The three layers of a rule (design/decisions/policy-v2.md): the SENTENCE leads,
// big — a rule you can say in one sentence. Open the card for the precise
// reading, the fix, and standing: who follows the rule right now, and for
// anyone who doesn't, exactly why and what to do about it.
function PolicyCard({ page, p, totalProjects, onClone, onEdit, onRemove }: {
  page: boolean; p: PolicyLibraryEntry; totalProjects: number; onClone: () => void; onEdit: () => void; onRemove: () => void
}) {
  const scope = p.adoptedBy >= totalProjects && totalProjects > 0 ? 'all projects' : `${p.adoptedBy} projects`
  const who = <ProjectCount to={`/policies/${encodeURIComponent(p.name)}/projects`} count={p.adoptedBy} />
  const [deleting, setDeleting] = useState(false)
  return (
    <LibraryRow name={p.name} to={page ? undefined : `/policies/${encodeURIComponent(p.name)}`}
      badges={p.origin !== 'stock' && <span className="badge">yours</span>}
      purpose={p.description}
      state={<span className="small">{who}</span>}
      menu={<MoreMenu label={p.name} items={[
        { label: 'Copy', hint: 'Make a policy of your own from this one.', onClick: onClone },
        ...(p.origin !== 'stock' ? [
          { label: 'Edit', onClick: onEdit },
          { label: 'Delete…', danger: true, onClick: () => setDeleting(true) },
        ] : []),
      ]} />}
      detail={page && <>
          <div className="muted small"><OriginChip origin={p.origin} />
            {p.packs.length > 0 && <> · in packs: {p.packs.map((pack, i) => <Fragment key={pack}>{i > 0 && ', '}<Ident>{pack}</Ident></Fragment>)}</>}</div>
          <div className="policies-page-policy-card-div">
            <div className="muted small policies-page-policy-card-div-2">The rule</div>
            {p.reading}
          </div>
          {p.fix && (
            <div className="policies-page-policy-card-div">
              <div className="muted small policies-page-policy-card-div-2">If it blocks you</div>
              {p.fix}
            </div>
          )}
          <section className="policies-page-policy-card-div">
            <h3 className="muted small policies-page-policy-card-summary">definition</h3>
            <pre className="source policies-page-policy-card-pre">
{['rule: ' + p.rule,
  ...(Object.keys(p.with ?? {}).length
    ? ['with:', ...Object.entries(p.with).map(([k, v]) => `  ${k}: ${JSON.stringify(v)}`)]
    : ['with: {}'])].join('\n')}
            </pre>
          </section>
          {p.adoptedBy > 0 && <p>Applies to {scope}. {p.nonConformant} not following it.</p>}

      </>}>
      {deleting && <div className="row small" onClick={(e) => e.stopPropagation()}>
        Delete “{p.name}” from the library? Projects that adopted it keep their copies.
        <button className="danger" onClick={() => { setDeleting(false); onRemove() }}>Delete</button>
        <button className="subtle" onClick={() => setDeleting(false)}>Keep it</button>
      </div>}
    </LibraryRow>
  )
}

function PolicyEditor({ policy, onDone }: { policy: PolicyLibraryEntry; onDone: (saved?: boolean) => void }) {
  const [p, setP] = useState(policy)
  const { run, busy, err } = useAction(() => onDone(true), { affects: ['/projects'] })
  const form = RULES.find((r) => r.rule === p.rule) ?? RULES[0]

  const save = () => run(() => post('/policy-library', { name: p.name, description: p.description, rule: p.rule, with: p.with }))

  return (
    <div className="policies-page-policy-editor-div">
      <h2>{policy.name && policy.origin === '' && policy.name === p.name ? `Edit: ${policy.name}` : 'New policy'}</h2>
      <div className="form-grid mb-3" >
        <label>Name</label>
        <input aria-label="Name" value={p.name} onChange={(e) => setP({ ...p, name: e.target.value })} placeholder="my-rule" autoFocus />
        <label>Description</label>
        <input aria-label="Description" value={p.description} onChange={(e) => setP({ ...p, description: e.target.value })} />
        <label>Rule</label>
        <select aria-label="Rule" value={p.rule} onChange={(e) => setP({ ...p, rule: e.target.value, with: {} })}>
          {RULES.map((r) => <option key={r.rule} value={r.rule}>{r.label}</option>)}
        </select>
        {form.params.map((param) => (
          <span key={param} className="policies-page-span">
            <label>{param}</label>
            <input aria-label={param} value={String(p.with?.[param] ?? '')}
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
