import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { post } from '../../api'
import { useAction } from '../../data'
import type { CatalogApplied, ProjectNotesView } from '../../wire.gen'

export function ProjectNotes({ slug, notes, writable }: { slug: string; notes: ProjectNotesView; writable: boolean }) {
  const nav = useNavigate()
  const { run, busy, err } = useAction()
  const [adding, setAdding] = useState(false)
  const [text, setText] = useState('')
  const apply = async (name: string, params: Record<string, string> = {}) => {
    const result = await run(() => post<CatalogApplied>(`/catalog-apply/${name}`, { project: slug, params }))
    if (result) nav(`/changes/${result.change.id}`)
  }
  return <section className="card" aria-label="What agents know about this project">
    <h2>What agents know about this project</h2>
    {notes.error ? <p className="error">{notes.error}</p> : <>
      {notes.notes.length === 0 ? <p>No notes yet</p> : <ul>{notes.notes.map((note, i) => <li key={`${note.line}-${i}`}>
        <p>{note.text} <span className="muted">({note.author} · {note.changeId ? <Link to={`/changes/${note.changeId}`}>change {note.change}</Link> : `change ${note.change}`})</span></p>
        <p className="small muted">{note.sourceError || (note.acceptedBy.length ? `Accepted by ${note.acceptedBy.join(', ')}` : 'No recorded acceptance')}</p>
        {writable && <button disabled={busy} onClick={() => apply('notes/remove', { line: note.line })}>Remove</button>}
      </li>)}</ul>}
      {writable && (!notes.present ? <button disabled={busy} onClick={() => apply('notes/start')}>Write this project's notes</button> : <button disabled={busy} onClick={() => setAdding(true)}>Add a note</button>)}
      {writable && adding && <form onSubmit={e => { e.preventDefault(); if (text.trim()) void apply('notes/add', { text: text.trim() }) }}>
        <label>Project fact or preference<textarea value={text} onChange={e => setText(e.target.value)} maxLength={1024} required /></label>
        <p className="small muted">This starts a change for review. Accepting the change accepts the note.</p>
        <button disabled={busy || !text.trim()} type="submit">Propose note</button>
        <button type="button" disabled={busy} onClick={() => setAdding(false)}>Cancel</button>
      </form>}
    </>}
    {err && <p className="error">{err}</p>}
  </section>
}
