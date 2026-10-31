import { PRODUCT } from '../brand'
import { FormEvent, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { GuidanceBlock, post } from '../api'
import { fmtSize, sizeTitle } from '../format'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { GeneratedGuidance } from '../wire.gen'

// Machine-written steering sections, rendered live — the library shows
// EVERYTHING that steers agents, authored and generated alike, each with
// what it costs an agent's context.
function GeneratedGuidance() {
  const q = useGet<GeneratedGuidance>('/guidance/generated')
  const data = q.data
  const [open, setOpen] = useState<string | null>(null)
  if (!data) return <div style={{ marginTop: 24 }}><ReadStatus q={q} what="the generated guidance" /></div>
  const gens = data.generated ?? [], always = data.alwaysBytes ?? 0, conditional = data.conditionalBytes ?? 0
  if (gens.length === 0) return null
  return (
    <div style={{ marginTop: 24 }}>
      <StaleNote q={q} />
      <div className="conn-section-head">
        <h2>Generated</h2>
        <span className="muted small">From instance settings</span>
      </div>
      <div className="muted small" style={{ marginBottom: 8 }}>
        <span title={sizeTitle(always)}>{fmtSize(always)}</span> per step
        {conditional > 0 && <> · up to <span title={sizeTitle(conditional)}>{fmtSize(conditional)}</span> conditional</>}
      </div>
      {gens.map((g) => (
        <div key={g.name} className="card" style={{ borderStyle: 'dashed' }}>
          <div className="row">
            <strong>{g.name}</strong>
            <span className="chip" title={g.always ? 'in every agent step' : 'only in some steps · the note says which'}>{g.always ? 'every step' : 'some steps'}</span>
            <span className="muted small" title={sizeTitle(g.bytes)}>{fmtSize(g.bytes)}</span>
            <span className="muted small">from {g.source}</span>
            <span style={{ flex: 1 }} />
            <button className="subtle" onClick={() => setOpen(open === g.name ? null : g.name)}>
              {open === g.name ? 'hide' : 'view rendered'}
            </button>
          </div>
          {open === g.name && (
            <>
              <pre style={{ whiteSpace: 'pre-wrap', fontSize: 'var(--fs-xs)', marginTop: 8 }}>{(g.bodyMd ?? '').trim()}</pre>
              <div className="muted small" style={{ marginTop: 6 }}>{g.note}</div>
            </>
          )}
        </div>
      ))}
    </div>
  )
}

export default function GuidanceLibrary() {
  const [q, setQ] = useState('')
  const [tag, setTag] = useState('')
  const [src, setSrc] = useState<'all' | 'stock' | 'mine'>('all')
  const [name, setName] = useState('')
  const [tags, setTags] = useState('')
  const { run, busy, err: actErr } = useAction()
  // Typing settles for 200ms before the search is asked.
  const [search, setSearch] = useState('')
  useEffect(() => {
    const params = new URLSearchParams()
    if (q) params.set('q', q)
    if (tag) params.set('tag', tag)
    const t = setTimeout(() => setSearch(params.toString()), 200)
    return () => clearTimeout(t)
  }, [q, tag])
  const blocksQ = useGet<GuidanceBlock[]>(`/guidance/blocks?${search}`)
  const blocks = blocksQ.data ?? []

  const create = (e: FormEvent) => {
    e.preventDefault()
    void run(async () => {
      await post('/guidance/blocks', { name, tags, bodyMd: '' })
      setName(''); setTags('')
    })
  }

  const shown = blocks.filter((b) => src === 'all' || (src === 'stock') === (b.origin === 'stock'))
  const [limit, setLimit] = useState(10)

  return (
    <div>
      <div className="row" style={{ marginBottom: 16 }}>
        <input placeholder="search name & body…" value={q} onChange={(e) => setQ(e.target.value)} style={{ width: 260 }} />
        <input placeholder="tag" value={tag} onChange={(e) => setTag(e.target.value)} style={{ width: 120 }} />
        <span style={{ flex: 1 }} />
        <div className="row" style={{ gap: 4 }}>
          {(['all', 'stock', 'mine'] as const).map((s) => (
            <button key={s} className={src === s ? 'primary' : 'subtle'} onClick={() => setSrc(s)}>
              {s === 'all' ? 'All' : s === 'stock' ? PRODUCT : 'Yours'}
            </button>
          ))}
        </div>
      </div>
      {actErr && <div className="error">{actErr}</div>}
      <StaleNote q={blocksQ} />
      {blocksQ.data ? <>
      <table className="stack">
        <thead><tr><th>Guidance</th><th>Summary</th></tr></thead>
        <tbody>
          {shown.slice(0, limit).map((b) => <tr key={b.id}>
            <td className="stack-lead"><Link className="tap-open" to={`/guidance/${b.id}`}>{b.name.split('/').at(-1)!.replaceAll('-', ' ')}</Link>{b.origin !== 'stock' && <span className="chip">Custom</span>}</td>
            <td><details><summary>Summary</summary><p className="guidance-summary">{b.summary || 'No text yet'}</p></details></td>
          </tr>)}
        </tbody>
      </table>
      {blocks.length === 0 && <div className="muted" style={{ padding: 16 }}>No blocks match.</div>}
      {shown.length > limit && <button onClick={() => setLimit(n => n + 10)}>Show more ({shown.length - limit})</button>}
      </> : <ReadStatus q={blocksQ} what="the guidance blocks" />}
      <details><summary>New guidance</summary>
      <form onSubmit={create} className="row">
        <input placeholder="name, e.g. prose/terse-style" value={name} onChange={(e) => setName(e.target.value)} required style={{ width: 260 }} />
        <input placeholder="tags, comma-separated" value={tags} onChange={(e) => setTags(e.target.value)} />
        <button className="primary" type="submit" disabled={busy}>Create v1</button>
      </form></details>
      <details><summary>Automatic guidance</summary><GeneratedGuidance /></details>
    </div>
  )
}
