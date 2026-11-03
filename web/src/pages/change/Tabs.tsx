import { FormEvent, useState } from 'react'
import { Link } from 'react-router-dom'
import { Status } from '../../Status'
import { renderMd } from '../../md'
import { Adr, Artifact, ChangeView, RunDetail, Step, patch, post } from '../../api'
import { useAction, useGet } from '../../data'
import type { RunTrace } from '../../wire.gen'

// The run a tab shows: the page's latest run as it polls, or another root
// run of this change read from its trace.
export function useRunDetail(view: ChangeView, runId?: string): { detail: RunDetail | null; err: string } {
  const latest = view.detail
  const other = runId != null && runId !== latest?.run.id ? runId : null
  const trace = useGet<RunTrace>(other != null ? `/runs/${other}/trace` : null)
  if (other == null) return { detail: latest, err: '' }
  const t = trace.data
  if (!t || !t.run) return { detail: null, err: trace.err }
  return {
    // A trace is the record, not the run's current offer: it carries no acts.
    detail: { run: t.run, stages: t.stages, steps: t.steps.filter(Boolean) as Step[], events: t.events as RunDetail['events'], artifacts: t.artifacts, landingMeans: [], next: [] },
    err: trace.err,
  }
}

// Full record bodies load separately from the polled change payload.
export function ArtifactBody({ artifact }: { artifact: Artifact }) {
  const truncated = (artifact.bodyBytes ?? 0) > artifact.body.length
  const fullQ = useGet<Artifact>(truncated ? `/artifacts/${artifact.id}` : null)
  const full = fullQ.data?.body ?? null
  const body = full ?? artifact.body
  const isJson = body.trimStart().startsWith('{') || body.trimStart().startsWith('[')
  return (
    <section>
      {fullQ.err && <div className="error small">Showing the first part only; the rest could not be read: {fullQ.err}</div>}
      {truncated && full === null && !fullQ.err && <div className="muted small">Reading the full body…</div>}
      {isJson || artifact.kind === 'agent-log'
        ? <pre className="log" style={{ maxHeight: 400 }}>{body}</pre>
        : <div className="md" dangerouslySetInnerHTML={{ __html: renderMd(body, true) }} />}
    </section>
  )
}

export function AdrLinks({ view, onChange }: { view: ChangeView; onChange: () => void }) {
  const { data: all } = useGet<Adr[]>(`/projects/${view.projectSlug}/adrs`)
  const [pick, setPick] = useState('')
  const [title, setTitle] = useState('')
  const { run, err } = useAction(onChange)
  const unlinked = (all ?? []).filter((a) => a.changeId !== view.change.id)
  const link = () => { if (pick) run(() => patch(`/adrs/${pick}`, { changeId: view.change.id })) }
  const createLinked = async (e: FormEvent) => {
    e.preventDefault()
    await run(() => post(`/projects/${view.projectSlug}/adrs`, { title, changeId: view.change.id }))
    setTitle('')
  }
  return (
    <div>
      {view.adrs.map((a) => (
        <div className="card row" key={a.id}>
          <Status word={a.statusLabel} />
          <Link to={`/adrs/${a.id}`}>ADR-{String(a.number).padStart(3, '0')} {a.title}</Link>
          <span className="muted small">{a.tags}</span>
        </div>
      ))}
      <div className="row" style={{ marginTop: 4 }}>
        <select value={pick} onChange={(e) => setPick(e.target.value)}>
          <option value="">link existing ADR…</option>
          {unlinked.map((a) => <option key={a.id} value={a.id ?? ''}>ADR-{String(a.number).padStart(3, '0')} {a.title} ({a.status})</option>)}
        </select>
        <button onClick={link} disabled={!pick}>Link</button>
        <form onSubmit={createLinked} className="row">
          <input placeholder="…or new ADR title" value={title} onChange={(e) => setTitle(e.target.value)} />
          <button type="submit" disabled={!title.trim()}>Create draft</button>
        </form>
      </div>
      {err && <div className="error">{err}</div>}
    </div>
  )
}

export function AddArtifact({ runId, onChange }: { runId: string; onChange: () => void }) {
  const [kind, setKind] = useState('')
  const [name, setName] = useState('')
  const [ref, setRef] = useState('')
  const { run, err } = useAction(onChange)
  const add = async (e: FormEvent) => {
    e.preventDefault()
    if (await run(() => post(`/runs/${runId}/artifacts`, { kind, name, ref })) !== undefined) { setKind(''); setName(''); setRef('') }
  }
  return (
 <form onSubmit={add} className="row" style={{ padding: '12px 0', flexWrap: 'wrap' }}>
      <input placeholder="Kind (for example notes)" value={kind} onChange={(e) => setKind(e.target.value)} required />
      <input placeholder="Name" value={name} onChange={(e) => setName(e.target.value)} />
      <input placeholder="Link (optional)" value={ref} onChange={(e) => setRef(e.target.value)} style={{ width: 220, maxWidth: '100%' }} />
      <button type="submit">Attach</button>
      {err && <span className="error">{err}</span>}
    </form>
  )
}
