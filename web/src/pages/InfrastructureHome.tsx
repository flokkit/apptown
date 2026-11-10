import './InfrastructureHome.css'
import { Section } from '../Section'
import { ListTable } from '../ListTable'
import { useHostStates } from '../hostState'
import { useAttention } from '../attention'
import { Status } from '../Status'
import { Link } from 'react-router-dom'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { lastSeen } from '../format'
import { DeletedProjects } from './Infrastructure'
import type { InfrastructureTargets, TargetList } from '../wire.gen'
import { familyRank } from '../targets'
import { PageHead } from '../PageHead'
import { TargetConnections } from './integrations/TargetConnections'

// Infrastructure lists hosts by name, state and one summary. Each host's
// page keeps its capacity, inventory, settings and backup details.

export function InfrastructureHome() {
  const attention = useAttention()
  const hostState = useHostStates()
  const infra = useGet<InfrastructureTargets>('/infrastructure', { poll: 30000 })
  const targets = useGet<TargetList>('/targets', { poll: 30000 })
  if (!infra.data || !targets.data) return <ReadStatus q={[infra, targets]} what="each deployment target's inventory" />
  const byName = new Map(targets.data.targets.map((t) => [t.name, t]))
  const live = [...infra.data.targets].sort((a, b) => familyRank(a.family) - familyRank(b.family) || a.target.localeCompare(b.target))
  return (
    <div>
      <StaleNote q={[infra, targets, attention]} />
      <PageHead page="infrastructure" title="Targets" actions={<Link className="btn primary" to="/targets/new">Add a deployment target</Link>} />
      <Section title="All targets" count={targets.data.targets.length} label="Targets">
      <ListTable className="target-list" label="Targets" rows={[
        ...live.map(t => ({ t, v: byName.get(t.target)! })),
        ...targets.data.targets.filter(v => !live.some(t => t.target === v.name)).map(v => ({ t: undefined, v })),
      ]} rowKey={({ t, v }) => t?.target || v.name} columns={[
        { key: 'name', label: 'Target', phone: 'name', render: ({ t, v }) => <Link to={`/targets/${t?.target || v.name}`}>{v?.displayLabel || t?.target || v.name}</Link> },
        { key: 'state', label: 'State', phone: 'state', render: ({ t, v }) => { const state = hostState(t?.target || v.name, t?.statusLabel || v.statusLabel); return <Status observation word={state.word} /> } },
        { key: 'place', label: 'Place', render: ({ t }) => t?.place || 'unknown' },
        { key: 'apps', label: 'Apps', kind: 'number', render: ({ v }) => <>{v?.deployments.length ?? 'unknown'}<span className="record-phone"> {v?.deployments.length === 1 ? 'app' : 'apps'}</span></> },
        { key: 'detail', label: 'Observation', render: ({ t, v }) => {
          const state = hostState(t?.target || v.name, t?.statusLabel || v.statusLabel)
          return state.summary || (t?.asOf && !t.live ? lastSeen(t.asOf) : v?.status === 'retired' ? 'Retired' : !t ? 'Inventory not known' : t.resources?.some(r => r.record === 'missing') ? 'Check missing resources' : t.resources?.some(r => r.record === 'unrecorded') ? 'Review resources' : '')
        } },
      ]} />
      </Section>
      <div className="mt-4"><DeletedProjects /></div>
      <TargetConnections targets={targets.data.targets} />
    </div>
  )
}
