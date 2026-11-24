import type { ReactNode } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useGet } from '../data'
import { useHolds } from '../instancePages'
import { ListRow } from '../ListRow'
import { ReadStatus, StaleNote } from '../ReadGate'
import { Status } from '../Status'
import { Tabs } from '../Tabs'
import { fmtBytes, fmtStamp } from '../format'
import { PRODUCT } from '../brand'
import { currentStep, kindWords, live, stateWords, StoreSettings } from './DataProtection'
import { DeploymentLine } from './project/settings/DeploymentLine'
import { protectionProblem } from './project/settings/Deployments'
import { useBackUp } from './project/DeploymentMenu'
import type { AllBackups, BackupDeployments, BackupItem, BackupStoreState, CopyRef, DeploymentProtection, FleetDeployment, Operation, ProjectBackup } from '../wire.gen'
import '../overview-line.css'
import './project/settings/deployments.css'
import './project/deployment-status.css'
import '../parts/runActivity.css'

// Backups: is each app's data safe, and can it come back. One row per
// deployment that holds data (the owner's list), trouble first, each with
// the one act that fixes it; beside them the store, last night's copies and
// the last restore. Each row opens that deployment's backups, where its
// copies are and each can be restored. The store and the keep rule are on
// Settings.

// DISK is the one name for where copies are without a backup store: under
// AppTown's own data directory, whichever host the app runs on.
export const DISK = `${PRODUCT}'s disk`

export const BACKUP_TABS = [{ key: 'status', label: 'Status' }, { key: 'settings', label: 'Settings' }] as const

export function BackupsView() {
  const tab = new URLSearchParams(useLocation().search).get('tab') === 'settings' ? 'settings' : 'status'
  return <>
    <Tabs className="tabs deployment-tabs" role="navigation" aria-label="Backups">
      {BACKUP_TABS.map(t => <Link key={t.key} to={t.key === 'status' ? '?' : `?tab=${t.key}`} className={'tab' + (t.key === tab ? ' active' : '')} aria-current={t.key === tab ? 'page' : undefined}>{t.label}</Link>)}
    </Tabs>
    {tab === 'settings' ? <StoreSettings /> : <BackupStatus />}
  </>
}

function BackupStatus() {
  const q = useGet<BackupDeployments>('/backups/deployments', { poll: 30000 })
  const sets = useGet<AllBackups>('/backups', { poll: 30000 })
  const store = useGet<BackupStoreState>('/backup-store')
  const admin = useHolds('platform:manage-deploy-targets')
  const nav = useNavigate()
  const noStore = !!store.data && !store.data.store && !store.denied
  const rows = q.data?.deployments ?? []
  const attached = !!store.data?.store
  const judged = rows.map(r => ({ r, v: backupVerdict(r.protection, q.refreshFailed, r.newest, attached) }))
    .sort((a, b) => a.v.rank - b.v.rank)
  const gone = q.data && sets.data ? takenDown(sets.data.backups) : []
  const names = new Map(rows.map(r => [r.project, r.projectName]))
  return <div className="deployment-status">
    <div className="section-body deployment-status-rows">
      <ReadStatus q={q} what="the deployments' backups" className="deployment-status-read" />
      <StaleNote q={[q, sets, store]} />
      {q.data && <ReadStatus q={sets} what="the backups of deployments taken down" className="deployment-status-read" />}
      {noStore && <ListRow className="card row run-list-row" to={admin ? '?tab=settings' : undefined} state={<span className="row-status"><Status word="low" reason="not attached" /></span>}
        identity={<><strong>No backup store</strong><span className="muted small">Copies stay on {DISK} and go if it does.</span></>}
        action={admin ? <button onClick={() => nav('?tab=settings')}>Attach…</button> : undefined} />}
      {judged.map(({ r, v }) => <FleetRow key={`${r.project}/${r.deployment}`} row={r} verdict={v} />)}
      {gone.map(b => <ListRow key={`${b.project}/${b.deployment}`} className="card row run-list-row" to={backupsPath(b.project, b.deployment)}
        state={<span className="row-status"><Status word="stopped" reason="taken down" /></span>}
        identity={<><strong><Link className="run-link" to={backupsPath(b.project, b.deployment)}>{names.get(b.project) ?? b.project} <span className="muted">{b.deploymentLabel || b.deployment}</span></Link></strong><span className="muted small">{takenDownWords(b)}</span></>} />)}
      {q.data && sets.data && rows.length === 0 && gone.length === 0 && <ListRow className="card row run-list-row"
        identity={<><strong>Nothing to back up yet</strong><span className="muted small">Copies start when an app declares a database, files or a bucket.</span></>} />}
    </div>
    {q.data && <div className="stack stack-3 deployment-status-facts">
      <div className="section-body"><div className="card deploy-entry"><div className="deploy-lines">
        {!store.denied && <DeploymentLine label="Store">{store.data
          ? store.data.store ? <><Link to="?tab=settings">{store.data.store.bucket}</Link>{store.data.keptBytes != null && ` · ${fmtBytes(store.data.keptBytes)}`}</> : <span className="muted">none</span>
          : <ReadStatus q={store} what="the store" />}</DeploymentLine>}
        {rows.length > 0 && <DeploymentLine label="Last night">{q.data.lastNight.copied} of {q.data.lastNight.of} copied{q.data.lastNight.at && <span className="muted"> · {clock(q.data.lastNight.at)}</span>}</DeploymentLine>}
        <DeploymentLine label="Last restore"><LastRestore rows={rows} /></DeploymentLine>
      </div></div></div>
    </div>}
  </div>
}

