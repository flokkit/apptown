import { PRODUCT } from '../brand'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { del, post } from '../api'
import { QuietConfirm } from '../QuietConfirm'
import { fmtWhen } from '../format'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { ComplaintList, ComplaintSteeringText } from '../wire.gen'
import { useHolds } from '../instancePages'

// Review reports about Forge, answer them and archive resolved items. Filing a
// complaint does not automatically start repair work.



function AnswerBox({ id }: { id: string }) {
  const [open, setOpen] = useState(false)
  const [text, setText] = useState('')
  const { run, busy, err } = useAction(() => setOpen(false))
  if (!open) return <div style={{ marginTop: 6 }}><button className="subtle" onClick={() => setOpen(true)}>↳ answer…</button></div>
  return (
    <div className="row" style={{ marginTop: 8, gap: 8 }}>
      <input value={text} placeholder="what was done about it (goes on the record)" style={{ flex: 1 }}
        onChange={(e) => setText(e.target.value)} autoFocus />
      <button className="primary" disabled={busy || !text.trim()} onClick={() => void run(() => post(`/complaints/${id}/answer`, { answer: text.trim() }))}>{busy ? '…' : 'answer'}</button>
      <button className="subtle" onClick={() => setOpen(false)}>cancel</button>
      {err && <span className="error small">{err}</span>}
    </div>
  )
}

