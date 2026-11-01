import { PRODUCT } from '../brand'
import { useEffect, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { renderMd } from '../md'
import { BlockView, patch } from '../api'
import { fmtSize, sizeTitle } from '../format'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'

export default function GuidanceBlockPage() {
  const { id } = useParams()
  const [params, setParams] = useSearchParams()
  const [editing, setEditing] = useState(false)
  const [body, setBody] = useState('')
  const [tags, setTags] = useState('')
  const { run, busy, err: actErr } = useAction()

  const version = params.get('version') || ''
  const viewQ = useGet<BlockView>(`/guidance/blocks/${encodeURIComponent(id ?? "")}${version ? `?version=${version}` : ''}`)
  const view = viewQ.data
  const load = viewQ.reload
  // The editor starts only from an answer this page read itself (a held one
  // may be old), and follows each newer reading until someone edits. A save
  // is last-write-wins on the tags; a body save adds a version, so text it
  // replaced stays readable as the version before (design/architecture/web-data.md).
  const current = viewQ.current
  useEffect(() => {
    if (!view || editing || !current) return
    setBody(view.block.bodyMd || ''); setTags(view.block.tags)
  }, [view, editing, current])

  if (!view) return <div><h1>{viewQ.err && !viewQ.denied ? 'Guidance unavailable' : 'Guidance'}</h1>
    <ReadStatus q={viewQ} what="the guidance block" />
    {viewQ.err && <p>This guidance could not be opened. Choose it from the library.</p>}
    <Link to="/guidance">Back to guidance</Link></div>

  const { block, shownVersion, versions, usedBy } = view
  const viewingOld = shownVersion !== block.latestVersion

  const save = () => run(async () => {
    await patch(`/guidance/blocks/${block.id}`, { bodyMd: body, tags })
    setEditing(false)
    setParams({})
  })

  return (
    <div>
      <div className="muted small"><Link to="/guidance">Guidance</Link></div>
      <div>
        <h1 style={{ marginBottom: 0 }}>{block.name.split('/').at(-1)!.replaceAll('-', ' ')}</h1>
        <details><summary>Technical details</summary><span className="badge">v{shownVersion}{viewingOld ? ` (latest is v${block.latestVersion})` : ''}</span>
        <span className="badge" title={sizeTitle(block.bytes)}>{fmtSize(block.bytes)}</span>
        {block.origin === 'stock'
          ? <span className="badge" title={`Ships with ${PRODUCT}; read-only, updates with each release`}>{PRODUCT}</span>
          : <span className="badge" title="You authored this · editable">yours</span>}
      <div className="muted small" style={{ marginBottom: 12 }}>
        <p><code>{block.name}</code></p>tags: {block.tags || '—'} · versions:{' '}
        {versions.map((v) => (
          <button key={v.version} className="subtle"
            onClick={() => setParams(v.version === block.latestVersion ? {} : { version: String(v.version) })}>
            v{v.version}
          </button>
        ))}
      </div>
      {usedBy.length > 0 && (
        <div className="muted small" style={{ marginBottom: 12 }}>
          imported by: {usedBy.map((u, i) => (
            <span key={i}>
              {i > 0 && ', '}
              <Link to={`/projects/${u.projectSlug}`}>{u.projectSlug}</Link> (v{u.version}{u.disabled ? ', disabled' : ''})
            </span>
          ))}
        </div>
      )}
      </details></div>
      <div className="row" style={{ marginBottom: 12 }}>
        {!editing && !viewingOld && block.origin !== 'stock' &&
          <button disabled={!current} title={current ? undefined : 'Reading the current text first'} onClick={() => setEditing(true)}>Edit (creates v{block.latestVersion + 1})</button>}
        {!viewingOld && block.origin === 'stock' &&
          <details className="muted small"><summary>Included with {PRODUCT} · Read-only</summary>To change it, write your own guidance and use it in a pack instead.</details>}
        {viewingOld && <span className="muted small">an older version, read-only</span>}
      </div>
      <StaleNote q={viewQ} />
      {actErr && <div className="error">{actErr}</div>}

      {editing ? (
        <div>
          <div className="form-grid" style={{ marginBottom: 8, maxWidth: 700 }}>
            <label>Tags</label>
            <input value={tags} onChange={(e) => setTags(e.target.value)} />
          </div>
          <textarea rows={16} value={body} onChange={(e) => setBody(e.target.value)}
            placeholder="One convention, stated plainly. Agents receive this text exactly as written." />
          <div className="row" style={{ marginTop: 8 }}>
            <button className="primary" disabled={busy} onClick={() => void save()}>Save as v{block.latestVersion + 1}</button>
            <button onClick={() => { setEditing(false); load() }}>Cancel</button>
          </div>
          <p className="muted small">Saving creates a version. Projects update separately.</p>
        </div>
      ) : (
        <details className="card"><summary>Read guidance</summary><div className="md" dangerouslySetInnerHTML={{ __html: renderMd(block.bodyMd || '_empty block_') }} /></details>
      )}

      {!editing && (view.checks ?? []).length > 0 && (
        <details className="card"><summary>Checks ({view.checks?.length})</summary>
          <div className="row" style={{ marginBottom: 6 }}>
            <strong>Checks</strong>
            <span className="muted small">checked on every change; versioned with this text</span>
          </div>
          {(view.checks ?? []).map((c) => (
            <div key={c.name} style={{ marginTop: 8 }}>
              <div className="row">
                <span className="badge">{c.severity}</span>
                <strong className="small">{c.title || c.name}</strong>{c.title && <code className="muted small">{c.name}</code>}
                <span className="badge">{c.evaluator}</span>
                <span className="muted small">
                  {c.evaluator === 'llm' && (c.scope ?? 'diff') === 'diff'
                    ? 'judged at landing only, on the change\'s diff · advisory, never blocks'
                    : c.evaluator === 'llm' ? 'judged at landing and in every review · advisory, never blocks'
                    : 'judged at landing and in every review'}
                </span>
              </div>
              <div className="muted small" style={{ marginTop: 2 }}>{c.expects}</div>
              {c.sh && <pre className="log" style={{ maxHeight: 120, marginTop: 4 }}>{c.sh}</pre>}
              {c.tool && <div className="small mono" style={{ marginTop: 4 }}>tool: {c.tool}</div>}
              {c.rubric && <div className="small" style={{ marginTop: 4, fontStyle: 'italic' }}>{c.rubric}</div>}
              {c.files && c.files.length > 0 && <div className="muted small mono" style={{ marginTop: 2 }}>judged on: {c.files.join(', ')}</div>}
            </div>
          ))}
        </details>
      )}
    </div>
  )
}
