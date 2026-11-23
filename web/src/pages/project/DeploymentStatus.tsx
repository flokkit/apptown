import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { useGet, type Read } from '../../data'
import { useHolds } from '../../instancePages'
import { useNotOff } from '../../features'
import { ListRow } from '../../ListRow'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Status } from '../../Status'
import { PRODUCT } from '../../brand'
import { post } from '../../api'
import { useAction } from '../../data'
import { AppAddress } from '../../runtime'
import { fmtBytes, fmtStamp, fmtWhen } from '../../format'
import { DeployStages, deployWords, releaseProgress } from '../change/DeployScene'
import { itemLabel } from '../DataProtection'
import { ReleaseApprovalActions } from './ReleaseApproval'
import type { ReleaseChoice } from './ChooseRelease'
import { useBackUp } from './DeploymentMenu'
import { notDeployedWords } from './DeploymentDeploys'
import { containerPath, containerRows, ContainersTable, ContainerStatus, nearLimit } from './DeploymentContainers'
import { HoldLine, NextStep } from './settings/Diagnostics'
import { DeploymentLine } from './settings/DeploymentLine'
import { AIModelsLine, backupProblem, RestoreLines, ServingRelease, timesWords } from './settings/Deployments'
import type { DeploymentChecked, DeploymentDiagnostics, DeploymentProtection, DeploymentResources, DeploymentView, ReleaseListed } from '../../wire.gen'
import '../../parts/runActivity.css'
import '../../overview-line.css'
import './settings/deployments.css'
import '../change/deploy-scene.css'
import './deployment-status.css'

// The deployment's Status tab: is it up, where, what runs, and is anything
// wrong or about to be. Two columns on a wide screen, stacked below it:
// the status rows (what it is doing, then one row per problem, worst first,
// each with the one act that fixes it), and the facts (address, what runs
// when no row said it, host and backups when no row is about them), with
// the containers beside them on a wide screen and as a line of links on a
// phone. Every fact here is one read the page already makes; a read that has
// not answered says so, and one that failed says unknown, never fine.

const STAGE_NAMES: Record<string, string> = { preparing: 'Code', 'backing-up': 'Backup', building: 'Build', starting: 'Start', checking: 'Check', live: 'Live' }

type Problem = { key: string; tone: 'bad' | 'warn' | 'unknown'; lead?: boolean; row: ReactNode }