// Three states: New (nobody has dealt with it), Answered (what was done
// about it is on the record, waiting to be read), Archived (out of the
// way). New → Answered → Archived, or straight New → Archived. Delete is
// the only thing that removes the text; the ledger keeps a line, and the
// AppTown deletes anything older than ninety days by itself.
type Tab = 'new' | 'answered' | 'archived'
export default function Complaints({ tab = 'new' }: { tab?: Tab }) {
  const rowsQ = useGet<ComplaintList>('/complaints')
  const rows = rowsQ.data?.complaints ?? []
  const [showSteering, setShowSteering] = useState(false)
  const steeringQ = useGet<ComplaintSteeringText>(showSteering ? '/complaints/steering' : null)
  const steering = steeringQ.data
  // Answering, archiving and deleting are the administrator's.
  const canEdit = useHolds('identity:manage-auth')
  const { run, busy, err: actErr } = useAction()

  const toggleSteering = () => setShowSteering(!showSteering)
  const markSeen = () => void run(() => post('/complaints/seen', {}))
  const fresh = rows.filter((r) => r.state === 'new')
  const answered = rows.filter((r) => r.state === 'answered')
  const archived = rows.filter((r) => r.state === 'archived')
  const older = [...fresh, ...answered].filter((r) => Date.now() - new Date(r.at).getTime() > 24 * 3600 * 1000).length
  const shown = tab === 'archived' ? archived : tab === 'answered' ? answered : fresh
  const unseen = fresh.filter((r) => !r.seen).length

  if (!rowsQ.data) return <ReadStatus q={rowsQ} what="the complaints" />
  return (
    <div style={{ maxWidth: 900 }}>
      <StaleNote q={rowsQ} />
      <div className="row">
        <h1 style={{ marginBottom: 0 }}>Complaints</h1>
        {unseen > 0 && <span className="chip pending">{unseen} unread</span>}
        <span style={{ flex: 1 }} />
        <button className="subtle" onClick={toggleSteering}>
          {showSteering ? 'hide the instructions' : 'what agents are told'}
        </button>
        {unseen > 0 && <button className="subtle" disabled={busy} onClick={markSeen}>mark all read</button>}
        {canEdit && tab !== 'archived' && older > 0 && (
          <QuietConfirm label={`archive older than a day (${older})`} className="subtle"
            ask={`Move ${older} complaint${older === 1 ? '' : 's'} older than a day to the archive? Nothing is deleted.`}
            onYes={() => void run(() => post('/complaints/archive-older', { hours: 24 }))} />
        )}
      </div>
      <p className="muted" style={{ maxWidth: 720, marginTop: 4 }}>
        Agents file these when {PRODUCT} gets in their way. Filing one does not start any work.
      </p>
      {actErr && <div className="error">{actErr}</div>}
      {showSteering && (
        <div className="card" style={{ marginBottom: 12 }}>
          {!steering && <ReadStatus q={steeringQ} what="the instructions agents are given" />}
          {steering && (
            <>
              <p className="muted small" style={{ marginTop: 0 }}>{steering.note}</p>
              <div className="small" style={{ fontWeight: 600 }}>Given to every workflow step:</div>
              <pre style={{ whiteSpace: 'pre-wrap', fontSize: 'var(--fs-xs)', overflowX: 'auto' }}>{steering.step.trim()}</pre>
              <div className="small" style={{ fontWeight: 600, marginTop: 8 }}>Given to every chat session (inside its ground rules):</div>
              <pre style={{ whiteSpace: 'pre-wrap', fontSize: 'var(--fs-xs)', overflowX: 'auto' }}>{steering.console.trim()}</pre>
            </>
          )}
        </div>
      )}
      <div className="tabs">
        <Link to="/complaints" className={`tab${tab === 'new' ? ' active' : ''}`}>New{fresh.length > 0 ? ` (${fresh.length})` : ''}</Link>
        <Link to="/complaints/answered" className={`tab${tab === 'answered' ? ' active' : ''}`}>Answered{answered.length > 0 ? ` (${answered.length})` : ''}</Link>
        <Link to="/complaints/archive" className={`tab${tab === 'archived' ? ' active' : ''}`}>Archived{archived.length > 0 ? ` (${archived.length})` : ''}</Link>
      </div>
      {shown.length === 0 && (
        tab === 'archived' ? <div className="muted">Nothing archived. Archived complaints are deleted after ninety days.</div>
        : tab === 'answered' ? <div className="muted">Nothing answered and waiting to be read. Answers stay here until someone archives them.</div>
        : <div className="muted">{rows.length === 0 ? 'No complaints.' : 'No new complaints.'}</div>)}
      {shown.map((c) => (
        <div key={c.id} className="card" style={{ marginBottom: 8, opacity: c.seen ? 0.65 : 1 }}>
          <div className="row">
            <span className="chip">{c.source}</span>
            {c.project && <Link className="small" to={`/projects/${c.project}`}>{c.projectName || c.project}</Link>}
            {c.workflow && <span className="muted small">{c.workflow}{c.step ? ` · ${c.step}` : ''}{c.runNumber ? ` · run ${c.runNumber}` : ''}</span>}
            {c.source === 'console' && <span className="muted small">chat</span>}
            {!!c.changeId && <Link className="small" to={`/changes/${c.changeId}`}>{c.changeNumber ? `change ${c.project ? `${c.project}#` : ''}${c.changeNumber}` : 'the change'}</Link>}
            {c.about && c.about !== 'apptown' && <span className="chip">about the {c.about === 'way-of-working' ? `way of working${c.packVersion ? ` (${c.packVersion})` : ''}` : 'project'}</span>}
            {!!c.evalId && <Link className="small" to={`/evals/${c.evalId}`}>from an eval</Link>}
            <span style={{ flex: 1 }} />
            <span className="muted small">{c.actor && c.actor !== 'anonymous' ? `${c.actor} · ` : ''}{fmtWhen(c.at)}</span>
          </div>
          <div style={{ marginTop: 6, fontWeight: c.seen ? 400 : 600 }}>{c.text}</div>
          {c.why && <div className="muted small" style={{ marginTop: 4 }}><strong>why:</strong> {c.why}</div>}
          {c.wish && <div className="muted small" style={{ marginTop: 2 }}><strong>wish:</strong> {c.wish}</div>}
          {c.answer && (
            <div className="small" style={{ marginTop: 8, padding: '8px 10px', borderLeft: '3px solid var(--ok)', background: 'var(--ok-bg)' }}>
              <strong>↳ answered</strong> <span className="muted">by {c.answeredBy} · {c.answeredAt ? fmtWhen(c.answeredAt) : ''}</span>
              <div style={{ marginTop: 4 }}>{c.answer}</div>
            </div>
          )}
          {c.state === 'new' && <AnswerBox id={c.id!} />}
          {c.state === 'archived' && <div className="muted small" style={{ marginTop: 6 }}>archived{c.answer ? '' : ' without an answer'} · {fmtWhen(c.archivedAt!)}</div>}
          {canEdit && (
            <div className="row small" style={{ marginTop: 6, gap: 12 }}>
              {c.state !== 'archived' && <button className="subtle" disabled={busy} onClick={() => void run(() => post(`/complaints/${c.id}/archive`, {}))}>archive</button>}
              <QuietConfirm label="delete" ask="Delete this complaint for good? The text goes; the record keeps a line saying it was deleted."
                onYes={() => void run(() => del(`/complaints/${c.id}`))} />
            </div>
          )}
        </div>
      ))}
    </div>
  )
}
