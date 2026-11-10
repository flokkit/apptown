import { ListTable } from '../../ListTable'
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
          <ListTable label="Decisions" rows={adrs} rowKey={a => String(a.id)} rowTo={a => `/adrs/${a.id}`} columns={[
            { key: 'title', label: 'Decision', phone: 'name', render: a => <Link to={`/adrs/${a.id}`}><strong>{a.title}</strong></Link> },
            { key: 'state', label: 'State', phone: 'state', render: a => <Status word={a.statusLabel} help={a.statusHelp} /> },
            { key: 'number', label: 'Number', className: 'muted small', render: a => `ADR-${String(a.number).padStart(3, '0')}` },
            { key: 'tags', label: 'Tags', className: 'muted small', render: a => a.tags },
          ]} />
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