export function DeploymentStatus({ slug, env, refreshFailed, releases, current, reload, onChoose }: {
  slug: string; env: DeploymentView; refreshFailed: boolean; releases: Read<ReleaseListed[]>; current: boolean; reload: () => Promise<void>; onChoose: (c: ReleaseChoice | 'list') => void
}) {
  const base = `/projects/${slug}/deployments/${env.name}`
  const resources = useGet<DeploymentResources>(env.generation ? `${base}/resources` : null, { poll: 5000 })
  const diagnostics = useGet<DeploymentDiagnostics>(env.generation ? `${base}/diagnostics` : null, { poll: 5000 })
  // The owner says whether it keeps backups for this deployment; a
  // deployment nothing has run on yet has nothing to back up.
  const hasProtection = useNotOff('backups') && env.backups && !!env.generation
  const protection = useGet<DeploymentProtection>(hasProtection ? `${base}/protection` : null, { poll: 5000 })
  const operate = useHolds('deployment:operate', slug)
  const check = useAction(undefined, { affects: [`/projects/${slug}/deployments`] })
  const backUp = useBackUp(slug, env)
  const checkNow = operate ? <button disabled={check.busy} onClick={() => check.run(async () => { await post<DeploymentChecked>(`${base}/check`, {}); await reload() })}>Check now</button> : undefined

  const history = releases.data?.filter(r => r.deployment === env.name) ?? []
  const latest = history[0]
  const personStopped = env.runtimeState === 'stopped'
  const notDeployed = !env.generation && !env.status.servingReleaseNumber && !!releases.data && history.length === 0
  const deploying = latest && latest.state === 'releasing' ? latest : undefined
  const failed = latest?.state === 'failed' ? latest : undefined
  const containers = env.generation && (resources.data || diagnostics.data) && !resources.loading && !diagnostics.loading
    ? containerRows(resources, diagnostics, personStopped) : undefined
  const share = resources.data?.share
  const word = env.status.word
  // The host did not answer, as the owner says: a browser's failed refresh
  // is the page's stale note, never evidence about the host.
  const hostSilent = !refreshFailed && (word === 'unreachable' || word === 'unknown' && env.status.stale)
  const target = env.target
  const hostLink = (children: ReactNode) => target ? <Link to={`/targets/${encodeURIComponent(target.name)}`}>{children}</Link> : children
  const hostName = target ? target.displayLabel || target.name : 'its host'
  // What runs, as the facts card says it under "Runs": the release and when
  // it went live; any other answer of the owner's (recorded, unknown, none)
  // in its own words.
  const n = env.status.servingReleaseNumber
  // A current reading that it runs (the observation, not the overall word,
  // which a failing part can make failed) lets the release be named plainly.
  const serving = n && env.running === true && !!env.status.servingRelease?.startsWith('Running')
    ? <><Link to={`/projects/${slug}/releases/${n}`}>release {n}</Link>{env.servingRelease?.number === n && ` · ${fmtStamp(env.servingRelease.releasedAt || env.servingRelease.createdAt)}`}</>
    : <ServingRelease slug={slug} env={env} />

  const problems: Problem[] = []
  const row = (key: string, tone: Problem['tone'], props: { name: ReactNode; to?: string; state: ReactNode; sub?: ReactNode; action?: ReactNode; detail?: ReactNode }, lead = tone === 'bad') =>
    problems.push({ key, tone, lead, row: <ListRow key={key} to={props.to} className="card row run-list-row" state={<span className="row-status">{props.state}</span>}
      identity={<><strong>{props.to ? <Link className="run-link" to={props.to}>{props.name}</Link> : props.name}</strong>{props.sub && <span className="muted small">{props.sub}</span>}</>}
      action={props.action} detail={props.detail} /> })

  if (hostSilent) row('host-silent', 'unknown', { name: hostName, to: target ? `/targets/${encodeURIComponent(target.name)}` : undefined,
    state: <Status observation word={env.status.word} />, sub: env.status.reason, action: checkNow }, true)
  if (failed) row('failed', 'bad', { name: `Release ${failed.number}`, to: `/projects/${slug}/releases/${failed.number}`,
    state: <Status word="failed" reason={failed.failedStage ? `failed at ${STAGE_NAMES[failed.failedStage]}` : undefined} />,
    sub: <>{failed.failure}{failed.failure && !/[.!?]$/.test(failed.failure) ? '.' : ''} {env.status.servingRelease}</>,
    action: failed.bundleId ? <button disabled={!current || !releases.current} onClick={() => onChoose({ release: failed })}>Try again</button> : undefined })
  for (const r of history.filter(r => r.state === 'waiting for approval')) row(`waiting-${r.number}`, 'warn', { name: `Release ${r.number}`, to: `/projects/${slug}/releases/${r.number}`,
    state: <Status word={r.approvalRole ? 'waiting for approval' : 'waiting'} />, sub: r.changes.map(c => `#${c.number} ${c.title}`).join(' · '),
    detail: <ReleaseApprovalActions slug={slug} release={r} decision={r.decision} current={current && releases.current} /> }, true)
  if (containers && !hostSilent && !deploying) {
    for (const c of containers.rows) {
      if (c.state.word === 'failed') {
        const kills = resources.data?.limitsKnown ? resources.data.limits.filter(l => l.part === c.part) : []
        row(`part-${c.part}`, 'bad', { name: c.label, to: containerPath(slug, env.name, c.part), state: <ContainerStatus state={c.state} />,
          sub: <>{kills.length > 0 && <>{kills.map(l => `${timesWords(l.count)} in the last day, last ${fmtWhen(l.last)}`).join(' · ').replace(/^./, t => t.toUpperCase())}. </>}{!failed && <NextStep p={c.diag!} settings={`${base}?tab=settings`} container={containerPath(slug, env.name, c.part)} />}</> })
      } else if (c.state.word === 'running' && nearLimit(containers.usage(c))) {
        const u = containers.usage(c)!
        row(`near-${c.part}`, 'warn', { name: c.label, to: containerPath(slug, env.name, c.part), state: <Status word="low" reason="near its memory limit" />, sub: `${fmtBytes(u.usedBytes)} of ${fmtBytes(u.limitBytes)}` })
      }
    }
  }
  if (diagnostics.data?.hold) row('hold', 'bad', { name: 'Restarts', state: <Status word={diagnostics.data.hold.confirmed ? 'stopped' : 'unknown'} />, sub: <HoldLine slug={slug} d={diagnostics.data} /> })
  const backups = hasProtection && !protection.loading ? backupProblem(protection) : ''
  if (backups) {
    const p = protection.data
    const named = (p?.items ?? []).filter(i => backups !== 'none' || i.copy === 'none').map(i => itemLabel(i, p!.items)).join(', ')
    const why = backups === 'unknown' ? p?.unknown || p?.reading?.failure || (p?.reading?.waiting ? 'waiting for the first check' : protection.reason) : ''
    const newest = newestCopy(p)
    row('backups', backups === 'unknown' ? 'unknown' : 'bad', { name: 'Backups', to: `${base}/backups`,
      state: backups === 'unknown' ? <Status word="unknown" help={p?.unknown || protection.reason} /> : <Status word={backups === 'none' ? 'missing' : 'overdue'} reason={backups === 'none' ? 'none yet' : 'overdue'} />,
      sub: [why || named, newest ? `newest ${fmtWhen(newest)}` : ''].filter(Boolean).join(' · '),
      action: backUp.backUp && backups !== 'unknown' ? <button disabled={backUp.busy || !env.generation} onClick={backUp.backUp}>Back up now</button> : undefined }, false)
  }
  const host = hostSilent ? [] : hostProblems(env, resources)
  for (const h of host) row(`host-${h.key}`, h.tone, { name: hostName, to: target ? `/targets/${encodeURIComponent(target.name)}` : undefined, state: h.state, sub: h.sub }, h.tone === 'bad')
  if (env.feed === 'main' && !env.feedConfirmed && env.kind === 'standing') row('feed', 'warn', { name: 'Automatic deploys', to: '?tab=settings', state: <Status word="waiting" reason="paused" />, sub: 'until a person saves the Release feed setting' }, false)
  if (env.readiness === 'uncertain') row('readiness', 'unknown', { name: 'This deployment', state: <Status word={env.readinessLabel} />,
    sub: `${PRODUCT} does not know whether it stands: its last attempt was cut off, or its records disagree. Check now asks its host and settles what it finds; it deploys nothing.`, action: checkNow }, false)
  else if (env.readiness && env.readiness !== 'reconciled' && !(env.readiness === 'failed' && failed)) row('readiness', env.readiness === 'failed' ? 'bad' : 'unknown', { name: 'This deployment', state: <Status word={env.readinessLabel} />, sub: `Deployment: ${env.readiness}.` })

  // The lead row says what the deployment is doing, unless a failure, a
  // host that does not answer or a release waiting on someone leads: then
  // what runs moves to the facts card.
  const leadProblem = !deploying && !notDeployed && problems[0]?.lead
  // Readings taken since the last release are still on their way: said on
  // the line that says what runs, wherever that is.
  const waiting = [resources.data?.reading, diagnostics.data?.reading, protection.data?.reading].some(r => r?.waiting)
  const checking = waiting && env.lastApplied ? `Checking after the release (since ${fmtWhen(env.lastApplied.at)})` : ''
  const lead = notDeployed
    ? <ListRow className="card row run-list-row" state={<span className="row-status"><Status word="not deployed" /></span>}
      identity={<><strong>Not deployed yet</strong><span className="muted small">{notDeployedWords(env)}</span></>}
      action={env.feed !== 'main' ? <button className="primary" disabled={!current} onClick={() => onChoose('list')}>Deploy a release…</button> : undefined} />
    : deploying ? <DeployingRow slug={slug} env={env} release={deploying} progressCurrent={!releases.refreshFailed} />
    : leadProblem ? null
    : <ListRow className="card row run-list-row" state={<span className="row-status"><Status observation word={env.status.word} /></span>}
      identity={<><strong>{env.status.servingReleaseNumber ? <Link className="run-link" to={`/projects/${slug}/releases/${env.status.servingReleaseNumber}`}>Release {env.status.servingReleaseNumber}</Link> : env.status.servingRelease}</strong>
        <span className="muted small">{[personStopped ? `${env.status.reason}${env.runtimeAt ? ` · ${fmtStamp(env.runtimeAt)}` : ''}`
          : word === 'working' && env.servingRelease && env.servingRelease.number === env.status.servingReleaseNumber ? `since ${fmtStamp(env.servingRelease.releasedAt || env.servingRelease.createdAt)}` : '',
          checking].filter(Boolean).join(' · ')}</span></>} />

  const narrowContainers = containers && containers.rows.length > 0
    ? <DeploymentLine label={containers.noContainers ? 'Website' : 'Containers'}><span className="deployment-container-links">{containers.rows.map((c, i) => <span key={c.part}>{i > 0 && ' · '}<Link to={containerPath(slug, env.name, c.part)}>{c.label}</Link></span>)}</span></DeploymentLine>
    : null
  const newest = newestCopy(protection.data)
  const factLines = <>
    {(env.url || !notDeployed) && <DeploymentLine label="Address">{env.url ? <AppAddress url={env.url} hostOnly={env.hostOnly} /> : <span className="muted">no address yet</span>}</DeploymentLine>}
    {leadProblem && env.kind === 'standing' && <DeploymentLine label="Runs">{serving}{checking && <div className="muted small">{checking}</div>}</DeploymentLine>}
    {narrowContainers && <div className="deployment-narrow-only">{narrowContainers}</div>}
    {target && host.length === 0 && !hostSilent && <DeploymentLine label="Host">{hostLink(hostName)}{share?.elastic && ' · grows on demand'}</DeploymentLine>}
    {hasProtection && !backups && !protection.denied && <DeploymentLine label="Backups">{protection.data
      ? <Link to={`${base}/backups`}>{protection.data.items.length === 0 ? 'no data declared' : newest ? fmtWhen(newest) : 'see backups'}</Link>
      : <ReadStatus q={protection} what="its backups" />}</DeploymentLine>}
    {env.aiModels && env.aiModels.length > 0 && <AIModelsLine slug={slug} name={env.name} />}
    <RestoreLines env={env} />
  </>

  return <div className="deployment-status">
    <div className="section-body deployment-status-rows">
      <ReadStatus q={releases} what="its releases" className="deployment-status-read" />
      <StaleNote q={[releases, protection]} />
      {lead}
      {problems.map(p => p.row)}
      {check.err && <p className="error small" role="alert">{check.err}</p>}
      {backUp.err && <p className="error small" role="alert">{backUp.err}</p>}
    </div>
    <div className="stack stack-3 deployment-status-facts">
      <div className="section-body"><div className="card deploy-entry"><div className="deploy-lines">{factLines}</div></div></div>
      {env.generation ? <>
        <ReadStatus q={[resources, diagnostics]} what="its containers" />
        <StaleNote q={[resources, diagnostics]} />
        {containers && <div className="section-body deployment-wide-only"><ContainersTable slug={slug} env={env} {...containers} /></div>}
      </> : null}
    </div>
  </div>
}

