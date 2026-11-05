import { LibraryRow, LibraryRows, firstSentence } from '../LibraryRow'
import { MoreMenu } from '../MoreMenu'
import { Section } from '../Section'
import { PRODUCT } from '../brand'
import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { LibraryWorkflow, del, post, put } from '../api'
import { firstErr, useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { SystemWorkflowBindings, WorkflowPublished } from '../wire.gen'

const NEW_TEMPLATE = `apiVersion: apptown.dev/v1
kind: Workflow
metadata:
  name: team/my-workflow
  category: change
  tier: extra
  description: What this workflow does.
spec:
  kind: lifecycle
  stages:
    - name: work
      steps:
        - name: do-it
          uses: agent/pi
        - name: accept
          uses: approval
`

export default function WorkflowLibrary() {
  const libsQ = useGet<LibraryWorkflow[]>('/workflows/library', { kind: 'config' })
  const libs = libsQ.data ?? []
  const [msg, setMsg] = useState('')
  const { run, busy, err: actErr } = useAction()
  const [editor, setEditor] = useState<{ yaml: string } | null>(null)
  const { run: runSave, busy: saving, err: saveErr, setErr: setSaveErr } = useAction()
  // Three tiers: Core — the short list everyone needs; Extras — the
  // longer tail we are less sure of; System — AppTown's own jobs, kept
  // apart but never hidden. A workflow declares its tier in its YAML;
  // custom ones land where they say, Extras by default.
  // The page's New workflow (in the title row) opens the editor here.
  const [params, setParams] = useSearchParams()
  useEffect(() => {
    if (params.get('new') === null) return
    setSaveErr(''); setEditor({ yaml: NEW_TEMPLATE })
    setParams((p) => { p.delete('new'); return p }, { replace: true })
  }, [params, setParams, setSaveErr])
  // The instance's system bindings: which library workflow stands in for
  // each stock system one (design/decisions/workflows-as-lanes.md — how an admin
  // customises landing or wiring for the whole instance without forking
  // per project).
  const bindingsQ = useGet<SystemWorkflowBindings>('/system-workflows')
  const bindings = bindingsQ.data
  const err = firstErr(actErr, libsQ.err, bindingsQ.err)
  const bind = (stock: string, name: string) => {
    setMsg('')
    return run(async () => {
      await put('/system-workflows', { stock, name })
      setMsg(name && name !== stock ? `${stock} now runs as ${name}` : `${stock} back to stock`)
    })
  }

  const saveCustom = () => {
    if (!editor) return
    return runSave(async () => {
      const res = await post<WorkflowPublished>('/workflows/library', { yaml: editor.yaml })
      setEditor(null)
      setMsg(`saved ${res.name} v${res.version}`)
    })
  }

  const removeCustom = (name: string) => {
    setMsg('')
    return run(async () => {
      await del(`/workflows/library/${name}`)
      setMsg(`deleted ${name}`)
    })
  }


  const TIERS = [['core', 'Everyday work'], ['extra', 'More workflows'], ['system', `${PRODUCT} administration`]] as const
  const copy = (l: LibraryWorkflow) => { setSaveErr(''); setEditor({ yaml: l.yaml.replace(/name: .*/, 'name: team/my-workflow') }) }

  return (
    <div>
      {editor && (
        <div className="card" style={{ marginBottom: 16 }}>
          <div className="row" style={{ marginBottom: 6 }}>
            <strong>Custom workflow</strong>
            <span className="muted small">Stock names are read-only.</span>
          </div>
          <textarea aria-label="Workflow YAML" rows={16} value={editor.yaml} onChange={(e) => setEditor({ yaml: e.target.value })}
            style={{ width: '100%', fontFamily: 'var(--font-mono)', fontSize: 'var(--fs-xs)' }} />
          {saveErr && <div className="error" style={{ marginTop: 6 }}>{saveErr}</div>}
          <div className="row" style={{ marginTop: 8 }}>
            <button className="primary" disabled={saving} onClick={() => void saveCustom()}>Save</button>
            <button onClick={() => setEditor(null)}>Cancel</button>
          </div>
        </div>
      )}
      {err && <div className="error">{err}</div>}
      {msg && <div className="muted small">{msg}</div>}
      <ReadStatus q={libsQ} what="the workflows" />
      <StaleNote q={libsQ} />
      {libsQ.data && libs.length === 0 && <p className="muted">No workflows.</p>}
      {TIERS.map(([tier, label]) => {
        const rows = libs.filter((l) => (l.tier ?? 'extra') === tier).sort((a, b) => a.name.localeCompare(b.name))
        if (rows.length === 0) return null
        return (
          <Section key={tier} title={label} count={rows.length}>
            <LibraryRows>
              {rows.map((l) => (
                <WorkflowRow key={l.name} l={l} busy={busy}
                  binding={tier === 'system' ? bindings?.bindings.find((b) => b.stock === l.name) : undefined}
                  candidates={bindings?.candidates ?? []}
                  onBind={(name) => void bind(l.name, name)}
                  onCopy={() => copy(l)}
                  onEdit={() => { setSaveErr(''); setEditor({ yaml: l.yaml }) }}
                  onDelete={() => void removeCustom(l.name)} />
              ))}
            </LibraryRows>
          </Section>
        )
      })}
    </div>
  )
}

// purpose is the first sentence of a workflow's description: what it is for.
// One workflow: its name and purpose, who ships it and its version. The
// whole row opens what it does in full; the less common acts sit behind ⋯.
function WorkflowRow({ l, busy, binding, candidates, onBind, onCopy, onEdit, onDelete }: {
  l: LibraryWorkflow; busy: boolean
  binding?: { stock: string; name: string; custom: boolean }; candidates: string[]
  onBind: (name: string) => void; onCopy: () => void; onEdit: () => void; onDelete: () => void
}) {
  const [open, setOpen] = useState(false)
  const stock = l.origin === 'stock' || (l.origin ?? '').startsWith('stock/')
  return (
    <LibraryRow name={l.name} open={open} onToggle={() => setOpen(!open)}
      badges={binding?.custom && <span className="badge">custom</span>}
      purpose={l.description && firstSentence(l.description)}
      state={<span className="muted small">{stock ? PRODUCT : 'yours'} · v{l.latestVersion}</span>}
      menu={<MoreMenu label={l.name} items={[
        { label: 'Copy', hint: 'Make a workflow of your own from this one. Save it with a new name.', onClick: onCopy },
        ...(!stock ? [
          { label: 'Edit', onClick: onEdit },
          { label: 'Delete', danger: true, disabled: busy, onClick: onDelete },
        ] : []),
      ]} />}
      detail={<>
          {l.description && <p>{l.description}</p>}
          <p className="small">{l.kind} · {l.stages.join(' → ')}</p>
          {binding && (
            <div className="row small" style={{ margin: '4px 0 8px' }}>
              <span className="muted">{PRODUCT} runs:</span>
              <select aria-label={`Workflow for ${binding.stock}`} value={binding.custom ? binding.name : ''} disabled={busy} onChange={(e) => onBind(e.target.value)}>
                <option value="">{l.name} (stock)</option>
                {candidates.map((c) => <option key={c} value={c}>{c} (yours)</option>)}
              </select>
              <span className="muted">A custom workflow must declare <code>system: true</code>.</span>
            </div>
          )}
          <pre className="log">{l.yaml}</pre>
      </>} />
  )
}
