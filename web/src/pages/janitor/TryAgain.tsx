import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useAction } from '../../data'
import { post } from '../../api'
import { useHolds } from '../../instancePages'
import type { JanitorRunView, QueueStatus } from '../../wire.gen'

// Both pages use the queue identity they read. The owner rejects a stale change.
export function TryAgain({ queue, slug, current, reload, refusedRun = false }: { queue: QueueStatus; slug: string; current: boolean; reload: () => void; refusedRun?: boolean }) {
  const action = useAction(reload)
  const [result, setResult] = useState<JanitorRunView>()
  const create = useHolds('change:create', slug)
  const start = useHolds('change:invoke-lane', slug)
  const close = useHolds('change:abandon', slug)
  const eligible = create && start && (!queue.changeId || close)
  if (!refusedRun && queue.state !== 'stuck' && queue.state !== 'stopped') return null
  return <>
    <button type="button" className="link small" disabled={!current || !eligible || action.busy}
      onClick={() => action.run(async () => {
        setResult(undefined)
        const result = await post<JanitorRunView>(`/projects/${encodeURIComponent(slug)}/janitor/run`, { queue: queue.queue, again: true, changeId: queue.changeId || '' })
        setResult(result)
      })}>{queue.changeId ? 'Close and try again' : 'Try again'}</button>
    {result?.starts.map((s, i) => s.refused ? <span className="error" key={i}> · {s.refused}</span> : s.whatIfId ? <span key={i}> · <Link to={`/conformance/what-ifs/${s.whatIfId}`}>assessment started</Link></span> : s.current ? <span key={i}> · up to date</span> : null)}
    {action.err && <span className="error"> · {action.err}</span>}
  </>
}
