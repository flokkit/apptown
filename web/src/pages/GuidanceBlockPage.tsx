import { usePageName } from '../chrome'
import { ProjectCount } from './DependentProjects'
import './GuidanceBlockPage.css'
import { PRODUCT } from '../brand'
import { useEffect, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { renderMd } from '../md'
import { BlockView, patch } from '../api'
import { fmtSize, sizeTitle } from '../format'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { Section } from '../Section'

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

  usePageName(`/guidance/${id}`, view?.block.name)
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

  const checks = view.checks ?? []
  const canEdit = !editing && !viewingOld && block.origin !== 'stock'
  return (
    <div>
      <div className="row guidance-block-page-div">
        <h1 className="grow guidance-block-page-h1">{block.name.split('/').at(-1)!.replaceAll('-', ' ')}</h1>
        {canEdit && <button disabled={!current} title={current ? undefined : 'Reading the current text first'} onClick={() => setEditing(true)}>Edit</button>}
      </div>
      {/* What it is, in one line: its full name, version, size, who ships it and its tags. */}
      <p className="muted small mt-1" >
        <code>{block.name}</code> · v{shownVersion}{viewingOld ? ` (latest is v${block.latestVersion})` : ''}
        {' · '}<span title={sizeTitle(block.bytes)}>{fmtSize(block.bytes)} per step</span>
        {' · '}{block.origin === 'stock' ? `included with ${PRODUCT}, read-only` : 'yours'}
        {block.tags && <> · tags: {block.tags.split(',').join(', ')}</>}
      </p>
      {block.origin === 'stock' && !viewingOld && <p className="muted small">To change it, write your own guidance and use it in a pack instead.</p>}
      <StaleNote q={viewQ} />
      {actErr && <div className="error">{actErr}</div>}

      {editing ? (
        <div>
          <div className="form-grid mb-2 guidance-block-page-form-grid">
            <label>Tags</label>
            <input aria-label="Tags" value={tags} onChange={(e) => setTags(e.target.value)} />
          </div>
          <textarea aria-label="Guidance" rows={16} value={body} onChange={(e) => setBody(e.target.value)}
            placeholder="One convention, stated plainly. Agents receive this text exactly as written." />
          <div className="row mt-2" >
            <button className="primary" disabled={busy} onClick={() => void save()}>Save as v{block.latestVersion + 1}</button>
            <button onClick={() => { setEditing(false); load() }}>Cancel</button>
          </div>
          <p className="muted small">Saving creates a version. Projects update separately.</p>
        </div>
      ) : (
        <Section title="Text">
          <div className="md" dangerouslySetInnerHTML={{ __html: renderMd(block.bodyMd || '_No text yet._') }} />
        </Section>
      )}

      {versions.length > 1 && (
        <Section title="Versions" count={versions.length}>
          {versions.map((v) => (
            <button key={v.version} className="card row guidance-block-page-card"  aria-pressed={v.version === shownVersion}
              onClick={() => setParams(v.version === block.latestVersion ? {} : { version: String(v.version) })}>
              <strong>v{v.version}</strong>{v.version === block.latestVersion && <span className="muted small">latest</span>}
              {v.version === shownVersion && <span className="muted small">shown</span>}
            </button>
          ))}
        </Section>
      )}

      <p><ProjectCount to={`/guidance/${id}/projects`} count={usedBy.length} /></p>

      {!editing && checks.length > 0 && (
        <Section title="Checks" count={checks.length} note="Checked on every change; versioned with this text.">
          {checks.map((c) => (
            <div key={c.name} className="card">
              <div className="row guidance-block-page-div-2">
                <strong className="small">{c.title || c.name}</strong>
                <span className="badge">{c.severity}</span>
                <span className="muted small">
                  {c.evaluator === 'llm' && (c.scope ?? 'diff') === 'diff'
                    ? 'judged at landing only, on the change\'s diff · advisory, never blocks'
                    : c.evaluator === 'llm' ? 'judged at landing and in every review · advisory, never blocks'
                    : 'judged at landing and in every review'}
                </span>
              </div>
              <div className="muted small guidance-block-page-div-3">{c.expects}</div>
              {c.sh && <pre className="log mt-1 guidance-block-page-log">{c.sh}</pre>}
              {c.tool && <div className="small mono mt-1" >tool: {c.tool}</div>}
              {c.rubric && <div className="small mt-1 guidance-block-page-div-4">{c.rubric}</div>}
              {c.files && c.files.length> 0 && <div className="muted small mono guidance-block-page-div-3">judged on: {c.files.join(', ')}</div>}
            </div>
          ))}
        </Section>
      )}
    </div>
  )
}
