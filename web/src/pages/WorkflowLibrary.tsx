import { PRODUCT } from '../brand'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { LibraryWorkflow, del, post, put } from '../api'
import { OriginChip } from '../OriginChip'
import { firstErr, useAction, useGet } from '../data'
import type { SystemWorkflowBindings, WorkflowPublished } from '../wire.gen'

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
  const [tier, setTier] = useState<'core' | 'extra' | 'system'>('core')
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

  return (
    <div>
      <div className="row" style={{ marginBottom: 12 }}>
        <span className="muted small">Each run uses the version its project adopted, and records which one ran.</span>
        <span style={{ flex: 1 }} />
        {!editor && <button className="primary" onClick={() => { setSaveErr(''); setEditor({ yaml: NEW_TEMPLATE }) }}>New workflow</button>}
      </div>
      {editor && (
        <div className="card" style={{ marginBottom: 16 }}>
          <div className="row" style={{ marginBottom: 6 }}>
            <strong>Custom workflow</strong>
            <span className="muted small">saved by its metadata.name; a name that ships with {PRODUCT} is read-only</span>
          </div>
          <textarea rows={16} value={editor.yaml} onChange={(e) => setEditor({ yaml: e.target.value })}
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
      <div className="tabs" style={{ marginTop: 4 }}>
        {([['core', 'Core'], ['extra', 'Extras'], ['system', 'System']] as const).map(([k, label]) => (
          <button key={k} className={tier === k ? 'active' : ''} onClick={() => setTier(k)}>
            {label} <span className="muted small">{libs.filter((l) => (l.tier ?? 'extra') === k).length}</span>
          </button>
        ))}
      </div>
      <div className="muted small" style={{ marginBottom: 10 }}>
        {tier === 'core' && 'The short list: the ways of working every project is likely to need.'}
        {tier === 'extra' && 'Workflows for particular situations.'}
        {tier === 'system' && `${PRODUCT} runs these itself (landing, shipping, wiring integrations in and out). An admin can replace one with a custom workflow.`}
      </div>
      {libs.filter((l) => (l.tier ?? 'extra') === tier).sort((a, b) => a.name.localeCompare(b.name)).map((l) => (
        <div className="card" key={l.name}>
          <div className="row">
            <strong>{l.name}</strong>
            <OriginChip origin={l.origin} />
            <span className="chip">v{l.latestVersion}</span>
            <span className="muted small">{l.kind}</span>
            <span style={{ flex: 1 }} />
            {l.origin !== 'stock' && <>
              <button className="subtle" onClick={() => { setSaveErr(''); setEditor({ yaml: l.yaml }) }}>edit</button>
              <button className="subtle" disabled={busy} onClick={() => void removeCustom(l.name)}>delete</button>
            </>}
          </div>
          <div className="small" style={{ margin: '6px 0' }}>{l.description}</div>
          {tier === 'system' && bindings && bindings.bindings.some((b) => b.stock === l.name) && (() => {
            const b = bindings.bindings.find((x) => x.stock === l.name)!
            return (
              <div className="row small" style={{ margin: '4px 0 8px' }}>
                <span className="muted">{PRODUCT} runs:</span>
                <select value={b.custom ? b.name : ''} disabled={busy} onChange={(e) => void bind(l.name, e.target.value)}>
                  <option value="">{l.name} (stock)</option>
                  {bindings.candidates.map((c) => <option key={c} value={c}>{c} (yours)</option>)}
                </select>
                {b.custom && <span className="chip pass">custom</span>}
                <span className="muted">— a custom stand-in must say <code>system: true</code></span>
              </div>
            )
          })()}
          <div className="row muted small">
            stages: {l.stages.map((s) => <span key={s} className="chip">{s}</span>)}
            {tier !== 'system' && (
              l.usedBy.length > 0
                ? <details style={{ display: 'inline-block', margin: 0 }}>
                    <summary className="muted small" style={{ display: 'inline-flex', padding: '0 6px', border: 'none', background: 'none' }}>used by {l.usedBy.length} project{l.usedBy.length === 1 ? '' : 's'}</summary>
                    <span className="small" style={{ marginLeft: 6 }}>{l.usedBy.map((u, i) => (
                      <span key={i}>{i > 0 && ', '}<Link to={`/projects/${u.project}/settings/working`}>{u.project}</Link></span>
                    ))}</span>
                  </details>
                : <span>not used by any project</span>
            )}
          </div>
          <details><summary className="muted small">view yaml</summary><pre className="log">{l.yaml}</pre></details>
        </div>
      ))}
    </div>
  )
}