// FleetRow is one deployment: its verdict, why, and Back up now when its
// backups are in trouble, for whoever may deploy in its project.
function FleetRow({ row, verdict: v }: { row: FleetDeployment; verdict: Verdict }) {
  const backUp = useBackUp(row.project, { name: row.deployment, generation: 1, target: { kind: row.targetKind } })
  const to = backupsPath(row.project, row.deployment)
  return <ListRow className="card row run-list-row" to={to} state={<span className="row-status">{v.state}</span>}
    identity={<><strong><Link className="run-link" to={to}>{row.projectName} <span className="muted">{row.deploymentLabel}</span></Link></strong><span className="muted small">{v.why}</span></>}
    action={v.backUp && backUp.backUp ? <button disabled={backUp.busy || !!row.protection.running || copyDoor(row.protection).disabled} title={copyDoor(row.protection).why} onClick={backUp.backUp}>Back up now</button> : undefined}
    detail={backUp.err ? <span className="error" role="alert">{backUp.err}</span> : undefined} />
}

export const backupsPath = (slug: string, name: string) => `/projects/${slug}/deployments/${encodeURIComponent(name)}/backups`

const clock = (iso: string) => new Date(iso).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })

// LastRestore is the newest restore put back, or restore test or rehearsal
// passed, across the listed deployments.
function LastRestore({ rows }: { rows: FleetDeployment[] }) {
  const done: { at: string; row: FleetDeployment; words: string }[] = []
  for (const row of rows) {
    const p = row.protection
    for (const op of p.recent) if (op.kind === 'deployment.restore' && op.state === 'succeeded' && op.finishedAt) done.push({ at: op.finishedAt, row, words: 'restored' })
    if (p.restoreTest?.outcome?.startsWith('passed')) done.push({ at: p.restoreTest.checkedAt || p.restoreTest.startedAt, row, words: 'test passed' })
    if (p.rehearsal?.passed) done.push({ at: p.rehearsal.at, row, words: 'test passed' })
  }
  const last = done.sort((a, b) => b.at.localeCompare(a.at))[0]
  if (!last) return <span className="muted">never</span>
  return <>{last.row.projectName} {last.row.deploymentLabel} <span className="muted">· {last.words} {fmtStamp(last.at)}</span></>
}

// takenDown is the newest retained set of each deployment taken down,
// one per deployment, as the sets record it.
export function takenDown(backups: ProjectBackup[]): ProjectBackup[] {
  const seen = new Map<string, ProjectBackup>()
  for (const b of backups) {
    const k = `${b.project}/${b.deployment}`
    if (b.deploymentRemoved && b.status !== 'pruned' && !seen.has(k)) seen.set(k, b)
  }
  return [...seen.values()]
}

// takenDownWords says what a taken-down deployment's newest retained set is,
// by its trigger and outcome: only a take-down set that holds data is a
// final copy; any other good set is the newest copy; a failed one says so.
export function takenDownWords(b: ProjectBackup): string {
  const holds = b.status === 'ok' || b.status === 'partial'
  if (holds && b.trigger === 'take-down') return `final copy ${fmtStamp(b.takenAt)}${b.status === 'partial' ? ', partial' : ''} · kept with the project`
  if (holds) return `newest copy ${fmtStamp(b.takenAt)}${b.status === 'partial' ? ', partial' : ''}`
  return `last copy failed ${fmtStamp(b.takenAt)}${b.error ? `: ${b.error}` : ''}`
}

// copyDoor is whether Back up now can start, as the copy door answers it:
// greyed with its reason when it cannot. A Docker host's or Portainer
// environment's copy is its backups (backups-page), which can always be asked.
export function copyDoor(p: DeploymentProtection): { disabled: boolean; why?: string } {
  const c = p.operations?.copy
  return c && c.reason !== 'backups-page' && c.reason !== 'not-on-this-platform' && !c.available ? { disabled: true, why: c.why } : { disabled: false }
}

// leadNote is the owner's why for the deployment's backups problem, which
// the verdict says once; an item that says the same need not repeat it.
export const leadNote = (p: DeploymentProtection) => p.items.filter(i => i.copy === 'none' || i.copyLabel === 'overdue').map(i => i.copyNote).find(Boolean)

export type Verdict = { rank: number; state: ReactNode; why: ReactNode; backUp: boolean; lead: boolean }

// latestEnded is the newest operation on the deployment that has ended.
export const latestEnded = (p: DeploymentProtection): Operation | undefined => p.recent.find(o => !live(o))

