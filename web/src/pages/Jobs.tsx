import './InfrastructureHome.css'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { ListTable, type ListColumn } from '../ListTable'
import { Status } from '../Status'
import { PRODUCT } from '../brand'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { relTime } from '../format'
import type { Job, JobList } from '../wire.gen'
import { useTechnicalDetail } from './change/TechnicalDetail'

type JobGroup = { job: Job; count: number }

const jobColumns: ListColumn<JobGroup>[] = [
  { key: 'job', label: 'Job', phone: 'name', render: ({ job: j, count: repeats }) => <>{JOB_WORDS[j.kind] ?? j.kind.replaceAll('-', ' ')}{repeats === 1 && typeof j.receipt?.target === 'string' && <> on {j.receipt.target}</>}{repeats > 1 && <span className="muted"> · {repeats} done</span>}</> },
  { key: 'state', label: 'State', phone: 'state', render: ({ job: j }) => <><Status word={j.statusLabel} />{j.error && <> <span className="error">{j.error}</span></>}{j.attempts > 1 ? <span className="muted"> · attempt {j.attempts}</span> : null}</> },
  { key: 'when', label: 'When', kind: 'time', render: ({ job: j }) => <span className="muted">{j.finishedAt ? 'Finished ' : j.startedAt ? 'Started ' : 'Queued '}{relTime(j.finishedAt || j.startedAt || j.createdAt)}</span> },
  { key: 'outcome', label: 'Outcome', render: ({ job: j }) => <>{jobReceipt(j)}<JobKey job={j} /></> },
]

// The job's kind and key are for administrators: the Job column already
// names the kind in words, so the raw line shows only with the technical switch.
function JobKey({ job }: { job: Job }) {
  const [technical] = useTechnicalDetail()
  return technical ? <code className="tech-line muted">{job.kind} · {job.key}</code> : null
}

export function JobsPage() {
  return <div><h1>Background jobs</h1><p>Backups, resource checks and workflow follow-ups run here. Safe jobs retry by themselves.</p><p><Link to="/usage">Usage</Link></p><JobQueue /></div>
}

const JOB_WORDS: Record<string, string> = {
  backup: 'Back up apps', inventory: 'Check resources', rebuild: 'Rebuild an app', 'image-retention': 'Keep release images', 'backup-sweep': 'Check backups', 'db-backup': `Back up ${PRODUCT}`,
  narrative: 'Write a change summary', 'protection-observation': 'Check backups', 'capacity-observation': 'Check server memory',
  'worker-observation': 'Check worker', 'disk-observation': 'Check storage', operation: 'Copy or restore data', 'remediation-pass': 'Fix findings', 'what-if': 'Compare rules',
  'eval-pass': 'Compare changes', 'project-description': 'Describe a project', 'workflow-completion': 'Finish a workflow',
  'workflow-abandonment': 'Stop a workflow', 'conformance-review': 'Review projects',
  'deployment-release': 'Release an app', 'deployment-recovery': 'Restore an app', 'restore-check': 'Check restored apps', 'restored-copy-sweep': 'Remove expired test copies',
  'installation-marks': 'Record installed resources', 'project-cleanup': 'Clean up project files',
  'infrastructure-check': 'Check servers and deployments', 'hourly-infrastructure-check': 'Check backups and connections',
  'integration-health': 'Check a connection', 'error-report-observation': 'Check error reports', 'backup-store-observation': 'Check the backup store',
  'target-observation': 'Check a target', 'preview-observation': 'Check previews', 'deployment-detail-observation': 'Check a deployment',
  'project-deletion-pass': 'Remove deleted apps', 'project-purge-pass': 'Remove expired projects',
}
const jobReceipt = (j: Job) => {
  if (j.error) return ''
  const r = j.receipt ?? {}
  if (typeof r.skipped === 'string') return `skipped: ${r.skipped}`
  if (typeof r.asked === 'number') return `asked for ${r.asked} backup(s)`
  if (typeof r.took === 'string') return r.took
  if (typeof r.url === 'string') return `running again at ${r.url}`
  if (typeof r.resources === 'number') return `${r.resources} ${r.resources === 1 ? 'resource' : 'resources'}`
  if (typeof r.bytes === 'number') return `${r.items ?? ''} item(s), ${Math.round((r.bytes as number) / 1024)} KB`
  return ''
}


// JobQueue: AppTown's own work (design/subsystems/jobs.md) — open jobs
// first, then the recent ones, each with its receipt or its failure.
export function groupJobs(jobs: Job[]): { job: Job; count: number }[] {
  const groups = new Map<string, { job: Job; count: number }>()
  for (const job of jobs) {
    // Only successful repeats collapse; failures and open work stay separate.
    const key = job.state === 'completed' ? job.kind : String(job.id)
    const group = groups.get(key)
    if (group) group.count++
    else groups.set(key, { job, count: 1 })
  }
  return [...groups.values()]
}

export function JobQueue() {
  const [count, setCount] = useState(10)
  const jobs = useGet<JobList>('/jobs?limit=500', { poll: 10000 })

  if (!jobs.data) return <div className="infrastructure-home-job-queue-div"><ReadStatus q={jobs} what="the job queue" /></div>
  const groups = groupJobs(jobs.data.jobs)
  const latest = jobs.data.jobs.find(j => j.finishedAt)?.finishedAt
  const open = jobs.data.jobs.filter((j) => ['available', 'pending', 'running', 'retryable', 'scheduled'].includes(j.state)).length
  return (
    <div className="infrastructure-home-job-queue-div">
      <StaleNote q={jobs} />
      <p className="muted small infrastructure-home-job-queue-p">{open> 0 ? `${open} open now.` : `Nothing open now.${latest ? ` Last ran ${relTime(latest)}.` : ''}`}</p>
      {jobs.data.jobs.length === 0
        ? <div className="muted small">No jobs yet.</div>
        : (
          <div className="infrastructure-home-job-queue-div-2">
            <ListTable label="Background jobs" className="small infrastructure-home-job-queue-job-table" rows={groups.slice(0, count)} rowKey={g => String(g.job.id)} columns={jobColumns} />
            {groups.length > count && <button onClick={() => setCount(n => n + 10)}>Show 10 more</button>}
          </div>
        )}
    </div>
  )
}
