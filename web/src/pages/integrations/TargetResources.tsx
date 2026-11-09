import { usePageName } from '../../chrome'
import { ListTable } from '../../ListTable'
import { PRODUCT } from '../../brand'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { useState } from 'react'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { MoreMenu } from '../../MoreMenu'
import { QuietConfirm } from '../../QuietConfirm'
import { ResourceStatus } from '../Infrastructure'
import { post } from '../../api'
import type { InfrastructureTargets, InfrastructureView, Resource, ResourceHost } from '../../wire.gen'

// The route key retains provider identity and kind, including resources without an ID.
const resourceKey = (r: Resource) => `${r.kind}:${r.id || r.name}`
const resourcePath = (target: string, r: Resource) => `/targets/${encodeURIComponent(target)}/resources/${encodeURIComponent(resourceKey(r))}`
const confirmed = (t: InfrastructureView) => !t.invalidated && !t.unknown && (t.live || !!t.asOf)

export function TargetResourcesTable({ t, only = () => true, empty = 'No resources in this inventory.' }: { t: InfrastructureView; only?: (r: Resource) => boolean; empty?: string }) {
  const resources = t.resources.filter(only)
  if (!resources.length) return <p className="muted small">{empty}</p>
  return <ListTable className="target-resources-table" label="Resources" rows={resources} rowKey={resourceKey} columns={[
    { key: 'name', label: 'Name', kind: 'technical', phone: 'name', render: r => <Link to={resourcePath(t.target, r)} title={r.name}>{r.name || r.id}</Link> },
    { key: 'kind', label: 'Kind', phone: 'extra', className: 'record-words', render: r => r.kind },
    { key: 'deployment', label: 'Deployment', phone: 'detail', className: 'record-words', render: r => (r.hosts ?? []).map((h, i) => <span key={i}>{i > 0 && ", "}{h.project.startsWith('(') ? h.project : <Link to={`/projects/${h.project}/deployments/${h.deployment}`} title={`${h.project}/${h.deploymentLabel || h.deployment}`}>{h.project}/{h.deploymentLabel || h.deployment}</Link>}</span>) },
    { key: 'state', label: 'State', phone: 'state', className: 'record-words', render: r => <ResourceStatus compact r={r} confirmed={confirmed(t)} invalidated={t.invalidated} /> },
    { key: 'owned', label: 'Owned', phone: 'time', className: 'record-words', render: r => r.record === 'unrecorded' ? 'unknown' : 'owned' },
  ]} />
}

export function TargetResourcesPage({ detail = false }: { detail?: boolean }) {
  const { name, id } = useParams()
  const [search, setSearch] = useSearchParams()
  const q = useGet<InfrastructureTargets>('/infrastructure', { poll: 30000 })
  usePageName(`/targets/${name}`, name)
  if (!q.data) return <ReadStatus q={q} what="the target's resources" />
  const recorded = q.data.targets.find(t => t.target === name)
  const t = recorded && { ...recorded, unknown: recorded.unknown || q.refreshFailed }
  if (!t) return <p>Inventory is unknown for this target.</p>
  const r = t.resources.find(r => resourceKey(r) === id)
  if (detail) return r ? <><StaleNote q={q} />{t.error && <p className="muted">Inventory is unknown: {t.error}</p>}<ResourcePage key={resourceKey(r)} t={t} r={r} reload={q.reload} /></> : <p>This resource is absent from the latest inventory. <Link to={`/targets/${name}/resources`}>Resources</Link></p>
  const deployments = [...new Set(t.resources.flatMap(r => (r.hosts ?? []).map(h => `${h.project}/${h.deployment}`)))].sort()
  const selected = search.get('deployment') ?? ''
  return <div>
    <h1>Resources</h1><StaleNote q={q} />
    {t.error && <p className="muted">Inventory is unknown: {t.error}</p>}
    {(t.unknown || t.invalidated) && <p>Current resource state is unknown.</p>}
    <label>Deployment <select value={selected} onChange={e => setSearch(e.target.value ? { deployment: e.target.value } : {})}>
      <option value="">All deployments</option>{[...new Set([...deployments, ...(selected ? [selected] : [])])].map(d => <option key={d}>{d}</option>)}
    </select></label>
    <TargetResourcesTable t={t} only={r => !selected || (r.hosts ?? []).some(h => `${h.project}/${h.deployment}` === selected)} />
    {t.asOf && <p className="muted small">Inventory read {t.asOf}</p>}
  </div>
}

