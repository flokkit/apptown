import { Section, Group } from '../Section'
import './infrastructure.css'
import { useHostStates } from '../hostState'
import { useId, useState } from 'react'
import { Status } from '../Status'
import { PRODUCT } from '../brand'
import { Link } from 'react-router-dom'
import { post } from '../api'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { QuietConfirm } from '../QuietConfirm'
import { lastSeen, fmtWhen, fmtStamp } from '../format'

// Infrastructure (design/subsystems/deployment.md §2.6, §15): the cloud, as it is.
// For each cloud target: what is at the provider right now — apps,
// databases, the registry — laid over what AppTown recorded, so each
// thing reads one of three ways: AppTown made it and here is what it
// hosts; AppTown recorded it and the provider has no trace (missing);
// the provider has it and AppTown never recorded it (unrecorded — made
// outside AppTown, or by an AppTown that lost its record). Nothing on this
// page changes anything; teardown and settling live on the project.

// The server's shapes, generated from its Go types (wire.gen.ts).
export type { Resource, InfrastructureView as TargetInfra } from '../wire.gen'
import type { InfrastructureTargets, InfrastructureView as TargetInfra, ProjectCleanupList, Resource } from '../wire.gen'
import { useHolds } from '../instancePages'
import { familyRank } from '../targets'


export const KIND_TITLE: Record<string, string> = { app: 'Apps', database: 'Databases', registry: 'Registry', container: 'Containers', volume: 'Data volumes', stack: 'Stacks' }
export const KIND_ORDER = ['app', 'container', 'stack', 'database', 'volume', 'registry']

export function InfrastructureView() {
  const q = useGet<InfrastructureTargets>('/infrastructure', { poll: 20000 })
  const hostState = useHostStates()
  const { data, reload } = q
  if (!data) return <ReadStatus q={q} what="each deployment target's inventory" />
  const targets = [...data.targets].sort((a, b) => familyRank(a.family) - familyRank(b.family) || a.target.localeCompare(b.target))
  return (
    <div>
      <StaleNote q={q} />
      <Section title="Deployment targets" count={targets.length} action={<Link to="/integrations/targets">Set up targets →</Link>}>
        {targets.map((t) => {
          // The same word as the target's own page: its connection, unless
          // something about the host (capacity, disk) needs attention.
          const state = hostState(t.target, t.statusLabel)
          const help = state.help.startsWith('Host connection') ? (t.status ? `Target check: ${t.status}` : undefined) : state.help
          return (
            <div key={t.target} className="card row" style={{ display: 'flex' }}>
              <Link to={`/infrastructure/${t.target}`} className="row" style={{ display: 'flex', flex: 1, minWidth: 0 }}>
                <span className="row-status"><Status observation word={state.word} help={help} /></span>
                <strong>{t.target}</strong>
                <span className="muted small">{[t.place, state.summary, t.asOf && !t.live && !state.summary.startsWith('last seen') ? lastSeen(t.asOf) : '', t.invalidated ? 'before the latest change' : ''].filter(Boolean).join(' · ')}</span>
              </Link>
              <span className="row-action"><Link className="btn" to={`/infrastructure/${t.target}`}>Manage</Link></span>
            </div>
          )
        })}
      </Section>
      {targets.map((t) => <TargetSection key={t.target} t={t} onChanged={reload} />)}
    </div>
  )
}

// A deleted project's containers, volumes and workspaces are removed after
// its records, by a queued cleanup that holds the project's address until it
// is done. Only cleanups still under way or waiting for a person show here.
export function DeletedProjects() {
  // The cleanups are read and retried by an administrator.
  const admin = useHolds('identity:manage-auth')
  const q = useGet<ProjectCleanupList>(admin ? '/project-cleanups' : null, { poll: 20000 })
  const { run, busy, err: actErr } = useAction()
  if (!admin) return null
  if (!q.data) return <div style={{ marginBottom: 16 }}><ReadStatus q={q} what="deleted projects' cleanup" /></div>
  const open = q.data.cleanups.filter((c) => c.state !== 'done')
  // None open, as last read: if that read may be old, say so.
  if (open.length === 0) return <StaleNote q={q} className="mb-4" />
  return (
    <div className="card" style={{ marginBottom: 16 }}>
      <StaleNote q={q} />
      <strong>Deleted projects still being cleaned up</strong>
      <p className="muted small" style={{ margin: '4px 0 8px' }}>
        Cleanup removes containers, volumes and workspaces, then frees the project address.
      </p>
      {open.map((c) => (
        <div key={c.projectId} className="row" style={{ gap: 10, alignItems: 'baseline', margin: '6px 0' }}>
          <b>{c.slug}</b>
          {c.state === 'needs-attention'
            ? <span className="error small">cleanup stopped after {c.attempts} tries: {c.detail}</span>
            : <span className="muted small">cleanup under way{c.attempts > 0 ? ` (tried ${c.attempts} times; last: ${c.detail})` : ''}</span>}
          <span className="muted small">deleted {fmtStamp(c.requestedAt)} by {c.requestedBy}</span>
          {c.state === 'needs-attention' &&
            <button disabled={busy} onClick={() => run(() => post(`/project-cleanups/${c.projectId}/retry`, {}))}>Try again</button>}
        </div>
      ))}
      {actErr && <div className="error small">{actErr}</div>}
    </div>
  )
}

