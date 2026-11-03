import { Link, useLocation } from 'react-router-dom'
import { shortName } from '../newProject/draft'
import { Status } from '../../Status'
import { useEffect, useRef, useState } from 'react'
import type { ChangeAttachment } from '../../wire.gen'
import { renderMd } from '../../md'
import DiffView from '../../DiffView'
import { ChangeView, HttpError, Run, del, patch, post } from '../../api'
import { closedNothingToChange } from '../../states'
import { fmtStamp } from '../../format'
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

// shortDiffstat says git's "4 files changed, 25 insertions(+), 2 deletions(-)"
// as "+25 −2 in 4 files"; anything else is shown as it came.
export function shortDiffstat(stat?: string): string {
  if (!stat) return ''
  const n = (re: RegExp) => Number(stat.match(re)?.[1] ?? 0)
  const files = n(/(\d+) files? changed/)
  if (!files) return stat
  const parts = [`+${n(/(\d+) insertions?\(\+\)/)}`, `−${n(/(\d+) deletions?\(-\)/)}`]
  return `${parts.join(' ')} in ${files} file${files === 1 ? '' : 's'}`
}

// The page's head: what this change is — its title, its state, who asked
// for it (when the record says), when it was opened and how big it is. The
// request and summary are on Overview; editing and attachments are in Details.
export function changeTitle(ch: ChangeView['change']): string {
  const title = ch.title.trim()
  const request = ch.body?.trim() ?? ''
  const copied = title.replace(/[…]+$|\.\.\.$/g, '').trim()
  return request && copied.length > 35 && request.startsWith(copied) ? shortName(request) || title : title
}

export function ChangeHeader({ view, compact }: { view: ChangeView; compact?: boolean }) {
  const ch = view.change
  return (
    <header className="ch-head">
      <div className="ch-title-row">
        <h1><span className="ch-number">Change #{ch.number}:</span> {changeTitle(ch)}</h1>
        {!compact && <Status word={view.statusLabel} />}
        {ch.provenance && <span className="badge" title="Adopted from an outside branch: history reconstructed from commits, not witnessed as it happened.">{ch.provenance}</span>}
      </div>
      {!compact && <div className="ch-byline">
        {[
          ch.openedBy && `Asked by ${ch.openedBy}`,
          `Opened ${fmtStamp(ch.createdAt)}`,
          ch.closedAt && `${ch.state === 'abandoned' && !closedNothingToChange(ch.closedAs) ? 'Abandoned' : 'Closed'} ${fmtStamp(ch.closedAt)}`,
          shortDiffstat(view.diffstat),
          ch.externalRef,
        ].filter(Boolean).join(' · ')}
      </div>}
    </header>
  )
}

export function changeReport(view: ChangeView, plainFirst = false): string | undefined {
  // The written summary is for people; the agent's own report is for engineers. Overview asks for the summary first.
  return (plainFirst ? [view.change.summary, view.report] : [view.report, view.change.summary]).find(text => text?.trim()
    && !/^project work[.!]?$/i.test(text.trim())
    && ![view.change.body?.trim(), view.change.title?.trim()].includes(text.trim()))

}

// Keep the existing automatic summary request for a waiting acceptance.
// It belongs to the page so moving between tabs does not ask again.
export function useChangeSummary(changeId: string | undefined, needed: boolean) {
  const asked = useRef<string>()
  useEffect(() => {
    if (!changeId || !needed || asked.current === changeId) return
    asked.current = changeId
    post(`/changes/${changeId}/distill`, {}).catch(() => { /* A missing summary makes no claim about the change. */ })
  }, [changeId, needed])
}