const hostName = (h: ResourceHost) => `${h.project}/${h.deploymentLabel || h.deployment}`

// A resource is titled by what it is and what it belongs to; its generated
// name, which only an administrator matches against the target, is the full
// reference below.
function resourceTitle(r: Resource): string {
  const kind = r.kind.charAt(0).toUpperCase() + r.kind.slice(1)
  const hosts = (r.hosts ?? []).filter(h => !h.project.startsWith('('))
  return hosts.length ? `${kind} for ${hosts.map(hostName).join(', ')}` : kind
}

function ResourcePage({ t, r, reload }: { t: InfrastructureView; r: Resource; reload: () => void }) {
  const [removing, setRemoving] = useState(false)
  const { run, busy, err } = useAction()
  const removable = r.record === 'unrecorded' && ['container', 'volume', 'stack'].includes(r.kind)
  return <div>
    <div className="row"><h1 className="grow target-resource-reference">{resourceTitle(r)}</h1>{removable && <MoreMenu label={r.name} items={[{ label: 'Remove…', onClick: () => setRemoving(true) }]} />}</div>
    <dl className="facts">
      <dt>Kind</dt><dd>{r.kind}</dd><dt>Full reference</dt><dd>{r.name && <code className="tech-line">{r.name}</code>}{r.id && r.id !== r.name && <code className="tech-line">{r.id}</code>}</dd>
      <dt>State</dt><dd><ResourceStatus r={r} confirmed={confirmed(t)} invalidated={t.invalidated} /></dd>
      <dt>Ownership</dt><dd>{r.record === 'unrecorded' ? `Unknown. ${PRODUCT} has no ownership record.` : r.record === 'missing' ? 'Owned. Missing at the target.' : `${PRODUCT} records ownership.`}</dd>
      <dt>Used by</dt><dd>{(r.hosts ?? []).length ? (r.hosts ?? []).map((h, i) => <div key={i}>{h.project.startsWith('(') ? h.project : <Link to={`/projects/${h.project}/deployments/${h.deployment}`} title={`${h.project}/${h.deploymentLabel || h.deployment}`}>{h.project}/{h.deploymentLabel || h.deployment}</Link>}{h.need && ` · ${h.need}`}</div>) : 'No deployment recorded.'}</dd>
      <dt>Generation</dt><dd>{(r.hosts ?? []).length ? (r.hosts ?? []).map((h, i) => <div key={i}>{hostName(h)}: {h.generation || 'unknown'}</div>) : 'unknown'}</dd>
      {r.detail && <><dt>Detail</dt><dd className="target-resource-reference">{r.detail}</dd></>}
      {r.version && <><dt>Version</dt><dd>{r.version}</dd></>}
      {r.size && <><dt>Size</dt><dd>{r.size}</dd></>}
      {r.region && <><dt>Region</dt><dd>{r.region}</dd></>}
      {r.url && <><dt>Address</dt><dd><a href={r.url}>{r.url}</a></dd></>}
      {!!r.tags?.length && <><dt>Tags</dt><dd>{r.tags.join(', ')}</dd></>}
      {r.retained && <><dt>Kept from</dt><dd><Link to={`/projects/${r.retained.project}/deployments`}>{r.retained.project}/{r.retained.deployment}</Link> · {r.retained.reason || `taken down ${r.retained.retainedAt} by ${r.retained.retainedBy}`}</dd></>}
    </dl>
    {removing && <QuietConfirm label="Remove resource" ask={`Remove ${r.kind} ${r.name} from ${t.target}?${r.kind === 'volume' ? ' Its data will be deleted.' : ''}`} yes="Remove" disabled={busy} onYes={() => void run(async () => { await post(`/infrastructure/${t.target}/remove`, { kind: r.kind, name: r.name, id: r.id ?? '', confirm: r.name }); setRemoving(false); reload() })} />}
    {err && <p className="error">{err}</p>}
  </div>
}
