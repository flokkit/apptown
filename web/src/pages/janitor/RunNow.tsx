import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useAction } from '../../data'
import { post } from '../../api'
import { useHolds } from '../../instancePages'
import { TryAgain } from './TryAgain'
import type { QueueStatus, JanitorRunView } from '../../wire.gen'

export function RunNow({ slug, queues, current, reload }: { slug: string; queues: QueueStatus[]; current: boolean; reload: () => Promise<void> }) {
  const { run: runAction, busy, err } = useAction(() => { void reload() })
  const [result, setResult] = useState<JanitorRunView>()
  const mayCreate = useHolds('change:create', slug)
  const mayStart = useHolds('change:invoke-lane', slug)
  const reason = (!mayCreate || !mayStart ? 'Needs permission to start work' : '')
  const run = async () => {
    await runAction(async () => { setResult(undefined); setResult(await post<JanitorRunView>(`/projects/${encodeURIComponent(slug)}/janitor/run`, { queue: '', again: false, changeId: '' })) })
  }
  return <>
    <button type="button" disabled={!!reason || !current || busy} title={reason || undefined} onClick={run}>Run now</button>
    {reason && <div className="muted record-note">{reason}</div>}
    {result?.starts.filter(s => s.refused || s.whatIfId || s.current).map((s, i) => <div className="record-note" key={i}>
      {queues.find(q => q.queue === s.queue)?.title || s.queue}: {s.refused || (s.whatIfId ? <Link to={`/maintenance/standards/what-ifs/${s.whatIfId}`}>assessment started</Link> : 'up to date')}
      {s.busyChangeId && queues.filter(q => q.queue === s.queue).map(queue => <span key={queue.queue}> · <Link to={`/changes/${s.busyChangeId}`}>change #{s.busyChangeNumber}</Link> · <TryAgain refusedRun queue={{ ...queue, changeId: s.busyChangeId, changeNumber: s.busyChangeNumber }} slug={slug} current={current} reload={reload} /></span>)}
    </div>)}
    {err && <div className="error record-note">{err}</div>}
  </>
}