function TargetSection({ t: tIn, onChanged }: { t: TargetInfra; onChanged: () => void }) {
  const t = { ...tIn, resources: tIn.resources ?? [] }
  const unrecorded = t.resources.filter((r) => r.record === 'unrecorded').length
  const missing = t.resources.filter((r) => r.record === 'missing').length
  const note = [
    t.domain ? `*.${t.domain}` : '', t.facts,
    unrecorded > 0 ? `${unrecorded} unrecorded` : '', missing > 0 ? `${missing} missing` : '',
    t.unreported ? UNREPORTED : t.unknown && !t.error ? 'Pending first check' : '',
    t.invalidated ? 'Inventory predates the latest change.' : '',
  ].filter(Boolean).join(' · ')
  return (
    <Section title={`Resources on ${t.target}`} label={`${t.target} resources`} count={t.resources.length || undefined} note={note || undefined}>
      {t.error && !t.live && <p className="error small">{t.error}{t.asOf ? ` · showing the inventory ${lastSeen(t.asOf)}.` : ` · showing ${PRODUCT}'s records only.`}</p>}
      <ResourceTables t={t} onChanged={onChanged} />
      {t.shared && <p className="muted small">{t.shared}</p>}
    </Section>
  )
}

// UNREPORTED: a target whose kind has no inventory reader. What it holds
// is unknown, which is neither a failure nor empty.
const UNREPORTED = "This kind of target doesn't report what it holds"

// ResourceTables: what is at a target, by kind, one row each, with the
// remove control on anything AppTown does not claim. The fleet view and a
// target's own page both show it, inside a section's panel.
export function ResourceTables({ t: tIn, onChanged, only, empty }: { t: TargetInfra; onChanged: () => void; only?: (r: Resource) => boolean; empty?: string }) {
  const t = { ...tIn, resources: (tIn.resources ?? []).filter(only ?? (() => true)) }
  const kinds = [...KIND_ORDER.filter((k) => t.resources.some((r) => r.kind === k)), ...[...new Set(t.resources.map((r) => r.kind))].filter((k) => !KIND_ORDER.includes(k))]
  if (t.resources.length === 0) return <p className="muted small">{empty ?? (t.live ? 'Empty inventory.' : t.asOf ? `No resources in the inventory ${lastSeen(t.asOf)}.` : t.unreported ? 'No recorded resources.' : 'Inventory is unknown.')}</p>
  const confirmed = !t.invalidated && (t.live || !!t.asOf && !t.unknown)
  const seenAt = !t.live && !t.invalidated && !t.unknown ? t.asOf : undefined
  return (
    <>
      {kinds.map((k) => (
        <Group key={k} label={KIND_TITLE[k] ?? k}>
          {t.resources.filter((r) => r.kind === k).map((r) => <ResourceRow key={`${r.kind}/${r.id || r.name}`} r={r} target={t.target} confirmed={confirmed} invalidated={t.invalidated} seenAt={seenAt} onChanged={onChanged} />)}
        </Group>
      ))}
    </>
  )
}

const HEALTH_KINDS = ['container', 'stack', 'app', 'database']

