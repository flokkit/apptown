import { Status } from '../../Status'
import { FormEvent, useState } from 'react'
import { Link } from 'react-router-dom'
import { Adr, post } from '../../api'
import { useAction, useGet } from '../../data'
import { ReadGate } from '../../ReadGate'
import { Section } from '../../Section'
import './settings/people.css'

export function AdrsTab({ slug }: { slug: string }) {
  const q = useGet<Adr[]>(`/projects/${slug}/adrs`)
  const { reload } = q
  const [title, setTitle] = useState('')
  const [tags, setTags] = useState('')
  const { run, err } = useAction(reload)

  const create = async (e: FormEvent) => {
    e.preventDefault()
    if (await run(() => post(`/projects/${slug}/adrs`, { title, tags })) !== undefined) { setTitle(''); setTags('') }
  }

  return (
    <>
      <Section title="Recorded" count={q.data?.length || undefined}>
        <ReadGate q={q} what="this project's decisions">{(adrs) => adrs.length === 0 ? <p className="muted">No decisions recorded yet.</p> : <>
          {adrs.map((a) => (
            <Link key={a.id} to={`/adrs/${a.id}`} className="card row adr-row" style={{ display: 'flex' }}>
              <span className="row-status"><Status word={a.statusLabel} help={`Decision: ${a.status}.`} /></span>
              <span className="adr-num muted small">ADR-{String(a.number).padStart(3, '0')}</span>
              <strong style={{ minWidth: 0 }} className="grow">{a.title}</strong>
              {a.tags && <span className="muted small">{a.tags}</span>}
            </Link>
          ))}
        </>}</ReadGate>
      </Section>
      <Section title="New decision">
        <form onSubmit={create} className="adr-new">
          <input aria-label="Title" placeholder="Title" value={title} onChange={(e) => setTitle(e.target.value)} required />
          <input aria-label="Tags" placeholder="tags, comma-separated" value={tags} onChange={(e) => setTags(e.target.value)} />
          <button type="submit">Start a draft</button>
        </form>
        {err && <div className="error">{err}</div>}
      </Section>
    </>
  )
}
