import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { renderMd } from '../md'
import { Adr, HttpError, patch, post } from '../api'
import { setProjectScope } from '../projectScope'
import { setPageContext } from '../chrome'
import { QuietConfirm } from '../QuietConfirm'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'

// Text is an ADR's editable text.
type Text = { title: string; bodyMd: string; tags: string }

type AdrOptions = { contentEditable: boolean; canSupersede: boolean; transitions: string[] }

const confirmTransition: Record<string, string> = {
  deprecated: 'Deprecate this decision? Checks that require it will stop counting it.',
}

export default function AdrPage() {
  const { id } = useParams()
  const nav = useNavigate()
  const adrQ = useGet<{ adr: Adr; supersededById: string | null; options: AdrOptions }>(`/adrs/${id}`)
  const adr = adrQ.data?.adr ?? null
  const supersededBy = adrQ.data?.supersededById ?? null
  const options = adrQ.data?.options ?? null
  const load = adrQ.reload
  const [editing, setEditing] = useState(false)
  const [body, setBody] = useState('')
  const [title, setTitle] = useState('')
  const [tags, setTags] = useState('')
  // start is the text the draft began from: the save names it for each field
  // it changes, and AppTown refuses the save if that text was replaced since.
  const [start, setStart] = useState<Text | null>(null)
  const { run, busy, err: actErr, failure, setErr } = useAction()
  const moved = failure instanceof HttpError && failure.code === 'adr-text-moved'
  useEffect(() => {
    if (!adr) return
    setProjectScope(adr.projectSlug ?? null)
    setPageContext({ crumbs: [{ label: `Decision ${String(adr.number).padStart(3, '0')} · ${adr.title}` }], chip: { text: adr.status, cls: adr.status } })
  }, [adr])
  // The editor starts only from an answer this page read itself (a held one
  // may be old), and follows each newer reading until someone edits. A save
  // sends only the fields that changed, each with the text it started from
  // (design/architecture/web-data.md, guarded editors).
  const current = adrQ.current
  useEffect(() => {
    if (!adr || editing || !current) return
    setBody(adr.bodyMd); setTitle(adr.title); setTags(adr.tags)
  }, [adr, editing, current])
  // A refused save reads the decision again, so its current text is shown.
  const reload = adrQ.reload
  useEffect(() => { if (moved) void reload() }, [moved, reload])
  useEffect(() => () => setPageContext({}), [])

  if (!adr || !options) return <ReadStatus q={adrQ} what="the decision" />

  const locked = !options.contentEditable

  const startEditing = () => { setStart({ title: adr.title, bodyMd: adr.bodyMd, tags: adr.tags }); setEditing(true) }
  const save = () => run(async () => {
    const from = start ?? { title: adr.title, bodyMd: adr.bodyMd, tags: adr.tags }
    const draft: Text = { title, bodyMd: body, tags }
    const changed = (Object.keys(draft) as (keyof Text)[]).filter((k) => draft[k] !== from[k])
    if (changed.length > 0) {
      const pick = (t: Text) => Object.fromEntries(changed.map((k) => [k, t[k]]))
      await patch(`/adrs/${adr.id}`, { ...pick(draft), expected: pick(from) })
    }
    setEditing(false)
  })
  // Keep my edits: the draft now starts from the current text, so the next
  // save replaces it knowingly. Discard: back to the current text.
  const keepMine = () => { setStart({ title: adr.title, bodyMd: adr.bodyMd, tags: adr.tags }); setErr('') }
  const discard = () => { setErr(''); setEditing(false) }
  const transition = (to: string) => run(() => patch(`/adrs/${adr.id}`, { status: to }))
  const supersede = () => run(async () => {
    const next = await post<Adr>(`/adrs/${adr.id}/supersede`)
    nav(`/adrs/${next.id}`)
  })

  return (
    <div>
      <div className="muted small">
        <Link to={`/projects/${adr.projectSlug}/settings/decisions`}>{adr.projectSlug} · decisions</Link>
      </div>
      <div className="row">
        <h1 style={{ marginBottom: 0 }}>ADR-{String(adr.number).padStart(3, '0')} — {adr.title}</h1>
        <span className={`chip ${adr.status}`}>{adr.status}</span>
      </div>
      <div className="muted small" style={{ marginBottom: 12 }}>
        tags: {adr.tags || '—'} · updated {adr.updatedAt.slice(0, 10)}
        {adr.changeId && <> · linked to <Link to={`/changes/${adr.changeId}`}>{adr.changeNumber ? `change ${adr.changeNumber}` : 'a change'}</Link></>}
        {adr.supersedesId && <> · supersedes <Link to={`/adrs/${adr.supersedesId}`}>ADR</Link></>}
        {supersededBy && <> · superseded by <Link to={`/adrs/${supersededBy}`}>ADR</Link></>}
      </div>

      <div className="row" style={{ marginBottom: 16 }}>
        {options.transitions.map((t) => confirmTransition[t]
          ? <QuietConfirm key={t} label={`→ ${t}`} className="" ask={confirmTransition[t]} onYes={() => void transition(t)} />
          : (
            <button key={t} className={t === 'accepted' ? 'primary' : ''} disabled={busy} onClick={() => void transition(t)}>
              → {t}
            </button>
          ))}
        {options.canSupersede && <button disabled={busy} onClick={() => void supersede()}>Supersede…</button>}
        {!locked && !editing && <button disabled={!current} title={current ? undefined : 'Reading the current text first'} onClick={startEditing}>Edit</button>}
        {locked && <span className="muted small">a decision that is {adr.status} cannot be edited; supersede it instead</span>}
      </div>
      <StaleNote q={adrQ} />
      {actErr && !moved && <div className="error">{actErr}</div>}
      {editing && moved && (
        <div className="card tone-bad">
          <div className="error">{actErr}</div>
          <div className="muted small">The decision as it is now:</div>
          <div className="md" dangerouslySetInnerHTML={{ __html: renderMd(`**${adr.title}** (tags: ${adr.tags || '—'})\n\n${adr.bodyMd}`) }} />
          <div className="row mt-2">
            <button disabled={!current} onClick={keepMine}>Keep my edits</button>
            <button onClick={discard}>Discard mine</button>
          </div>
        </div>
      )}

      {editing ? (
        <div>
          <div className="form-grid" style={{ marginBottom: 8, maxWidth: 700 }}>
            <label>Title</label>
            <input value={title} onChange={(e) => setTitle(e.target.value)} />
            <label>Tags</label>
            <input value={tags} onChange={(e) => setTags(e.target.value)} placeholder="comma,separated" />
          </div>
          <textarea rows={18} value={body} onChange={(e) => setBody(e.target.value)} placeholder="# Context&#10;…&#10;# Decision&#10;…&#10;# Consequences&#10;…" />
          <div className="row" style={{ marginTop: 8 }}>
            <button className="primary" disabled={busy || moved} onClick={() => void save()}>Save</button>
            <button onClick={() => { setEditing(false); load() }}>Cancel</button>
          </div>
        </div>
      ) : (
        <div className="card md" dangerouslySetInnerHTML={{ __html: renderMd(adr.bodyMd || '_no content yet_') }} />
      )}
    </div>
  )
}
