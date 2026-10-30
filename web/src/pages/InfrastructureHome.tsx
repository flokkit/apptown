import { STATUS_LIGHT, Status } from '../Status'
import { PRODUCT } from '../brand'
import { Link, NavLink, useLocation } from 'react-router-dom'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { fmtBytes, relTime } from '../format'
import { DeletedProjects, useRefreshInfrastructure } from './Infrastructure'
import { Glyph } from '../parts/Glyph'
import type { AllBackups, DiskReadings, InfrastructureTargets, Job, JobList, Reading, TargetList } from '../wire.gen'
import { FAMILY_TITLE, familyRank } from '../targets'
import { PageHead } from '../PageHead'

// Infrastructure: every deployment target, one card each, with its
// inventory — deployments, resources at the provider, unrecorded resources,
// backups — and its own page one click in.

const JOB_WORDS: Record<string, string> = { backup: 'backup of', inventory: 'inventory of', rebuild: 'rebuild of', 'backup-sweep': 'backup sweep,', 'db-backup': `${PRODUCT}'s own database backup,` }
const jobReceipt = (j: Job) => {
  if (j.error) return j.error
  const r = j.receipt ?? {}
  if (typeof r.skipped === 'string') return `skipped: ${r.skipped}`
  if (typeof r.asked === 'number') return `asked for ${r.asked} backup(s)`
  if (typeof r.took === 'string') return r.took
  if (typeof r.url === 'string') return `running again at ${r.url}`
  if (typeof r.resources === 'number') return `${r.resources} resources`
  if (typeof r.bytes === 'number') return `${r.items ?? ''} item(s), ${Math.round((r.bytes as number) / 1024)} KB`
  return ''
}


export function InfrastructureHome() {
  const tab = useLocation().pathname.endsWith('/jobs') ? 'jobs' : 'targets'
  const infra = useGet<InfrastructureTargets>('/infrastructure', { poll: 30000 })
  const targets = useGet<TargetList>('/targets', { poll: 30000 })
  const backups = useGet<AllBackups>('/backups', { poll: 60000 })
  const { refresh, refreshing, err: refreshErr } = useRefreshInfrastructure()
  if (!infra.data || !targets.data) return <ReadStatus q={[infra, targets]} what="each deployment target's inventory" />
  const byName = new Map(targets.data.targets.map((t) => [t.name, t]))
  const live = [...infra.data.targets].sort((a, b) => familyRank(a.family) - familyRank(b.family) || a.target.localeCompare(b.target))
  const latestBackup = (envs: { project: string; name: string }[]) => {
    const keys = new Set(envs.map((e) => e.project + '/' + e.name))
    const mine = (backups.data?.backups ?? []).filter((b) => keys.has(b.project + '/' + b.deployment) && b.status !== 'pruned' && b.status !== 'failed')
    return mine.length ? { latest: mine[0].takenAt, kept: mine.length } : null
  }
  return (
    <div>
      <StaleNote q={[infra, targets, backups]} />
      <PageHead page="infrastructure"
        actions={tab === 'targets' && <button className="subtle" onClick={refresh} disabled={refreshing}>{refreshing ? 'refreshing…' : 'Refresh all'}</button>} />
      {refreshErr && <div className="error">The inventory could not be refreshed: {refreshErr}</div>}
      <div className="tabs" style={{ marginTop: 10 }}>
        <NavLink to="/infrastructure" end className={() => 'tab' + (tab === 'targets' ? ' active' : '')}>Deployment targets</NavLink>
        <NavLink to="/infrastructure/jobs" className={() => 'tab' + (tab === 'jobs' ? ' active' : '')}>Job queue</NavLink>
      </div>
      {tab === 'targets' && <OwnDisks />}
      {tab === 'targets' && <div style={{ marginTop: 16 }}><DeletedProjects /></div>}
      {tab === 'jobs' ? <JobQueue /> : <div className="infra-grid" style={{ marginTop: 16 }}>
        {live.map((t) => {
          const v = byName.get(t.target)
          const res = t.resources ?? []
          const envs = v?.deployments ?? []
          const unrecorded = res.filter((r) => r.record === 'unrecorded').length
          const missing = res.filter((r) => r.record === 'missing').length
          const thingWord = t.kind === 'portainer' ? 'stack' : t.kind === 'do-app' ? 'app' : t.kind === 's3-bucket' ? 'bucket' : 'container'
          const things = res.filter((r) => r.record !== 'missing' && ['container', 'stack', 'app', 'bucket'].includes(r.kind)).length
          const dataWord = t.kind === 'do-app' ? 'database' : 'data volume'
          const data = res.filter((r) => r.record !== 'missing' && (r.kind === 'volume' || r.kind === 'database')).length
          const bk = latestBackup(envs)
          const offers = v?.offers ?? []
          return (
            <Link key={t.target} to={`/infrastructure/${t.target}`} className="conn-card infra-card">
              <div className="infra-head">
                <div className="infra-glyph" aria-hidden><Glyph kind={t.kind} /></div>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div className="infra-title">{t.target}</div>
                  <div className="infra-kind">{FAMILY_TITLE[t.family] ?? t.family} · {t.place}{t.domain ? ` · *.${t.domain}` : ''}</div>
                </div>
                <Status word={t.statusLabel} tone={STATUS_LIGHT[t.statusLabel]} help={`Target check: ${t.status}.`} />
              </div>
              <div className="infra-facts" title={t.facts}>{t.facts}</div>
              <div className="infra-stats">
                <div className="infra-stat"><b>{envs.length}</b><span>deployment{envs.length === 1 ? '' : 's'}</span></div>
                <div className="infra-stat"><b>{things}</b><span>{thingWord}{things === 1 ? '' : 's'}</span></div>
                <div className="infra-stat"><b>{data}</b><span>{dataWord}{data === 1 ? '' : 's'}</span></div>
              </div>
              <div className="infra-attention">
                {unrecorded > 0 && <span className="chip pending">{unrecorded} unrecorded</span>}
                {missing > 0 && <span className="chip fail">{missing} missing</span>}
                {unrecorded === 0 && missing === 0 && t.live && <span className="chip pass">inventory matches records</span>}
                {/* The host's own state is the chip above; this is the
                    inventory's. No answer yet is not a failure (sweep 7). */}
                {t.error
                  ? <span className="chip fail" title={t.error}>inventory failed</span>
                  : !t.live && <span className="chip pending">checking…</span>}
              </div>
              <div className="infra-foot">
                <span><small>inventory</small>{t.live ? (t.asOf ? relTime(t.asOf) : 'now') : t.error ? 'no answer' : 'checking…'}</span>
                <span><small>backups</small>{!backups.data ? (backups.err ? 'could not be read' : 'reading…') : bk ? `${relTime(bk.latest)} · ${bk.kept} kept` : envs.length > 0 ? 'none yet' : 'no state'}</span>
                <span><small>offered to</small>{offers.length > 0 ? offers.map((o) => o.group === '*' ? 'everyone' : o.group).join(', ') : 'nobody'}</span>
              </div>
            </Link>
          )
        })}
        <Link to="/infrastructure/new" className="conn-card infra-add">
          <div style={{ fontWeight: 600 }}>Add a deployment target</div>
          <div className="muted small">a Docker host, a Portainer environment, a DigitalOcean account</div>
        </Link>
      </div>}
    </div>
  )
}

