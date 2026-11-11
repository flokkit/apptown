import './NewChange.css'
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Workflow, post } from '../../api'
import { Modal } from '../../Modal'
import { useAction, useGet } from '../../data'
import { START_OPTIONS } from '../../startOptions'
import type { CreatedChange } from '../../wire.gen'

// New change is THE entry point to the dev loop (design/subsystems/dev-loop.md): title
// + For + optional ref, then start it (branch + worktree, off to the
// cockpit). A workflow is optional — work needs a place to happen before it
// needs a workflow. Ideas for later belong in the team's ticket tracker.
export function NewChangeButton({ slug, external, request }: { slug: string; request?: { title: string; body: string }; external?: { open: boolean; onClose: () => void } }) {
  const nav = useNavigate()
  const [openState, setOpenState] = useState(false)
  const open = external ? external.open : openState
  const setOpen = (v: boolean) => { if (external) { if (!v) external.onClose() } else setOpenState(v) }
  const [title, setTitle] = useState(request?.title ?? '')
  const [body, setBody] = useState(request?.body ?? '')
  const [ref, setRef] = useState('')
  const [wf, setWf] = useState('')
  const { data: loadedWorkflows } = useGet<Workflow[]>(open ? `/projects/${slug}/workflows` : null)
  // AppTown runs its own workflows (landing, integration wiring); a person picks among the rest.
  const workflows = (loadedWorkflows ?? []).filter((w) => w.tier !== 'system')
  const { run, busy, err, setErr } = useAction()

  const create = async () => {
    if (!title.trim()) { setErr('title required'); return }
    const r = await run(() => post<CreatedChange>(`/projects/${slug}/changes`, {
      title, body, externalRef: ref, workflow: wf,
    }))
    if (!r) return
    setOpen(false); setTitle(''); setBody(''); setRef(''); setWf('')
    nav(`/changes/${r.change.id}`)
  }

  return (
    <>
      {!external && <button className="subtle new-change-new-change-button-subtle"  onClick={() => setOpen(true)}>+ new change without a chat</button>}
      {open && (
        <Modal title="New change" onClose={() => setOpen(false)} maxWidth={560}>
          <div className="new-change-new-change-button-div">
            <div className="form-grid">
              <label>Title</label>
              <input aria-label="Title" autoFocus value={title} onChange={(e) => setTitle(e.target.value)} />
              <label>For</label>
              <textarea aria-label="For" className="prose" rows={4} value={body} onChange={(e) => setBody(e.target.value)}
                placeholder={START_OPTIONS.find((l) => l.wf === wf)?.bodyHint ?? 'Why this change exists · agents work from this text.'} />
              <label>Reference</label>
              <input aria-label="Reference" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="Jira key / ticket URL (optional)" />
            </div>
            <div className="new-change-new-change-button-div-2">
              <div className="muted small new-change-new-change-button-div-3">Workflow</div>
              {START_OPTIONS.filter((l) => workflows.some((w) => w.name === l.wf)).map((l) => (
                <label key={l.wf} className="row new-change-label">
                  <input type="radio" name="lane" checked={wf === l.wf} onChange={() => setWf(l.wf)} />
                  <span className={wf === l.wf ? 'text-name' : undefined}>{l.label}</span>
                  <span className="muted small">{l.hint}</span>
                </label>
              ))}
              <label className="row new-change-label">
                <input type="radio" name="lane" checked={wf === ''} onChange={() => setWf('')} />
                <span className={wf === '' ? 'text-name' : undefined}>Just open it</span>
                <span className="muted small">no run yet; use the chat or a workflow later</span>
              </label>
              <label className="row new-change-new-change-button-label">
                <span className="muted small">other workflow</span>
                <select value={START_OPTIONS.some((l) => l.wf === wf) || wf === '' ? '' : wf}
                  onChange={(e) => e.target.value && setWf(e.target.value)}>
                  <option value="">—</option>
                  {workflows.filter((w) => w.name !== 'land' && !START_OPTIONS.some((l) => l.wf === w.name))
                    .map((w) => <option key={w.id} value={w.name}>{w.name}</option>)}
                </select>
              </label>
            </div>
            <div className="row new-change-new-change-button-div-4">
              <button className="primary" disabled={busy} onClick={() => create()}>
                {busy ? '…' : wf ? `Start · ${START_OPTIONS.find((l) => l.wf === wf)?.label ?? wf}` : 'Start change'}
              </button>
              {err && <span className="error">{err}</span>}
            </div>
          </div>
        </Modal>
      )}
    </>
  )
}
