import { Status, STATUS_LIGHT } from '../../../Status'
import { FormEvent, useState } from 'react'
import { Link } from 'react-router-dom'
import { Adr, post } from '../../../api'
import { useAction, useGet } from '../../../data'
import { ReadGate } from '../../../ReadGate'

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
    <div>
      <ReadGate q={q} what="this project's decisions">{(adrs) => (
      <table>
        <thead><tr><th>#</th><th>Title</th><th>Status</th><th>Tags</th></tr></thead>
        <tbody>
          {adrs.map((a) => (
            <tr key={a.id}>
              <td>ADR-{String(a.number).padStart(3, '0')}</td>
              <td><Link to={`/adrs/${a.id}`}>{a.title}</Link></td>
              <td><Status word={a.statusLabel} tone={STATUS_LIGHT[a.statusLabel]} help={`Decision: ${a.status}.`} /></td>
              <td className="muted">{a.tags}</td>
            </tr>
          ))}
        </tbody>
      </table>
      )}</ReadGate>
      <h2>New decision</h2>
      <form onSubmit={create} className="row">
        <input placeholder="Title" value={title} onChange={(e) => setTitle(e.target.value)} required style={{ width: 320 }} />
        <input placeholder="tags, comma-separated" value={tags} onChange={(e) => setTags(e.target.value)} />
        <button className="primary" type="submit">Start a draft</button>
      </form>
      {err && <div className="error">{err}</div>}
    </div>
  )
}