// OwnDisks: the filesystems this AppTown and its worker write to, each its
// own reading with the time it was read, and what it means for new work.
// A deployment target's own disk is on its page (its capacity).
export function OwnDisks() {
  const q = useGet<DiskReadings>('/infrastructure/disks', { poll: 60000 })
  if (!q.data) return <div style={{ marginTop: 16 }}><ReadStatus q={q} what={`${PRODUCT}'s disks`} className="small" /></div>
  return (
    <div style={{ marginTop: 16 }}>
      <StaleNote q={q} />
      <div style={{ overflowX: 'auto' }}>
      <table className="small" style={{ fontVariantNumeric: 'tabular-nums' }}>
        <thead><tr><th>Disk</th><th>Free</th><th>Read</th><th>New work</th></tr></thead>
        <tbody>
          {q.data.disks.map((d) => (
            <tr key={d.what + (d.path ?? '')}>
              <td>{d.what}{d.path && <span className="muted"> · {d.path}</span>}</td>
              <td style={d.unknown ? undefined : { whiteSpace: 'nowrap' }}>{d.unknown ? <>unknown <span className="muted">— {d.unknown}</span></> : `${fmtBytes(d.freeBytes ?? 0)} of ${fmtBytes(d.totalBytes ?? 0)}`}</td>
              <td className="muted" style={{ whiteSpace: 'nowrap' }}>{relTime(d.readAt)}</td>
              <td><DiskChip d={d} /></td>
            </tr>
          ))}
        </tbody>
      </table>
      </div>
    </div>
  )
}

// DiskChip: refused (below the floor, or not read where the reading admits
// work), low, not read, or room.
function DiskChip({ d }: { d: Reading }) {
  if (d.refuses) return <span className="chip fail">{d.unknown ? 'refused: not read' : `refused: under ${fmtBytes(d.floorBytes ?? 0)} free`}</span>
  if (d.unknown) return <span className="chip pending">not read</span>
  if (d.low) return <span className="chip pending">{d.warningBytes ? `low: under ${fmtBytes(d.warningBytes)} free` : 'disk space low'}</span>
  return <span className="chip pass">room</span>
}

// JobQueue: AppTown's own work (design/subsystems/jobs.md) — open jobs
// first, then the recent ones, each with its receipt or its failure.
function JobQueue() {
  const jobs = useGet<JobList>('/jobs?limit=100', { poll: 10000 })

  if (!jobs.data) return <div style={{ marginTop: 14 }}><ReadStatus q={jobs} what="the job queue" /></div>
  const open = jobs.data.jobs.filter((j) => ['available', 'pending', 'running', 'retryable', 'scheduled'].includes(j.state)).length
  return (
    <div style={{ marginTop: 14 }}>
      <StaleNote q={jobs} />
      <p className="muted small" style={{ margin: '0 0 8px', maxWidth: 760 }}>{open > 0 ? `${open} open now.` : 'Nothing open now.'}</p>
      {jobs.data.jobs.length === 0
        ? <div className="muted small">No jobs yet.</div>
        : (
          <div style={{ overflowX: 'auto' }}>
            <table className="small" style={{ fontVariantNumeric: 'tabular-nums' }}>
              <thead><tr><th>Job</th><th>State</th><th>When</th><th>Outcome</th></tr></thead>
              <tbody>
                {jobs.data.jobs.map((j) => (
                  <tr key={j.id}>
                    <td>{JOB_WORDS[j.kind] ?? j.kind} <b>{j.key}</b></td>
                    <td><Status word={j.statusLabel} tone={STATUS_LIGHT[j.statusLabel]} help={j.error || j.state} />{j.attempts > 1 ? <span className="muted"> · attempt {j.attempts}</span> : null}</td>
                    <td className="muted">{relTime(j.finishedAt || j.startedAt || j.createdAt)}</td>
                    <td className={j.error ? 'error' : ''}>{jobReceipt(j)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
    </div>
  )
}
