import { useState } from 'react'
import { useHostStates } from '../hostState'
import { useAttention } from '../attention'
import { GlanceRow } from '../GlanceRow'
import { STATUS_LIGHT, Status } from '../Status'
import { PRODUCT } from '../brand'
import { Link, NavLink, useLocation } from 'react-router-dom'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { fmtBytes, relTime } from '../format'
import { DeletedProjects, useRefreshInfrastructure } from './Infrastructure'
import type { DiskReadings, InfrastructureTargets, Job, JobList, Reading, TargetList } from '../wire.gen'
import { familyRank } from '../targets'
import { PageHead } from '../PageHead'

// Infrastructure lists hosts by name, state and one summary. Each host's
// page keeps its capacity, inventory, settings and backup details.

const JOB_WORDS: Record<string, string> = {
  backup: 'Back up apps', inventory: 'Check resources', rebuild: 'Rebuild an app', 'backup-sweep': 'Check backups', 'db-backup': `Back up ${PRODUCT}`,
  narrative: 'Write a change summary', 'protection-observation': 'Check backups', 'capacity-observation': 'Check server memory',
  'worker-observation': 'Check worker', 'disk-observation': 'Check storage', operation: 'Copy or restore data', 'remediation-pass': 'Fix findings', 'what-if': 'Compare rules',
  'eval-pass': 'Compare changes', 'project-description': 'Describe a project', 'workflow-completion': 'Finish a workflow',
  'workflow-abandonment': 'Stop a workflow', 'conformance-review': 'Review projects',
}
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
  const attention = useAttention()
  const hostState = useHostStates()
  const tab = useLocation().pathname.endsWith('/jobs') ? 'jobs' : 'targets'
  const infra = useGet<InfrastructureTargets>('/infrastructure', { poll: 30000 })
  const targets = useGet<TargetList>('/targets', { poll: 30000 })
  const { refresh, refreshing, err: refreshErr } = useRefreshInfrastructure()
  if (!infra.data || !targets.data) return <ReadStatus q={[infra, targets]} what="each deployment target's inventory" />
  const byName = new Map(targets.data.targets.map((t) => [t.name, t]))
  const live = [...infra.data.targets].sort((a, b) => familyRank(a.family) - familyRank(b.family) || a.target.localeCompare(b.target))
  return (
    <div>
      <StaleNote q={[infra, targets, attention]} />
      <PageHead page="infrastructure" />
      {tab === 'targets' && <details className="small"><summary>Options</summary><button className="subtle" onClick={refresh} disabled={refreshing}>{refreshing ? 'Refreshing…' : 'Refresh all'}</button></details>}
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
          const state = hostState(t.target, t.statusLabel)
          return (
            <Link key={t.target} to={`/infrastructure/${t.target}`} className="conn-card infra-card">
              <div className="host-row">
                <strong>{v?.displayLabel || t.target}</strong>
                <Status word={state.word} tone={state.word === 'working' ? 'pass' : STATUS_LIGHT[state.word]} help={state.help} />
                <span className="muted small">{state.summary || (missing > 0 ? 'Check missing resources' : unrecorded > 0 ? 'Review resources' : `${envs.length} ${envs.length === 1 ? 'app' : 'apps'}`)}</span>
              </div>
            </Link>
          )
        })}
        <Link to="/infrastructure/new" className="conn-card infra-add">
          <div style={{ fontWeight: 600 }}>Add a deployment target</div>
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
      <div className="host-disks">
        {q.data.disks.map(d => <details key={d.what + (d.path ?? '')} className="host-disk">
          <summary className="host-row">
            <strong>{d.what.startsWith('this ') ? PRODUCT : d.what.includes('Docker') ? 'Worker apps' : d.what.includes('worker') ? 'Worker storage' : d.what}</strong>
            <DiskChip d={d} />
            <span className="muted small">{d.unknown ? 'Disk not known' : d.low ? 'Low on disk' : `${Math.round(100 * (d.freeBytes ?? 0) / (d.totalBytes || 1))}% disk free`}</span>
          </summary>
          <div className="small">{d.what}{d.path && <p><code>{d.path}</code></p>}
            {d.unknown || `${fmtBytes(d.freeBytes ?? 0)} free of ${fmtBytes(d.totalBytes ?? 0)}`} · {relTime(d.readAt)}
            {d.refuses && <p>New work is blocked. Check the disk and free space.</p>}
            {d.warningBytes ? <p>Free some space when less than {fmtBytes(d.warningBytes)} is free.</p> : null}
            {d.floorBytes ? <p>New work stops below {fmtBytes(d.floorBytes)} free.</p> : null}
          </div>
        </details>)}
      </div>
    </div>
  )
}