function ResourceRow({ r, target, onChanged, confirmed, invalidated, seenAt }: { r: Resource; target: string; onChanged: () => void; confirmed: boolean; invalidated: boolean; seenAt?: string }) {
  const unknownReason = invalidated ? 'Inventory predates the latest change. The next inventory check must confirm this resource; ask an administrator to check the target if it cannot finish.' : 'No current inventory confirms presence. Ask an administrator to check this target.'
  const host = r.url?.replace(/^https?:\/\//, '').replace(/\/$/, '')
  const [open, setOpen] = useState(false)
  const detailId = useId()
  const { run, busy, err } = useAction()
  const removable = r.record === 'unrecorded' && ['container', 'volume', 'stack'].includes(r.kind)
  const remove = async () => {
    const ok = await run(() => post(`/infrastructure/${target}/remove`, { kind: r.kind, name: r.name, id: r.id ?? '', confirm: r.name }))
    if (ok) onChanged()
  }
  // One fixed state word per row: unknown without a current inventory,
  // failed when the provider has lost a recorded resource, otherwise its
  // health, or present for a resource with nothing running to check. The
  // time of the reading is in the meta line, once.
  const state = !confirmed ? <Status observation word="unknown" help={unknownReason} />
    : r.record === 'missing' ? <Status observation word="failed" help="The provider did not find this recorded resource. Check the target and restore or release the app." />
      : HEALTH_KINDS.includes(r.kind) ? <Status observation word={r.statusLabel} help={r.status && !['working', 'running'].includes(r.statusLabel) && r.status.toLowerCase() !== r.statusLabel ? r.status : undefined} />
        : <span className="muted">present</span>
  const holders = (r.hosts ?? []).map((h) => h.project.startsWith('(') ? h.project : `${h.project}/${h.deployment}${h.need ? ` (its ${h.need})` : ''}`)
  const meta = [
    holders.join(', ') || (r.retained ? `kept from ${r.retained.project}/${r.retained.deployment}` : ''),
    r.record === 'unrecorded' ? 'unrecorded' : r.record === 'missing' ? 'missing at the target' : '',
    [r.version, r.size].filter(Boolean).join(' · '), r.region ?? '',
    seenAt ? lastSeen(seenAt) : '',
  ].filter(Boolean).join(' · ')
  return (
    <div className="card row resource-row" style={{ display: 'flex', flexWrap: 'wrap', opacity: r.record === 'missing' ? 0.7 : undefined }}>
      <span className="row-status">{state}</span>
      <strong style={{ overflowWrap: 'anywhere' }}>{r.name || `Unlinked ${r.kind}`}</strong>
      <span className="muted small" style={{ flex: 1, minWidth: 0 }}>{meta}</span>
      <span className="row-action"><button aria-expanded={open} aria-controls={detailId} onClick={() => setOpen(!open)}>{open ? 'Hide resource' : 'View resource'}</button></span>
      {open && <div id={detailId} className="resource-evidence small" style={{ flexBasis: '100%', margin: '4px 0 8px' }}>
        {r.url && <div><a href={r.url} target="_blank" rel="noreferrer">{host}</a></div>}
        {r.id && <div className="muted" title="the provider's id">{r.id}</div>}
        {r.detail && <div className="muted">{r.detail}</div>}
        {!confirmed && <div className="muted">{unknownReason}</div>}
        {(r.hosts ?? []).filter((h) => !h.project.startsWith('(')).map((h, i) => (
          <div key={i}>Used by <Link to={`/projects/${h.project}/settings/deployments`}>{h.project}/{h.deployment}</Link>{h.need ? <span className="muted"> — its {h.need}</span> : h.generation ? <span className="muted"> gen {h.generation}</span> : null}</div>
        ))}
        {r.retained && (
          <div>Kept by <Link to={`/projects/${r.retained.project}/settings/deployments`}>{r.retained.project}/{r.retained.deployment}</Link><span className="muted">{r.retained.reason ? ` · ${r.retained.reason}` : `, taken down ${fmtWhen(r.retained.retainedAt)} by ${r.retained.retainedBy}`}</span></div>
        )}
        <div className="muted">{r.record === 'unrecorded' ? `At the provider, not in ${PRODUCT}'s records: made outside ${PRODUCT}, or before ${PRODUCT} lost its record.` : `${PRODUCT} records ownership of this resource.`}</div>
        {r.tags && r.tags.length > 0 && <div className="muted" title="the provider's tags">Tags: {r.tags.join(', ')}</div>}
        {removable && (
          <div style={{ marginTop: 4 }}>
            <QuietConfirm label="Remove" ask={`Remove ${r.kind} ${r.name} from ${target}? ${PRODUCT} has no record of it${r.kind === 'volume' ? ', and a volume\'s data goes with it' : ''}.`}
              onYes={remove} yes="Remove" className="subtle danger" title={`Remove it; ${PRODUCT} has no record of it`} disabled={busy} />
            {err && <div className="error small">{err}</div>}
          </div>
        )}
      </div>}
    </div>
  )
}