const opName = (op: Operation) => (kindWords[op.kind] ?? op.kind).toLowerCase()
const opLink = (op: Operation, words: string) => <Link to={`/operations/${op.id}`}>{words}</Link>

// backupVerdict is a deployment's backups in one state, worst first, from
// the owner's answer and the shared rule (protectionProblem): no copy yet,
// overdue and unknown; then a failed or unknown last operation, one
// running, production data only on AppTown's disk, backed up. A running
// operation, and a failed or unknown one under a backup problem, is named
// in why whatever the state says.
export function backupVerdict(p: DeploymentProtection, refreshFailed: boolean, newest?: CopyRef | null, storeAttached = false, sayNewest = true): Verdict {
  const problem = protectionProblem(p, refreshFailed)
  const ended = latestEnded(p)
  const endedBad = ended && (ended.state === 'failed' || ended.state === 'unknown') ? ended : undefined
  const when = newest && sayNewest ? `newest ${fmtStamp(newest.takenAt)}` : ''
  const running = p.running ? opLink(p.running, currentStep(p.running)) : null
  const parts = (...xs: ReactNode[]) => {
    const shown = xs.filter(Boolean)
    return <>{shown.map((x, i) => <span key={i}>{i > 0 && ' · '}{x}</span>)}</>
  }
  if (problem === 'none' || problem === 'overdue') {
    const items = p.items.filter(i => problem === 'none' ? i.copy === 'none' : i.copyLabel === 'overdue')
    const note = items.map(i => i.copyNote).find(Boolean)
    return { rank: problem === 'none' ? 0 : 0.5, lead: true, backUp: true,
      state: problem === 'none' ? <Status word="missing" reason="no copy yet" /> : <Status word="overdue" reason="overdue" />,
      why: parts(running, endedBad && opLink(endedBad, `${opName(endedBad)} ${endedBad.state}`), when, note ?? (problem === 'none' ? items.map(i => i.need).join(', ') : '')) }
  }
  if (problem === 'unknown') {
    const reason = p.unknown || p.reading?.failure || (p.reading?.waiting ? 'waiting for the first check' : refreshFailed ? 'the last answer is shown; it could not be read again' : 'the last check is too old')
    return { rank: 1, lead: true, backUp: false, state: <Status word="unknown" />,
      why: parts(running, endedBad && opLink(endedBad, `${opName(endedBad)} ${endedBad.state}`), reason, when) }
  }
  if (endedBad) return { rank: 1.5, lead: false, backUp: false, state: <Status word={endedBad.statusLabel} reason={`${opName(endedBad)} ${endedBad.state}`} />,
    why: parts(running, opLink(endedBad, stateWords(endedBad)), endedBad.finishedAt ? fmtStamp(endedBad.finishedAt) : '') }
  if (p.running) return { rank: 2, lead: false, backUp: false,
    state: <Status word="running" reason={p.running.kind === 'deployment.restore' ? 'restoring' : p.running.kind === 'deployment.copy' ? 'backing up' : opName(p.running)} />, why: running }
  const size = newest ? `${fmtStamp(newest.takenAt)} · ${fmtBytes(newest.bytes)}` : ''
  if (p.dataClass === 'production' && !p.offHost && p.keptOn === 'host')
    return { rank: 3, lead: true, backUp: false, state: <Status word="low" reason={`${DISK} only`} />, why: size }
  return { rank: 4, lead: false, backUp: false, state: <Status word="fresh" reason="backed up" />,
    why: parts(size, storeAttached && p.keptOn === 'host' ? DISK : '') }
}

// itemBadge is how an item was copied, as a fact: a database's own dump, a
// service's own copy, or files, which a running service may change mid-copy.
export function itemBadge(i: BackupItem): string | null {
  if (i.error) return 'not copied'
  if (i.copy === 'declared') return i.format ? 'database copy' : "service's own copy"
  if (i.copy === 'files') return 'file copy, app stopped'
  if (i.copy === 'live-files') return 'file copy, app running'
  return null
}

// liveFilesLimit is what a copy of a running app's files cannot promise.
// The set still finished: its status says so, and this line says why the
// copy may not match any one moment.
export const liveFilesLimit = 'Copied while the app was running: a file it was writing at that moment may be incomplete in this copy.'

export const itemName = (i: { kind: string; name: string }) => i.kind === 'volume' ? 'data volume' : i.name

// caveatNote is what a set's copies do not hold, as each need's kind
// declares it (object storage's ACLs and versions), for the question before a restore:
// a known loss is said where the decision is made. Empty when none.
export function caveatNote(items: { kind: string; name: string; caveat?: string }[]): string {
  const said = items.filter((i) => i.caveat).map((i) => `${itemName(i)}: ${i.caveat}`)
  return said.length ? ` Not in this copy · ${said.join('; ')}.` : ''
}

// managedCopy is a copy of a managed database: never restored over what
// is there, only started from. A Docker host's set is restored in place
// wherever it is kept, on AppTown's disk or in the backup store.
export const managedCopy = (b: ProjectBackup) => b.items.some((i) => !!i.instance)