// DeployingRow is a deploy on its way: the release, what it carries and
// what serves meanwhile, and its stages. The time spent in a stage is the
// release log's and the change's live panel's, not this summary's.
function DeployingRow({ slug, env, release: r, progressCurrent }: { slug: string; env: DeploymentView; release: ReleaseListed; progressCurrent: boolean }) {
  const { uncertain, outcome } = releaseProgress(r, progressCurrent)
  const stage = r.stage ?? r.lastStage
  return <ListRow className="card row run-list-row" state={<span className="row-status"><Status observation word={env.status.word} /></span>}
    identity={<><strong><Link className="run-link" to={`/projects/${slug}/releases/${r.number}`}>Release {r.number}</Link></strong>
      <span className="muted small">{r.changes.map(c => `#${c.number} ${c.title} · `).join('')}<ServingRelease slug={slug} env={env} /></span></>}
    detail={<>
      <DeployStages stage={stage} outcome={outcome} compact />
      {uncertain && <p>{stage ? `Last seen: ${deployWords[stage]} · ` : ''}{r.progressUncertain ? r.interruptedByRestart ? 'Interrupted by a restart — checking.' : 'Deployment interrupted; being checked.' : 'Current progress is unknown until releases can be read again.'}</p>}
    </>} />
}

// newestCopy is when the newest backup of any item was taken.
function newestCopy(p?: DeploymentProtection | null): string | undefined {
  return (p?.items ?? []).map(i => i.lastCopy?.takenAt).filter((t): t is string => !!t).sort().at(-1)
}

