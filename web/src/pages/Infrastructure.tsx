import { GlanceRow } from '../GlanceRow'
import { STATUS_LIGHT, Status } from '../Status'
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
  const { data, reload } = q
  if (!data) return <ReadStatus q={q} what="each deployment target's inventory" />
  const targets = [...data.targets].sort((a, b) => familyRank(a.family) - familyRank(b.family) || a.target.localeCompare(b.target))
  return (
    <div>
      <StaleNote q={q} />
      <details className="small"><summary>About resources</summary><p className="muted small" style={{ maxWidth: 760, marginTop: 0 }}>
        Every deployment target and its inventory. Each resource is <b>recorded</b> (made by {PRODUCT} for a deployment),
        <b> retained</b> (left behind when its deployment was torn down), <b>unrecorded</b> (present at the target, absent from the
        {PRODUCT}'s records) or <b>missing</b> (recorded, gone from the target).
      </p></details>
      <div style={{ marginBottom: 16 }}>
        <div className="row" style={{ gap: 10 }}>
          <Link className="small" to="/infrastructure/new">+ add a deployment target</Link>
          <Link className="small" to="/integrations/targets">offers</Link>
        </div>
      </div>
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
  return (
    <section style={{ marginBottom: 26 }}>
      <div className="host-row"><strong><Link to={`/infrastructure/${t.target}`}>{t.target}</Link></strong><Status word={t.statusLabel} tone={t.statusLabel === 'working' ? 'pass' : STATUS_LIGHT[t.statusLabel]} help={`Target check: ${t.status}`} /><span className="muted small">{t.resources.length} resources</span>
        {(t.unknown || t.unreported) && <Status word="unknown" tone="unknown" help={t.unreported ? UNREPORTED : t.error || 'Pending first check.'} />}
        {t.asOf && <span className="muted small">{lastSeen(t.asOf)}</span>}
        {t.invalidated && <span className="chip pending">before the latest change</span>}
      </div>
      <div className="target-inventory">
      <div className="row small" style={{ marginBlock: 8 }}>
        <span className="muted">{t.place}{t.domain ? ` · *.${t.domain}` : ''}{t.facts ? ` · ${t.facts}` : ''}</span>
        {unrecorded > 0 && <span className="chip pending">{unrecorded} unrecorded</span>}
        {missing > 0 && <span className="chip fail">{missing} missing</span>}
        <Link to={`/infrastructure/${t.target}`}>manage</Link>
      </div>
      {t.error && !t.live && <div className="error small" style={{ marginBottom: 8 }}>{t.error}{t.asOf ? ' · showing the last inventory read.' : ` · showing ${PRODUCT}'s records only.`}</div>}
      <ResourceTables t={t} onChanged={onChanged} />
      {t.shared && <div className="muted small" style={{ marginTop: 6 }}>{t.shared}</div>}
      </div>
    </section>
  )
}

// UNREPORTED: a target whose kind has no inventory reader. What it holds
// is unknown, which is neither a failure nor empty.
const UNREPORTED = "This kind of target doesn't report what it holds"

// ResourceTables: what is at a target, by kind, with the remove control on
// anything AppTown does not claim. The fleet view and a target's own page
// both show it.
export function ResourceTables({ t: tIn, onChanged, only, empty }: { t: TargetInfra; onChanged: () => void; only?: (r: Resource) => boolean; empty?: string }) {
  const t = { ...tIn, resources: (tIn.resources ?? []).filter(only ?? (() => true)) }
  const kinds = [...KIND_ORDER.filter((k) => t.resources.some((r) => r.kind === k)), ...[...new Set(t.resources.map((r) => r.kind))].filter((k) => !KIND_ORDER.includes(k))]
  if (t.resources.length === 0) return <div className="muted small">{empty ?? (t.live ? 'Empty inventory.' : t.asOf ? `No resources in the inventory ${lastSeen(t.asOf)}.` : t.unreported ? 'No recorded resources.' : 'Inventory is unknown.')}</div>
  return (
    <>
      {kinds.map((k) => (
        <div key={k} style={{ marginBottom: 12, overflowX: 'auto' }}>
          <h3 style={{ margin: '8px 0 4px' }}>{KIND_TITLE[k] ?? k}</h3>
          <table style={{ fontVariantNumeric: 'tabular-nums' }}>
            <thead>
              <tr>
                <th>Name</th><th>Status</th><th>Where</th><th>Size</th><th>Hosting</th><th>Record</th>
              </tr>
            </thead>
            <tbody>
              {t.resources.filter((r) => r.kind === k).map((r) => <ResourceRow key={`${r.kind}/${r.id || r.name}`} r={r} target={t.target} onChanged={onChanged} />)}
            </tbody>
          </table>
        </div>
      ))}
    </>
  )
}

function ResourceRow({ r, target, onChanged }: { r: Resource; target: string; onChanged: () => void }) {
  const host = r.url?.replace(/^https?:\/\//, '').replace(/\/$/, '')
  const { run, busy, err } = useAction()
  const removable = r.record === 'unrecorded' && ['container', 'volume', 'stack'].includes(r.kind)
  const remove = async () => {
    const ok = await run(() => post(`/infrastructure/${target}/remove`, { kind: r.kind, name: r.name, id: r.id ?? '', confirm: r.name }))
    if (ok) onChanged()
  }
  return (
    <GlanceRow name={0} state={1} fact={3} detail={<div className="resource-evidence">
        <p>{r.name}</p>
        {r.url && <a className="small" href={r.url} target="_blank" rel="noreferrer">{host}</a>}
        {r.id && <div className="muted small" title="the provider's id">{r.id}</div>}
        {r.detail && <div className="muted small">{r.detail}</div>}

        {removable && (
          <div style={{ marginTop: 4 }}>
            <QuietConfirm label="remove" ask={`Remove ${r.kind} ${r.name} from ${target}? ${PRODUCT} has no record of it${r.kind === 'volume' ? ', and a volume\'s data goes with it' : ''}.`}
              onYes={remove} yes="Remove" className="subtle danger" title={`Remove it; ${PRODUCT} has no record of it`} disabled={busy} />
            {err && <div className="error small">{err}</div>}
          </div>
        )}</div>} labels={["Resource", "State", "Where", "Size", "Apps", "Record"]} style={r.record === 'missing' ? { opacity: 0.6 } : undefined}>
      <td>
        <div style={{ fontWeight: 600 }}>{r.hosts?.[0] && !r.hosts[0].project.startsWith('(') ? `${r.hosts[0].project} · ${r.kind === 'volume' ? 'data' : r.kind}` : r.retained ? `${r.retained.project} · old ${r.kind}` : r.name || `Unlinked ${r.kind}`}</div>
      </td>
      <td>{r.status ? <Status word={r.statusLabel} tone={STATUS_LIGHT[r.statusLabel]} help={r.status} /> : <span className="muted">—</span>}</td>
      <td className="small">{r.region || <span className="muted">—</span>}</td>
      <td className="small">{[r.version, r.size].filter(Boolean).join(' · ') || <span className="muted">—</span>}</td>
      <td className="small">
        {(r.hosts ?? []).map((h, i) => (
          <div key={i}>
            {h.project.startsWith('(')
              ? <span className="muted">{h.project}</span>
              : <>
                  <Link to={`/projects/${h.project}/settings/deployments`}>{h.project}/{h.deployment}</Link>
                  {h.need ? <span className="muted"> — its {h.need}</span> : h.generation ? <span className="muted"> gen {h.generation}</span> : null}
                </>}
          </div>
        ))}
        {r.retained && (
          <div>
            <span className="chip pending">retained</span>{' '}
            <span className="muted">by <Link to={`/projects/${r.retained.project}/settings/deployments`}>{r.retained.project}/{r.retained.deployment}</Link>{r.retained.reason ? ` · ${r.retained.reason}` : `, torn down ${fmtWhen(r.retained.retainedAt)} by ${r.retained.retainedBy}`}</span>
          </div>
        )}
        {!r.retained && (r.hosts ?? []).length === 0 && <span className="muted">none recorded</span>}
      </td>
      <td>
        {r.record === 'recorded' && <span className="chip pass" title={`${PRODUCT} made this and the provider confirms it`}>recorded</span>}
        {r.record === 'unrecorded' && <span className="chip pending" title={`at the provider, not in ${PRODUCT}'s records · made outside ${PRODUCT}, or before ${PRODUCT} lost its record`}>unrecorded</span>}
        {r.record === 'missing' && <Status word={"missing"} tone={STATUS_LIGHT["missing"]} help={`in ${PRODUCT}'s records; the provider has no trace of it`} />}
        {r.tags && r.tags.length > 0 && <div className="muted small" title="the provider's tags">{r.tags.join(', ')}</div>}
      </td>
    </GlanceRow>
  )
}
