import { Link } from 'react-router-dom'
import { post } from '../../../api'
import { useGet, useAction } from '../../../data'
import { useHolds } from '../../../instancePages'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import { FieldRow } from '../../../FieldRow'
import { Section } from '../../../Section'
import { fmtWhen } from '../../../format'
import { janitorLevelName } from '../../../janitorLevels'
import { RunNow } from '../../janitor/RunNow'
import type { ProjectScanView } from '../../../wire.gen'

export function JanitorChoice({ slug }: { slug: string }) {
  const q = useGet<ProjectScanView>(`/projects/${slug}/scans`, { poll: 10000 })
  const canManage = useHolds('project:manage-standing-approval', slug)
  const action = useAction()
  const standing = q.data?.standing
  const set = (level: string) => void action.run(async () => {
    try { await post(`/projects/${slug}/janitor/settings`, { level }) }
    finally { await q.reload() }
  })
  return <div id="janitor"><Section title="Janitor">
    <ReadStatus q={q} what="janitor settings" /><StaleNote q={q} />
    {standing && <>
      <div className="form-grid"><FieldRow label="Dependencies"><select aria-label="Dependencies" value={standing.choice?.level ?? 'instance'} disabled={!canManage || !q.current || action.busy} onChange={e => set(e.target.value)}>
        <option value="instance">Same as everywhere ({janitorLevelName(standing.instanceLevel)})</option>
        {['off', 'routine', 'aggressive'].map(level => <option key={level} value={level}>{janitorLevelName(level)}</option>)}
      </select></FieldRow></div>
      {standing.choice && <p>{standing.choice.person.name}, {fmtWhen(standing.choice.at)}</p>}
      {standing.instanceLevel === 'off' && <p><Link to="/janitor/settings">Dependency updates are off everywhere</Link></p>}
      <div className="mb-3"><RunNow slug={slug} queues={q.data!.queues} current={q.current} reload={q.reload} /></div>
    </>}
    {action.err && <p className="error" role="alert">{action.err}</p>}
  </Section></div>
}
