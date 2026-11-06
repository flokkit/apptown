import { FormEvent, useState } from 'react'
import { Link } from 'react-router-dom'
import { Status } from '../../Status'
import { renderMd } from '../../md'
import { Artifact, ChangeView, RunDetail, Step, post } from '../../api'
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
        : <div className="md" dangerouslySetInnerHTML={{ __html: renderMd(body) }} />}
    </section>
  )
}

export function AdrLinks({ view }: { view: ChangeView; onChange: () => void }) {
  return <div>
    {view.adrs.map(a => <div className="card row" key={a.id}>
      <Status word={a.statusLabel} />
      <Link to={`/adrs/${a.id}`}>ADR-{String(a.number).padStart(3, '0')} {a.title}</Link>
      <span className="muted small">{a.tags}</span>
    </div>)}
    <Link to={`/projects/${view.projectSlug}/memory/decisions`}>Create or link a decision in Memory › Decisions →</Link>
  </div>
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
      <input aria-label="Record kind" placeholder="Kind (for example notes)" value={kind} onChange={(e) => setKind(e.target.value)} required />
      <input aria-label="Record name" placeholder="Name" value={name} onChange={(e) => setName(e.target.value)} />
      <input aria-label="Record link" placeholder="Link (optional)" value={ref} onChange={(e) => setRef(e.target.value)} style={{ width: 220, maxWidth: '100%' }} />
      <button type="submit">Attach</button>
      {err && <span className="error">{err}</span>}
    </form>
  )
}
