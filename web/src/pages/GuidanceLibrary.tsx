import { PRODUCT } from '../brand'
import { Section } from '../Section'
import { FormEvent, useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { GuidanceBlock, post } from '../api'
import { fmtSize, sizeTitle } from '../format'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { LibraryRow, LibraryRows, firstSentence } from '../LibraryRow'
import { MoreMenu } from '../MoreMenu'
import type { GeneratedGuidance } from '../wire.gen'

// Machine-written steering sections, rendered live — the library shows
// EVERYTHING that steers agents, authored and generated alike, each with
// what it costs an agent's context.
export function GeneratedGuidance({ page = false }: { page?: boolean }) {
  const { name } = useParams()
  const q = useGet<GeneratedGuidance>('/guidance/generated')
  const data = q.data
  if (!data) return <>{page && <h1>{name || 'Automatic guidance'}</h1>}<ReadStatus q={q} what="the generated guidance" /></>
  const gens = data.generated ?? [], always = data.alwaysBytes ?? 0, conditional = data.conditionalBytes ?? 0
  if (gens.length === 0) return page ? <><h1>{name || 'Automatic guidance'}</h1><p>No automatic guidance is recorded.</p></> : null
  return (
    <>{page && <h1>{name || 'Automatic guidance'}</h1>}<Section title={page ? '' : 'Automatic guidance'} count={name ? undefined : gens.length}
      note={<>Written from instance settings. <span title={sizeTitle(always)}>{fmtSize(always)}</span> per step
        {conditional > 0 && <> · up to <span title={sizeTitle(conditional)}>{fmtSize(conditional)}</span> in some steps</>}</>}>
      <StaleNote q={q} />
      <LibraryRows>
        {gens.filter(g => !page || !name || g.name === name).map((g) => (
          <LibraryRow key={g.name} name={g.name.split('/').at(-1)!.replaceAll('-', ' ')} to={page && name ? undefined : `/guidance/generated/${encodeURIComponent(g.name)}`}
            purpose={`from ${g.source}`}
            state={<span className="muted small">{g.always ? 'every step' : 'some steps'} · <span title={sizeTitle(g.bytes)}>{fmtSize(g.bytes)}</span></span>}
            detail={page && name && <>
              <pre className="log" style={{ whiteSpace: 'pre-wrap' }}>{(g.bodyMd ?? '').trim()}</pre>
              {g.note && <p className="muted small">{g.note}</p>}
            </>} />
        ))}
      </LibraryRows>
    </Section></>
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
      setName(''); setTags(''); setAdding(false)
    })
  }

  const shown = blocks.filter((b) => src === 'all' || (src === 'stock') === (b.origin === 'stock'))
  const [adding, setAdding] = useState(false)

  return (
    <div>
      {actErr && <div className="error">{actErr}</div>}
      <StaleNote q={blocksQ} />
      <Section title="Guidance" count={blocksQ.data ? shown.length : undefined}
        action={<>
          <input className="section-filter guidance-search" aria-label="Search guidance" placeholder="Search name and text" value={q} onChange={(e) => setQ(e.target.value)} />
          <button className="primary" onClick={() => setAdding(!adding)} aria-expanded={adding}>New guidance</button>
        </>}
        note={<span className="row guidance-filters">
          {(['all', 'stock', 'mine'] as const).map((s) => (
            <button key={s} className={`subtle${src === s ? ' active' : ''}`} aria-pressed={src === s} onClick={() => setSrc(s)}>
              {s === 'all' ? 'All' : s === 'stock' ? PRODUCT : 'Yours'}
            </button>
          ))}
          <input aria-label="Tag" placeholder="tag" value={tag} onChange={(e) => setTag(e.target.value)} style={{ width: 120 }} />
        </span>}>
        {adding && (
          <form onSubmit={create} className="card row" style={{ flexWrap: 'wrap' }}>
            <input aria-label="Guidance name" placeholder="name, e.g. prose/terse-style" value={name} onChange={(e) => setName(e.target.value)} required style={{ width: 260 }} autoFocus />
            <input aria-label="Guidance tags" placeholder="tags, comma-separated" value={tags} onChange={(e) => setTags(e.target.value)} />
            <button className="primary" type="submit" disabled={busy}>Create v1</button>
            <button type="button" onClick={() => setAdding(false)}>Cancel</button>
          </form>
        )}
        {!blocksQ.data ? <ReadStatus q={blocksQ} what="the guidance blocks" />
          : shown.length === 0 ? <p className="muted">No guidance matches.</p>
          : <LibraryRows>{shown.map((b) => <GuidanceRow key={b.id ?? b.name} b={b} />)}</LibraryRows>}
      </Section>
      <GeneratedGuidance />
    </div>
  )
}

// One guidance block, laid out like a workflow or pack row: the whole row
// opens its full text in place; editing and versions are on its own page.
function GuidanceRow({ b }: { b: GuidanceBlock }) {
  const nav = useNavigate()
  return (
    <LibraryRow name={b.name.split('/').at(-1)!.replaceAll('-', ' ')} to={`/guidance/${b.id}`}
      badges={b.origin !== 'stock' && <span className="badge">yours</span>}
      purpose={firstSentence(b.summary || 'No text yet')}
      menu={b.id && <MoreMenu label={b.name} items={[
        { label: 'Open its page', hint: `Versions, projects using it${b.origin !== 'stock' ? ', and editing' : ''}.`, onClick: () => nav(`/guidance/${b.id}`) },
      ]} />}
 />
  )
}
