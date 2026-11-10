import { Tabs } from '../Tabs'
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
import { Section } from '../Section'
import './Complaints.css'

// Review reports about AppTown, answer them and archive resolved items. Filing a
// complaint does not automatically start repair work.



function AnswerBox({ id }: { id: string }) {
  const [open, setOpen] = useState(false)
  const [text, setText] = useState('')
  const { run, busy, err } = useAction(() => setOpen(false))
  if (!open) return <div className="complaints-answer-box-div"><button className="subtle" onClick={() => setOpen(true)}>↳ answer…</button></div>
  return (
    <div className="row mt-2 complaints-answer-box-div-2">
      <input aria-label="What was done about it" value={text} placeholder="what was done about it (goes on the record)"
        onChange={(e) => setText(e.target.value)} autoFocus className="grow" />
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
  const [limit, setLimit] = useState(10)
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
    <div className="complaints-div">
      <StaleNote q={rowsQ} />
      <div className="row">
        <h1 className="grow complaints-h1">Complaints</h1>
        {unseen > 0 && <button className="subtle" disabled={busy} onClick={markSeen}>Mark {unseen} read</button>}
        {canEdit && tab !== 'archived' && older > 0 && (
          <QuietConfirm label={`Archive older than a day (${older})`} className="subtle"
            ask={`Move ${older} complaint${older === 1 ? '' : 's'} older than a day to the archive? Nothing is deleted.`}
            onYes={() => void run(() => post('/complaints/archive-older', { hours: 24 }))} />
        )}
      </div>
      {actErr && <div className="error">{actErr}</div>}
      <Tabs className="tabs">
        <Link to="/complaints" className={`tab${tab === 'new' ? ' active' : ''}`}>New{fresh.length > 0 && <span className="section-count"> {fresh.length}</span>}</Link>
        <Link to="/complaints/answered" className={`tab${tab === 'answered' ? ' active' : ''}`}>Answered{answered.length > 0 && <span className="section-count"> {answered.length}</span>}</Link>
        <Link to="/complaints/archive" className={`tab${tab === 'archived' ? ' active' : ''}`}>Archived{archived.length > 0 && <span className="section-count"> {archived.length}</span>}</Link>
      </Tabs>
      <Section title={tab === 'archived' ? 'Archived' : tab === 'answered' ? 'Answered' : 'New'} count={shown.length}
        note={`Agents file these when ${PRODUCT} gets in their way. Filing one does not start any work.`}
        action={<button className="link" onClick={toggleSteering}>{showSteering ? 'Hide instructions' : 'Instructions for reporting problems'}</button>}>
      {showSteering && (
        <div className="card complaint-steering">
          {!steering && <ReadStatus q={steeringQ} what="the instructions agents are given" />}
          {steering && (
            <>
              <p className="muted small mt-0" >{steering.note}</p>
              <div className="small text-name">Given to every workflow step:</div>
              <pre className="complaints-pre">{steering.step.trim()}</pre>
              <div className="small mt-2 text-name">Given to every chat session (inside its ground rules):</div>
              <pre className="complaints-pre">{steering.console.trim()}</pre>
            </>
          )}
        </div>
      )}
      {shown.length === 0 && (
        tab === 'archived' ? <p className="muted">Nothing archived. Archived complaints are deleted after ninety days.</p>
        : tab === 'answered' ? <p className="muted">Nothing answered and waiting to be read. Answers stay here until someone archives them.</p>
        : <p className="muted">{rows.length === 0 ? 'No complaints. Agents have not reported a problem.' : 'No new complaints.'}</p>)}
      {shown.slice(0, limit).map((c) => (
        <div key={c.id} className={`card complaint${c.seen ? '' : ' complaint-unread'}`}>
          <div className="row">
            <span className="badge">{c.source}</span>
            {c.project && <Link className="small" to={`/projects/${c.project}`}>{c.projectName || c.project}</Link>}
            {c.workflow && <span className="muted small">{c.workflow}{c.step ? ` · ${c.step}` : ''}{c.runNumber ? ` · run ${c.runNumber}` : ''}</span>}
            {c.source === 'console' && <span className="muted small">chat</span>}
            {!!c.changeId && <Link className="small" to={`/changes/${c.changeId}`}>{c.changeNumber ? `change ${c.project ? `${c.project}#` : ''}${c.changeNumber}` : 'the change'}</Link>}
            {c.about && c.about !== 'apptown' && <span className="badge">about the {c.about === 'way-of-working' ? `way of working${c.packVersion ? ` (${c.packVersion})` : ''}` : 'project'}</span>}
            {!!c.evalId && <Link className="small" to={`/evals/${c.evalId}`}>from an eval</Link>}
            <span className="grow" />
            <span className="muted small">{c.actor && c.actor !== 'anonymous' ? `${c.actor} · ` : ''}{fmtWhen(c.at)}</span>
          </div>
          <div className="complaints-answer-box-div complaint-text">{c.text}</div>
          {c.why && <div className="muted small mt-1" ><strong>why:</strong> {c.why}</div>}
          {c.wish && <div className="muted small complaints-div-3"><strong>wish:</strong> {c.wish}</div>}
          {c.answer && (
            <div className="small mt-2 complaints-div-4">
              <strong>↳ answered</strong> <span className="muted">by {c.answeredBy} · {c.answeredAt ? fmtWhen(c.answeredAt) : ''}</span>
              <div className="mt-1">{c.answer}</div>
            </div>
          )}
          {c.state === 'new' && <AnswerBox id={c.id!} />}
          {c.state === 'archived' && <div className="muted small complaints-answer-box-div">archived{c.answer ? '' : ' without an answer'} · {fmtWhen(c.archivedAt!)}</div>}
          {canEdit && (
            <div className="row small complaints-div-5">
              {c.state !== 'archived' && <button className="subtle" disabled={busy} onClick={() => void run(() => post(`/complaints/${c.id}/archive`, {}))}>archive</button>}
              <QuietConfirm label="delete" ask="Delete this complaint for good? The text goes; the record keeps a line saying it was deleted."
                onYes={() => void run(() => del(`/complaints/${c.id}`))} />
            </div>
          )}
        </div>
      ))}
      {shown.length > limit && <p><button onClick={() => setLimit(n => n + 10)}>Show more ({shown.length - limit})</button></p>}
      </Section>
    </div>
  )
}
