import { Status, STATUS_LIGHT } from '../../Status'
import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import type { ChangeAttachment } from '../../wire.gen'
import { renderMd } from '../../md'
import DiffView from '../../DiffView'
import { ChangeView, HttpError, Run, del, patch, post } from '../../api'
import { closedNothingToChange } from '../../states'
import { fmtCost, fmtStamp, fmtTokens } from '../../format'
import { useAction, useGet, useGetText } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'

type Brief = { body: string; externalRef: string }
const briefOf = (c: { body: string; externalRef?: string }): Brief => ({ body: c.body, externalRef: c.externalRef ?? '' })

// firstLine: a text's first sentence or line, for a one-line summary.
export function firstLine(text: string, max = 180): string {
  const t = text.replace(/^#+ .*\n+/, '').trim()
  const line = t.split('\n').find((l) => l.trim() && !l.trim().startsWith('#'))?.trim() ?? ''
  const plain = line.replace(/[*_`]/g, '')
  const dot = plain.search(/[.!?](\s|$)/)
  const one = dot > 20 ? plain.slice(0, dot + 1) : plain
  return one.length > max ? one.slice(0, max - 1) + '…' : one
}

// The page's head: what this change is — its title, where it belongs, who
// asked for it (when the record says), when it was opened and under which
// workflow, and one plain line of what
// it does. The full request, the summary and the attachments are in the
// About tab, one click away.
export function ChangeHeader({ view, workflow, aboutTab }: { view: ChangeView; workflow?: string; aboutTab: string }) {
  const ch = view.change
  return (
    <header className="ch-head">
      <div className="ch-crumb">
        <Link to={`/projects/${view.projectSlug}`}>{view.projectSlug}</Link><span className="ch-sep">/</span>Change {ch.number}
      </div>
      <div className="ch-title-row">
        <h1>{ch.title}</h1>
        <Status word={view.statusLabel} tone={STATUS_LIGHT[view.statusLabel]} />
        {ch.provenance && <span className="chip pending" title="Adopted from an outside branch: history reconstructed from commits, not witnessed as it happened.">{ch.provenance}</span>}
      </div>
      <div className="ch-meta">
        {[
          ch.openedBy && `Asked by ${ch.openedBy}`,
          `Opened ${fmtStamp(ch.createdAt)}`,
          workflow && `${workflow} workflow`,
          ch.closedAt && `${ch.state === 'abandoned' && !closedNothingToChange(ch.closedAs) ? 'abandoned' : 'closed'} ${fmtStamp(ch.closedAt)}`,
          view.diffstat,
        ].filter(Boolean).join(' · ')}
        {ch.externalRef && <> · {ch.externalRef}</>}
        {' · '}<Link to={aboutTab}>request and details</Link>
      </div>
    </header>
  )
}

// What this change is for, in full: the request (editable), where it is up
// to, and its reference material.
//
// The request editor follows design/architecture/web-data.md: it opens only
// on an answer the page read itself, keeps the draft while the page polls,
// saves only what changed with the revision it started from, and on a
// conflict shows the current text and lets the person choose.
export function ChangeBrief({ view, current = true, reload }: { view: ChangeView; current?: boolean; reload: () => void }) {
  const ch = view.change
  const [editing, setEditing] = useState(false)
  const [base, setBase] = useState<{ brief: Brief; revision: number } | null>(null)
  const [draft, setDraft] = useState<Brief>({ body: '', externalRef: '' })
  const [conflict, setConflict] = useState(false)
  const { run: runSave, busy: saving, err: saveErr, failure, setErr: setSaveErr } = useAction(undefined, { affects: [`/changes/${ch.id}`] })
  const open = () => {
    const start = { brief: briefOf(ch), revision: ch.editRevision }
    setBase(start); setDraft(start.brief); setConflict(false); setEditing(true)
  }
  const changed = base ? (Object.keys(draft) as (keyof Brief)[]).filter((k) => draft[k] !== base.brief[k]) : []
  const save = async () => {
    if (!base || changed.length === 0) { setEditing(false); return }
    setConflict(false)
    const body: Record<string, unknown> = { expectedEditRevision: base.revision }
    for (const k of changed) body[k] = draft[k]
    const saved = await runSave(() => patch(`/changes/${ch.id}`, body))
    if (saved) setEditing(false)
  }
  // A refused save means the page's reading is old: read the change again so
  // the person sees what changed before choosing.
  const reread = useRef(reload)
  reread.current = reload
  useEffect(() => {
    if (failure instanceof HttpError && failure.code === 'change-edit-moved') { setConflict(true); reread.current() }
  }, [failure])
  const newer = !!base && ch.editRevision > base.revision
  const keepMine = () => {
    const now = briefOf(ch)
    setDraft((d) => ({ body: base && d.body !== base.brief.body ? d.body : now.body, externalRef: base && d.externalRef !== base.brief.externalRef ? d.externalRef : now.externalRef }))
    setBase({ brief: now, revision: ch.editRevision })
    setConflict(false); setSaveErr('')
  }
  const discard = () => { setEditing(false); setConflict(false); setSaveErr('') }
  const emptyCommitted = view.committedDiff?.status === 'empty'
  const soFar = emptyCommitted ? view.committedDiff?.message : ch.summary || view.report
  const { run: distill, busy: distilling, err: distillErr } = useAction()
  const redistill = () => distill(() => post(`/changes/${ch.id}/distill`, {}))
  const u = view.usage
  return (
    <div className="ch-brief">
      <div className="ch-sheet">
        <div className="row">
          <span className="ch-label">What was asked for</span>
          <span style={{ flex: 1 }} />
          <button className="subtle" disabled={!editing && !current} onClick={() => (editing ? discard() : open())}
            title={!editing && !current ? 'reading the current change before editing' : undefined}>
            {editing ? 'cancel' : 'edit'}
          </button>
        </div>
        {!editing && (ch.body
          ? <div style={{ whiteSpace: 'pre-wrap' }}>{ch.body}</div>
          : <div className="muted">No request text was written; the title is the request.</div>)}
        {!editing && ch.externalRef && <div className="muted small" style={{ marginTop: 6 }}>External reference: {ch.externalRef}</div>}
        {editing && (
          <div>
            <textarea rows={4} style={{ width: '100%' }} value={draft.body} disabled={saving} onChange={(e) => setDraft((d) => ({ ...d, body: e.target.value }))} />
            <div className="row" style={{ marginTop: 6 }}>
              <input placeholder="external ref (Jira key, ticket URL)" value={draft.externalRef} disabled={saving} onChange={(e) => setDraft((d) => ({ ...d, externalRef: e.target.value }))} />
              <button className="primary" onClick={save} disabled={saving || conflict}>{saving ? 'Saving…' : 'Save'}</button>
              {saveErr && !conflict && <span className="error small">{saveErr}</span>}
            </div>
            {conflict && base && (
              <div className="card tone-warn" style={{ marginTop: 8 }}>
                <div>Someone edited this change after you started. Your edits are kept here, unsaved.</div>
                {briefOf(ch).body !== base.brief.body && <div className="muted small" style={{ marginTop: 4, whiteSpace: 'pre-wrap' }}>What was asked for is now: “{briefOf(ch).body}”</div>}
                {briefOf(ch).externalRef !== base.brief.externalRef && <div className="muted small" style={{ marginTop: 4 }}>The external reference is now “{briefOf(ch).externalRef}”.</div>}
                <div className="row" style={{ marginTop: 8 }}>
                  <button onClick={keepMine} disabled={!newer}>Keep my edits on top of theirs</button>
                  {!newer && <span className="muted small">reading the current change…</span>}
                  <button className="subtle" onClick={discard}>Discard my edits</button>
                </div>
              </div>
            )}
          </div>
        )}
      </div>

      <div className="ch-sheet">
        <div className="row">
          <span className="ch-label">Where it's up to</span>
          <span className="muted small">{!soFar ? '' : emptyCommitted ? 'from the committed diff' : ch.summary ? 'an AI summary of the recorded work' : "the agent's latest change report"}</span>
          <span style={{ flex: 1 }} />
          <button className="subtle" onClick={redistill} disabled={distilling}
            title="Write the summary again from the current evidence" aria-label="write the summary again">{distilling ? 'Writing…' : 'Write it again'}</button>
        </div>
        {distillErr && <div className="error small">The summary could not be refreshed: {distillErr}</div>}
        {soFar ? <SoFarNote text={soFar} /> : <div className="muted">No summary has been written yet.</div>}
      </div>

      <div className="ch-sheet">
        <Attachments changeId={ch.id!} readOnly={ch.state === 'shipped' || ch.state === 'abandoned'} />
        <table className="kv" style={{ marginTop: 10 }}>
          <tbody>
            {ch.branch && <tr><td className="kv-key">Branch</td><td className="kv-val"><code>{ch.branch}</code></td></tr>}
            {ch.baseSha && <tr><td className="kv-key">Started from</td><td className="kv-val"><code>{ch.baseSha.slice(0, 12)}</code></td></tr>}
            {ch.landedSha && <tr><td className="kv-key">Landed as</td><td className="kv-val"><code>{ch.landedSha.slice(0, 12)}</code></td></tr>}
            <tr><td className="kv-key">Kind</td><td className="kv-val">{ch.type}</td></tr>
            <tr><td className="kv-key">Cost so far</td><td className="kv-val">{u != null && u.total > 0 ? <>{fmtTokens(u.total)} tokens{u.costMicros > 0 && <> · {fmtCost(u.costMicros)}</>}</> : 'No model usage recorded.'}</td></tr>
          </tbody>
        </table>
      </div>
    </div>
  )
}

// Reference material pinned to the Change: visible to agents and consoles at
// .apptown/attachments/ in the worktree; never merges (gitignored).
export function Attachments({ changeId, readOnly }: { changeId: string; readOnly: boolean }) {
  const itemsQ = useGet<ChangeAttachment[]>(`/changes/${changeId}/attachments`)
  const items = itemsQ.data ?? []
  const { run, busy, err } = useAction()

  const upload = (f: File) => {
    const fd = new FormData()
    fd.append('file', f)
    void run(() => post(`/changes/${changeId}/attachments`, fd))
  }
  const remove = (name: string) => { void run(() => del(`/changes/${changeId}/attachments/${encodeURIComponent(name)}`)) }

  // A list never read is not an empty one: say why instead.
  if (readOnly && !itemsQ.data) return <ReadStatus q={itemsQ} what="the attachments" className="small" />
  if (readOnly && items.length === 0) return <><StaleNote q={itemsQ} /><div className="muted small">No attachments.</div></>
  return (
    <div className="row" style={{ marginTop: 8, flexWrap: 'wrap' }}>
      <span className="ch-label">Attachments</span>
      <ReadStatus q={itemsQ} what="the attachments" className="small" />
      <StaleNote q={itemsQ} />
      {items.map((i) => (
        <span key={i.name} className="chip" title={`${(i.bytes / 1024).toFixed(1)} KB · agents see .apptown/attachments/${i.name}`}>
          <a href={`/api/v1/changes/${changeId}/attachments/${encodeURIComponent(i.name)}`} target="_blank" rel="noreferrer">{i.name}</a>
          {!readOnly && <button className="subtle" style={{ marginLeft: 4, padding: 0 }} aria-label={`remove ${i.name}`} title="remove" disabled={busy} onClick={() => remove(i.name)}>✕</button>}
        </span>
      ))}
      {!readOnly && (
        <label className="subtle" style={{ cursor: 'pointer' }}>
          + attach file
          <input type="file" style={{ display: 'none' }} disabled={busy}
            onChange={(e) => { const f = e.target.files?.[0]; if (f) upload(f); e.target.value = '' }} />
        </label>
      )}
      {err && <span className="error small">{err}</span>}
    </div>
  )
}

// The Changes tab: what the change does to the code — the committed diff
// against where it started, and its commits. A comparison that could not be
// made says so; "no changes" is said only after a comparison that worked.
// A change with no code of its own (only ever shipped main) has no branch:
// it shows what its release put live that the release before it did not.
export function ChangesTab({ view, run }: { view: ChangeView; run?: Run }) {
  const ch = view.change
  const commits = view.commits ?? []
  const noCode = view.committedDiff?.status === 'no-code'
  return (
    <div>
      {noCode && <p className="muted">{view.committedDiff.message}</p>}
      {noCode && !!view.committedDiff.shipped?.since && <ChangeDiffBlock changeId={ch.id!} empty={`No difference from release #${view.committedDiff.shipped.since}.`} />}
      {view.committedDiff?.status === 'empty' && <p className="muted">{view.committedDiff.message}</p>}
      {view.committedDiff?.status === 'unavailable' && ch.branch && <p className="muted">{view.committedDiff.message}</p>}
      {!ch.branch && !noCode && <p className="muted">No branch.</p>}
      {ch.branch && !noCode && view.committedDiff?.status !== 'empty' && <ChangeDiffBlock changeId={ch.id!} />}
      {commits.length > 0 && (
        <>
          <h3>Commits ({commits.length})</h3>
          <table className="stack">
            <tbody>
              {commits.map((c) => (
                <tr key={c.sha}>
                  <td className="muted small"><code>{c.sha}</code></td>
                  <td className="small stack-lead">{c.subject}</td>
                  <td className="muted small">{c.when}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
      {run?.id && <RunDiff runId={run.id} number={run.number} />}
    </div>
  )
}

// The latest run's own diff and workspace: what the run's workspace holds,
// which can differ from the committed diff while work is under way.
function RunDiff({ runId, number }: { runId: string; number: number }) {
  const [open, setOpen] = useState(false)
  return (
    <details onToggle={(e) => setOpen((e.target as HTMLDetailsElement).open)}>
      <summary>The latest run's workspace (run {number})</summary>
      <div className="row" style={{ marginBottom: 6 }}>
        <a href={`/api/v1/runs/${runId}/workspace/`} target="_blank" rel="noreferrer" className="btn">Browse the workspace ↗</a>
      </div>
      {open && <RunDiffBody runId={runId} />}
    </details>
  )
}

function RunDiffBody({ runId }: { runId: string }) {
  return <DiffBlock path={`/runs/${runId}/diff`} what="the diff" empty="(empty diff)" />
}

export function ChangeDiffBlock({ changeId, empty }: { changeId: string; empty?: string }) {
  // A shared read: the cockpit and its footer ask once, and an act on the
  // change refreshes it with the change.
  return <DiffBlock path={`/changes/${changeId}/diff`} what="the committed diff" empty={empty ?? 'No committed changes since this change started.'} />
}

// DiffBlock is one diff read and shown: the status until it is read, a
// stale note if its refresh failed, the diff, or what empty means here. Text
// that is not a git diff (a note from AppTown) is shown as it came.
function DiffBlock({ path, what, empty }: { path: string; what: string; empty: string }) {
  const q = useGetText(path)
  const diff = q.data
  if (diff === null) return <ReadStatus q={q} what={what} className="small" />
  return <>
    <StaleNote q={q} />
    {!diff
      ? <div className="muted small">{empty}</div>
      : !diff.startsWith('diff --git')
        ? <pre className="log" style={{ maxHeight: 200, marginTop: 8 }}>{diff}</pre>
        : <div style={{ maxHeight: 560, overflowY: 'auto' }}><DiffView raw={diff} /></div>}
  </>
}

// SoFarNote: long notes clip at a sentence boundary and expand IN PLACE —
// never a mid-word cut, never the same text rendered twice.
export function SoFarNote({ text }: { text: string }) {
  const [open, setOpen] = useState(false)
  let cut = text.length
  if (text.length > 520) {
    const window = text.slice(280, 520)
    const dot = window.search(/[.!?]\s/)
    cut = dot >= 0 ? 280 + dot + 1 : 440
  }
  const clipped = cut < text.length
  const shown = open || !clipped ? text : text.slice(0, cut)
  return (
    <div>
      <div className="md-doc" dangerouslySetInnerHTML={{ __html: renderMd(shown) }} />
      {clipped && (
        <button className="subtle" style={{ marginTop: 4 }} onClick={() => setOpen(!open)}>
          {open ? '− show less' : '+ read the rest'}
        </button>
      )}
    </div>
  )
}