// DiskChip keeps a blocked disk distinct from an unknown reading or low room.

function DiskChip({ d }: { d: Reading }) {
  const word = d.refuses ? 'blocked' : d.unknown ? 'unknown' : d.low ? 'low' : 'working'
  return <Status word={word} tone={d.unknown ? 'unknown' : d.refuses ? 'fail' : d.low ? 'pending' : 'pass'} />
}

// JobQueue: AppTown's own work (design/subsystems/jobs.md) — open jobs
// first, then the recent ones, each with its receipt or its failure.
export function groupJobs(jobs: Job[]): { job: Job; count: number }[] {
  const groups = new Map<string, { job: Job; count: number }>()
  for (const job of jobs) {
    // Only successful repeats collapse; failures and open work stay separate.
    const key = job.state === 'completed' ? `${job.kind}/${job.key}` : String(job.id)
    const group = groups.get(key)
    if (group) group.count++
    else groups.set(key, { job, count: 1 })
  }
  return [...groups.values()]
}

function JobQueue() {
  const [count, setCount] = useState(10)
  const jobs = useGet<JobList>('/jobs?limit=500', { poll: 10000 })

  if (!jobs.data) return <div style={{ marginTop: 14 }}><ReadStatus q={jobs} what="the job queue" /></div>
  const groups = groupJobs(jobs.data.jobs)
  const latest = jobs.data.jobs.find(j => j.finishedAt)?.finishedAt
  const open = jobs.data.jobs.filter((j) => ['available', 'pending', 'running', 'retryable', 'scheduled'].includes(j.state)).length
  return (
    <div style={{ marginTop: 14 }}>
      <StaleNote q={jobs} />
      <p className="muted small" style={{ margin: '0 0 8px', maxWidth: 760 }}>{open > 0 ? `${open} open now.` : `Nothing open now.${latest ? ` Last ran ${relTime(latest)}.` : ''}`}</p>
      {jobs.data.jobs.length === 0
        ? <div className="muted small">No jobs yet.</div>
        : (
          <div style={{ overflowX: 'auto' }}>
            <table className="small stack job-table" style={{ fontVariantNumeric: 'tabular-nums' }}>
              <thead><tr><th>Job</th><th>State</th><th>When</th><th>Outcome</th></tr></thead>
              <tbody>
                {groups.slice(0, count).map(({ job: j, count: repeats }) => (
                  <GlanceRow name={0} state={1} fact={2} labels={["Job", "State", "When", "Outcome"]} key={j.id}>
                    <td className="stack-lead">{JOB_WORDS[j.kind] ?? 'Background task'}{typeof j.receipt?.target === 'string' && <> on {j.receipt.target}</>}{repeats > 1 && <span className="muted"> · {repeats} done</span>}</td>
                    <td className="stack-end"><Status word={j.statusLabel} tone={STATUS_LIGHT[j.statusLabel]} help={j.error || j.state} />{j.attempts > 1 ? <span className="muted"> · attempt {j.attempts}</span> : null}</td>
                    <td className="muted">{relTime(j.finishedAt || j.startedAt || j.createdAt)}</td>
                    <td data-label="outcome" className={j.error ? 'error' : ''}>{jobReceipt(j)}<details className="inline-detail"><summary>Technical details</summary><div>{j.kind}</div><code className="job-key">{j.key}</code></details></td>
                  </GlanceRow>
                ))}
              </tbody>
            </table>
            {groups.length > count && <button onClick={() => setCount(n => n + 10)}>Show 10 more</button>}
          </div>
        )}
    </div>
  )
}