// What this change is for: a few request lines and a link to the full text,
// with the request editor and its reference material.
//
// The request editor follows design/architecture/web-data.md: it opens only
// on an answer the page read itself, keeps the draft while the page polls,
// saves only what changed with the revision it started from, and on a
// conflict shows the current text and lets the person choose.
export function RequestEntry({ view, current = true, reload }: { view: ChangeView; current?: boolean; reload: () => void }) {
  const ch = view.change
  const location = useLocation()
  const request = ch.body || ch.title
  const lines = request.split('\n')
  const full = location.hash === '#request-text'
  // The request is fixed once work starts: it is what the work was asked to do. Only the ticket link stays editable.
  const started = !!view.detail
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
  return (
    <div id="request">
      <div className="ch-story-line">
        <span>{ch.openedBy ? `by ${ch.openedBy}` : 'Requested'}</span><button className="link" disabled={!editing && !current} onClick={() => (editing ? discard() : open())}>{editing ? 'Cancel' : started ? (ch.externalRef ? 'Edit ticket link' : 'Add ticket link') : 'Edit'}</button>
        <time dateTime={ch.createdAt}>{fmtStamp(ch.createdAt)}</time>
      </div>
      {!editing && <div id={full ? 'request-text' : undefined} className="ch-request">{full ? request : lines.slice(0, 4).join('\n')}{!full && lines.length > 4 && <><br /><Link to={`${location.pathname}?tab=details#request-text`}>full request</Link></>}</div>}
      {!editing && ch.externalRef && <p className="muted small">Ticket: {ch.externalRef}</p>}
        {editing && (
          <div style={{ padding: '12px 0' }}>
            {!started && <textarea rows={4} style={{ width: '100%' }} value={draft.body} disabled={saving} aria-label="what was asked for" onChange={(e) => setDraft((d) => ({ ...d, body: e.target.value }))} />}
            <div className="row" style={{ marginTop: 6, flexWrap: 'wrap' }}>
              <input placeholder="Ticket key or link" value={draft.externalRef} disabled={saving} onChange={(e) => setDraft((d) => ({ ...d, externalRef: e.target.value }))} />
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

  // A finished change with nothing attached has no attachments section; a
  // list never read is not an empty one, so that still says why.
  if (readOnly && itemsQ.data && items.length === 0 && !itemsQ.refreshFailed) return null
  const attach = !readOnly && (
    <label className="link ch-attach">
      attach a file →
      <input type="file" style={{ display: 'none' }} disabled={busy}
        onChange={(e) => { const f = e.target.files?.[0]; if (f) upload(f); e.target.value = '' }} />
    </label>
  )
  return (
    <tr><th scope="row">Attachments</th><td>{attach}
      <ReadStatus q={itemsQ} what="the attachments" className="small" />
      <StaleNote q={itemsQ} />
      {items.map((i) => (
        <div key={i.name} className="card row" style={{ display: 'flex' }}>
          <a style={{ flex: 1, minWidth: 0, overflowWrap: 'anywhere' }} href={`/api/v1/changes/${changeId}/attachments/${encodeURIComponent(i.name)}`} target="_blank" rel="noreferrer">{i.name}</a>
          <span className="muted small">{(i.bytes / 1024).toFixed(1)} KB</span>
          {!readOnly && <span className="row-action"><button className="subtle" aria-label={`remove ${i.name}`} disabled={busy} onClick={() => remove(i.name)}>Remove</button></span>}
        </div>
      ))}
      {err && <p className="error small">{err}</p>}
    </td></tr>
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
  return (
    <section>
      <h4>The latest run's workspace (run {number})</h4>
      <div className="row" style={{ marginBottom: 6 }}>
        <a href={`/api/v1/runs/${runId}/workspace/`} target="_blank" rel="noreferrer" className="btn">Browse the workspace ↗</a>
      </div>
      <RunDiffBody runId={runId} />
    </section>
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
        : <div style={{ maxHeight: 560, overflowY: 'auto' }}><DiffView raw={diff} unfolded /></div>}
  </>
}

// Overview excerpts link to the complete text in Details.
export function SoFarNote({ text, href, label = 'Read the full report →', plain = false }: { plain?: boolean; text: string; href?: string; label?: string }) {
  const lines = text.split('\n')
  const clipped = !!href && (text.length > 520 || lines.length > 4)
  const shown = clipped ? lines.slice(0, 4).join('\n').slice(0, 520) + '…' : text
  return <div>
    <>{plain ? <div className="ch-request">{shown}</div> : <div className="md-doc" dangerouslySetInnerHTML={{ __html: renderMd(shown, true) }} />}</>
    {clipped && <a href={href}>{label}</a>}
  </div>
}