// hostProblems is what is wrong with the deployment's host, each said once:
// out of disk or low on disk, its disk and memory unknown, and its
// containers' memory limits adding up past its memory. A host that grows on
// demand has none of these.
export type HostProblem = { key: string; tone: Problem['tone']; state: ReactNode; sub?: string }
export function hostProblems(env: DeploymentView, resources: Read<DeploymentResources>): HostProblem[] {
  const share = resources.data?.share
  if (!env.generation || !env.target || share?.elastic || resources.loading || resources.denied) return []
  const current = !!share?.readAt && !share.error && !share.stale && !resources.data?.reading?.waiting && !resources.refreshFailed
  if (!current) return [{ key: 'unknown', tone: 'unknown', state: <Status word="unknown" />, sub: 'disk and memory could not be read' }]
  const out: HostProblem[] = []
  if (share!.diskRefuses) out.push({ key: 'disk', tone: 'bad', state: <Status word="failed" reason="out of disk" />, sub: 'New deploys are blocked.' })
  else if (share!.lowDisk) out.push({ key: 'disk', tone: 'warn', state: <Status word="low" reason="low on disk" /> })
  if (share!.overcommitted && share!.limits?.known && share!.hostMemory.known) out.push({ key: 'memory', tone: 'warn', state: <Status word="low" reason="over-committed" />, sub: `Memory limits ${fmtBytes(share!.limits.value)}, host ${fmtBytes(share!.hostMemory.value)}.` })
  return out
}
