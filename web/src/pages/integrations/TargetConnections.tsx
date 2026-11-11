import { PRODUCT } from '../../brand'
import { Link } from 'react-router-dom'
import { ListTable } from '../../ListTable'
import { Section } from '../../Section'
import { HelpButton } from '../../PageHead'
import { Status } from '../../Status'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { ConnectorCatalog, IntegrationEntry, IntegrationList, TargetView } from '../../wire.gen'
import { healthState, specFor, usedFor } from './kinds'

// Relationships come from the connector each target is shown under (its
// provider, or the built-in host's stock connector), never from display names.
function connectionTargets(b: IntegrationEntry, targets: TargetView[]) {
  return targets.filter(t => t.connector === b.name)
}

export function TargetConnections({ targets }: { targets: TargetView[] }) {
  const q = useGet<IntegrationList>('/integrations')
  const catalog = useGet<ConnectorCatalog>('/integrations/catalog', { kind: 'config' })
  return <>{(['deploy', 'cloud-account'] as const).map(cap => {
    const title = cap === 'deploy' ? 'Deploy connectors' : 'Cloud accounts'
    const rows = q.data?.integrations?.filter(b => b.capability === cap) ?? []
    return <Section key={cap} label={title} title={<>{title} <HelpButton about={title} guide="deployment-targets"><p>{cap === 'deploy' ? `Deploy connectors let ${PRODUCT} reach Docker hosts and Portainer environments to deploy apps and read their resources.` : `Cloud accounts let ${PRODUCT} deploy apps and manage resources, including image registries, with the cloud provider.`}</p></HelpButton></>}>
      <StaleNote q={q} />
      {!q.data ? <ReadStatus q={q} what={title.toLowerCase()} /> : !rows.length ? <p className="muted small">None connected.</p> : <ListTable className="target-connections" label={title} rows={rows} rowKey={b => b.id || b.name} columns={[
        { key: 'name', label: 'Connection', phone: 'name', render: b => <Link to={`/integrations/${b.name}`}>{b.displayLabel || b.name}</Link> },
        { key: 'state', label: 'State', phone: 'state', render: b => <Status observation word={q.refreshFailed ? 'unknown' : healthState(b.health)} /> },
        { key: 'description', label: 'Description', render: b => <span title={usedFor(b, specFor(catalog.data?.catalog ?? [], b)) || undefined}>{usedFor(b, specFor(catalog.data?.catalog ?? [], b))}</span> },
        { key: 'uses', label: 'Used for', render: b => {
          const used = connectionTargets(b, targets)
          return used.length ? used.map(t => <div key={t.id}><Link to={`/targets/${t.name}`}>{t.displayLabel || t.name}</Link> · deploying{t.kind === 'do-app' && typeof t.address.registry === 'string' && t.address.registry ? ' · image registry' : ''}{t.ingress.rehearsals === true ? ' · rehearsal host' : ''}{targets.some(other => other.address.copyRunner === t.name) ? ' · backup jobs' : ''}</div>) : <span className="muted small">Not used by any target yet.</span>
        } },
      ]} />}

    </Section>
  })}</>
}
