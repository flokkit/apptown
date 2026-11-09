import { useNotOff } from '../../../features'
import './deployments.css'
import { deploymentName } from '../../../deploymentName'
import { DeploymentFeed, feedWords } from './DeploymentFeed'
import '../../../overview-line.css'
import { DeploymentDeploys, type DeploymentTab } from '../DeploymentDeploys'
import { Tabs } from '../../../Tabs'
import { DeploymentContainers } from '../DeploymentContainers'
import { DeploymentTable } from '../../DeploymentTable'
import { setPageContext } from '../../../chrome'
import { TechnicalWords, useTechnicalDetail } from '../../change/TechnicalDetail'
import { DeploymentLine } from './DeploymentLine'
import { Status } from '../../../Status'
import { DeploymentAddressEdit } from './DeploymentAddress'
import { ReleaseDefaultPicker } from './ReleaseDefault'
import { DisplayLabelEdit } from '../../../DisplayLabel'
import { PRODUCT } from '../../../brand'
import { AppAddress, failedRuntimeRefresh } from '../../../runtime'
import { useEffect, useLayoutEffect, useState } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router-dom'
import { del, post, put, type ChangeView } from '../../../api'
import { useAction, useGet, useRefresh } from '../../../data'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import { fmtBytes, fmtWhen, fmtStamp } from '../../../format'
import { AUDIENCES, DATA_CLASSES, OfferedTarget, Retained, capsWords, placeOf, placeWords } from '../../../targets'
import { ProtectionLines } from '../../DataProtection'
import { Section } from '../../../Section'
import { ListTable, type ListColumn } from '../../../ListTable'
import type { AIModelsKey, DeploymentAIModels, DeploymentProtection, DeploymentResources, DeploymentDiagnostics, OverrideResult, ProjectSecrets, DeploymentView, FinalCopies, ProjectTargets, ReleaseListed, ReleaseRequested, RetainedList } from '../../../wire.gen'

// Project deployments (design/subsystems/deployment.md §2.3, §2.7, §15.7): the
// deployments of this project, each bound to a target the
// project was offered. A project opts in here; what it may opt into is
// whatever its group, or everyone, was offered. Deploying ships the latest
// bundle and opens the release; tearing down backs up the deployment's data,
// then deletes everything AppTown made for it. Data on a target without
// backups, and anything a delete failed on, is listed below until a person
// deletes it and says so.

export function DeploymentsTab({ slug }: { slug: string }) {
  const envsQ = useGet<DeploymentView[]>(`/projects/${slug}/deployments`, { poll: 5000 })
  const offeredQ = useGet<ProjectTargets>(`/projects/${slug}/targets`)
  const retainedQ = useGet<RetainedList>(`/projects/${slug}/retained`)
  const [adding, setAdding] = useState(false)
  const envs = envsQ.data && offeredQ.data && retainedQ.data ? envsQ.data : null
  const offered = offeredQ.data?.targets ?? []
  const retained = retainedQ.data?.retained ?? []
  const load = useRefresh('/projects')
  const choices = offered.filter((t) => t.origin !== 'built-in' || t.kind === 'docker-host')

  if (!envs) return <ReadStatus q={[envsQ, offeredQ, retainedQ]} what="the deployments" />
  const open = retained.filter((r) => r.disposition === 'retained')
  const settled = retained.filter((r) => r.disposition !== 'retained')

  // The project’s Deployments page owns the list and its creation form.
  return (
    <div className="deployments-settings">
      <StaleNote q={[envsQ, offeredQ, retainedQ]} />
      <Section title="Where it runs" count={envs.length || undefined}
        note={choices.length === 0 ? 'No deployment target has been offered to this project.' : undefined}
        action={!adding && choices.length > 0 ? <button onClick={() => setAdding(true)}>New deployment</button> : undefined}>
        {adding && <AddDeployment slug={slug} choices={choices} existing={envs} onDone={(added) => { setAdding(false); if (added) load() }} />}
        {envs.length === 0 && !adding && <p className="muted">Nothing deploys yet. {choices.length === 0 ? 'Ask an administrator to offer a deployment target, then choose New deployment.' : 'Choose New deployment to add one.'}</p>}
        {envs.length > 0 && <DeploymentTable release={r => <DeploymentReleaseCell slug={slug} env={envs.find(e => e.name === r.deployment)!} />}
          rows={envs.map(e => ({ ...(envsQ.refreshFailed ? failedRuntimeRefresh(e, envsQ.reason) : e), slug, app: slug, deployment: e.name, label: deploymentName(e), static: e.static, url: e.url, hostOnly: e.hostOnly }))} />}
      </Section>

      <ReleaseDefaultPicker slug={slug} deployments={envs.filter(e => e.kind !== 'preview')} />

      {(open.length > 0 || settled.length > 0) && (
        <Section title="Old resources" count={open.length || undefined}>
          <RetainedTable rows={[...open, ...settled]} />
        </Section>
      )}
    </div>
  )
}

function FeedPause({ env: e }: { env: DeploymentView }) {
  return e.feed === 'main' && !e.feedConfirmed
    ? <div className="muted small">Automatic deploys are paused until a person saves the Release feed setting.</div>
    : null
}

function ServingRelease({ slug, env: e }: { slug: string; env: DeploymentView }) {
  const number = e.status.servingReleaseNumber
  const release = e.servingRelease
  return <>{number ? <Link to={`/projects/${slug}/releases/${number}`}>{e.status.servingRelease}</Link> : e.status.servingRelease}{release && release.number === number && e.status.word !== 'deploying' && <> · {fmtStamp(release.releasedAt || release.createdAt)}</>}</>
}

function promotionHelp(e: DeploymentView) {
  return e.feed === 'promotion'
    ? e.promotionBlocked || `Deploys release ${e.promotionRelease}, the release ${e.promotionSource} last deployed successfully.`
    : undefined
}

function deployLabel(e: DeploymentView) {
  return e.feed === 'promotion' ? e.promotionRelease ? `Promote release ${e.promotionRelease}` : 'Promote' : 'Deploy latest'
}

// DeploymentReleaseCell is a deployment's release facts for the project's
// deployments table: what it runs (or which change it previews), where its
// releases come from and who approves them, and what holds a release up.
// Its backup and host warnings stay here too, beside the table's last-backup
// and resources columns, so nothing the list said is lost.
export function DeploymentReleaseCell({ slug, env: e }: { slug: string; env: DeploymentView }) {
  const base = `/projects/${slug}/deployments/${e.name}`
  const resources = useGet<DeploymentResources>(e.generation ? `${base}/resources` : null, { poll: 5000 })
  const hasProtection = useNotOff('backups') && (e.target?.kind === 'do-app' || e.target?.kind === 'docker-host')
  const protection = useGet<DeploymentProtection>(hasProtection ? `${base}/protection` : null, { poll: 5000 })
  const change = useGet<ChangeView>(e.kind === 'preview' && e.changeId ? `/changes/${e.changeId}` : null)
  const releases = useGet<ReleaseListed[]>(e.kind !== 'preview' ? `/projects/${slug}/releases` : null)
  const waitingRelease = releases.data?.find(r => r.deployment === e.name && r.state === 'waiting for approval')
  const p = protection.data
  const backup = !hasProtection || protection.denied ? ''
    : !p || protection.refreshFailed || p.unknown || p.reading?.unknown || p.reading?.stale ? 'backup unknown'
    : p.items.some(i => i.copy === 'none') ? 'no backup yet'
    : p.items.some(i => i.copyLabel === 'overdue') ? 'backup overdue'
    : p.items.some(i => i.copyLabel === 'unknown') ? 'backup unknown' : ''
  const share = resources.data?.share
  const warning = e.readiness === 'uncertain' ? 'uncertain'
    : share?.diskRefuses ? 'disk full' : share?.lowDisk ? 'low disk'
    : resources.refreshFailed || resources.data?.reading?.unknown || resources.data?.reading?.stale ? 'resources unknown' : ''
  return <div className="deployment-release-cell">
    <div>{e.kind === 'preview'
      ? e.changeId ? <Link to={`/changes/${e.changeId}`}>Preview of {change.data ? `change #${change.data.change.number}: ${change.data.change.title}` : 'change'}</Link> : 'Change not recorded'
      : <ServingRelease slug={slug} env={e} />}{e.kind === 'preview' && <StaleNote q={change} inline />}</div>
    {e.kind === 'standing' && <div className="muted small">{feedWords(e)} · {e.approvalRole ? `approval from ${e.approvalRole}` : 'no approval'}</div>}
    {waitingRelease && <div className="deploy-warning small">Release {waitingRelease.number} is waiting for approval.</div>}
    <FeedPause env={e} />
    {e.promotionBlocked && <div className="muted small">{e.promotionBlocked}</div>}
    {(backup || warning) && <div>{backup && <span className={backup === 'no backup yet' ? 'error small' : 'deploy-warning small'}>{backup} </span>}{warning && <span className={share?.diskRefuses ? 'error small' : 'deploy-warning small'}>{warning}</span>}</div>}
  </div>
}

const DEPLOYMENT_TABS: { key: DeploymentTab; label: string }[] = [{ key: 'status', label: 'Status' }, { key: 'history', label: 'History' }, { key: 'settings', label: 'Settings' }]

// DeploymentSettings is what a person sets on a deployment: where its
// releases come from and who approves them, its limits, and its name.
export function DeploymentSettings({ slug, env: e, onChanged }: { slug: string; env: DeploymentView; onChanged: () => void }) {
  return <>
    <DeploymentFeed key={`${slug}/${e.name}/feed`} slug={slug} name={e.name} />
    <div className="card deploy-entry"><div className="deploy-lines">
      {e.generation ? <ResourceLines slug={slug} name={e.name} /> : null}
      {e.target?.kind === 'portainer' && e.id && <DeploymentLine label="Address"><DeploymentAddressEdit slug={slug} id={e.id} /></DeploymentLine>}
      <DeploymentLine label="Name"><div className="deployment-name-line"><strong>{e.displayLabel || e.name}</strong><DisplayLabelEdit value={e.displayLabel || e.name} machineKey={e.name} path={`/projects/${slug}/deployments/${e.id}/label`} onChanged={onChanged} /></div></DeploymentLine>
    </div></div>
  </>
}

export function DeploymentPage() {
  const { slug, name } = useParams()
  const asked = new URLSearchParams(useLocation().search).get('tab')
  const tab = DEPLOYMENT_TABS.find(t => t.key === asked)?.key ?? 'status'
  const q = useGet<DeploymentView[]>(`/projects/${slug}/deployments`, { poll: 5000 })
  const e = q.data?.find(e => e.name === name)
  const label = e ? deploymentName(e) : 'Deployment'
  useLayoutEffect(() => {
    setPageContext({ names: { [`/projects/${slug}/deployments/${name}`]: label } })
    return () => setPageContext({})
  }, [slug, name, label])
  return <><ReadStatus q={q} what="the deployment" /><StaleNote q={q} />
    {e && <DeploymentDeploys key={`${slug}/${name}`} slug={slug!} name={name!} displayLabel={e ? deploymentName(e) : undefined} url={e?.url} env={q.refreshFailed ? failedRuntimeRefresh(e, q.reason) : e} current={q.current} reload={q.reload}
      facts={<DeploymentFacts slug={slug!} env={e} />}
      tab={tab} tabs={<Tabs className="tabs deployment-tabs" role="navigation" aria-label="about this deployment">
        {DEPLOYMENT_TABS.map(t => <Link key={t.key} to={t.key === 'status' ? '?' : `?tab=${t.key}`} className={'tab' + (t.key === tab ? ' active' : '')} aria-current={t.key === tab ? 'page' : undefined}>{t.label}</Link>)}
      </Tabs>}
      body={e.generation ? <DeploymentContainers slug={slug!} name={name!} stopped={e.runtimeState === 'stopped'} /> : undefined}
      below={<EnvCard slug={slug!} env={q.refreshFailed ? failedRuntimeRefresh(e, q.reason) : e} onChanged={q.reload} />}
      settings={<DeploymentSettings slug={slug!} env={e} onChanged={q.reload} />} />}
  </>
}

// DeploymentFacts is the line under a deployment's name: where it answers,
// the release it runs, and whether it is protected or its automatic deploys
// are paused.
export function DeploymentFacts({ slug, env: e }: { slug: string; env: DeploymentView }) {
  return <div className="deployment-page-facts muted">
    <span className="deployment-address"><DeploymentFact env={e} /></span>{e.kind === 'standing' && <> · <ServingRelease slug={slug} env={e} /></>}
    {e.protected && <> <span className="badge" title="reaches partners or the public, or holds production data · deploys need the release grant">protected</span></>}
    <FeedPause env={e} />
  </div>
}

export function EnvCard({ slug, env: e }: { slug: string; env: DeploymentView; onChanged?: () => void }) {
  const [technical] = useTechnicalDetail()
  const nav = useNavigate()
  const { run, busy, err } = useAction()
  const deploy = async () => {
    const r = await run(() => post<ReleaseRequested>(`/projects/${slug}/deployments/${e.name}/deploy`, e.feed === 'promotion' ? { expectedSourceBundleId: e.promotionBundleId } : {}))
    if (r) nav(`/projects/${slug}/releases/${r.number}`)
  }
  const base = `/projects/${slug}/deployments/${e.name}`
  const resources = useGet<DeploymentResources>(e.generation ? `${base}/resources` : null, { poll: 5000 })
  const diagnostics = useGet<DeploymentDiagnostics>(e.generation ? `${base}/diagnostics` : null, { poll: 5000 })
  const protection = useGet<DeploymentProtection>(e.target?.kind === 'do-app' || e.target?.kind === 'docker-host' ? `${base}/protection` : null, { poll: 5000 })
  const models = useGet<DeploymentAIModels>(e.aiModels?.length ? `${base}/ai-models` : null, { poll: 5000 })
  const waiting = [resources.data?.reading, diagnostics.data?.reading, protection.data?.reading, ...(models.data?.keys.map(k => k.reading) ?? [])].some(r => r?.waiting)
  const place = e.target ? placeWords(e.target.title, e.target.vendor, e.target.region) : 'not bound'
  const meta = [
    `${place}${e.target ? ` (${e.target.displayLabel || e.target.name})` : ''}`,
    feedWords(e),
    e.audience === 'team' ? 'team only' : e.audience,
    `${e.data === 'synthetic' ? 'test' : e.data} data`,
  ].filter(Boolean).join(' · ')
  // Follows main deploys on landing; other feeds can release here.
  const action = e.feed === 'main'
    ? null
    : e.target
    ? <div className="stack stack-1"><button title={promotionHelp(e)} disabled={busy || !!e.promotionBlocked} onClick={deploy}>{deployLabel(e)}</button>{e.promotionBlocked && <span className="muted small">{e.promotionBlocked}</span>}</div>
    : null

  return (
    <div className="card deploy-entry">
      <div className="deploy-lines">
        <DeploymentLine label="Runs on" trailer={e.target && <Link to={`/targets/${encodeURIComponent(e.target.name)}/resources?deployment=${encodeURIComponent(`${slug}/${e.name}`)}`}>resources →</Link>}>
          {meta}{' '}<TechnicalWords shown={technical}>{e.generation ? `generation ${e.generation}` : ''}</TechnicalWords>
        {waiting && e.lastApplied && <div className="muted">Checking after the release (since {fmtWhen(e.lastApplied.at)})</div>}
        {e.needs && e.needs.length > 0 && (
          <div>
            <span className="muted">backed by</span>{' '}
            {e.needs.map((n) => (
              <span key={n.need} title={n.id ? `the provider's id: ${n.id}` : undefined} className="deployments-span">
                {n.need} <b>{n.name}</b>
                <span className="muted"> {[n.region, n.version && (n.need === 'postgres' ? `pg ${n.version}` : n.version), n.size].filter(Boolean).join(' · ')}</span>
                {n.status !== 'ready' && <Status word={n.statusLabel} help={`Service: ${n.status}.`} />}
              </span>
            ))}
            <Link to="/targets">see the infrastructure</Link>
          </div>
        )}
        </DeploymentLine>
        {e.generation && e.target && <HostLine share={resources.data?.share} target={e.target.name} old={resources.data?.reading?.waiting} />}
        {e.aiModels && e.aiModels.length > 0 && <AIModelsLine slug={slug} name={e.name} />}
        {(e.target?.kind === 'do-app' || e.target?.kind === 'docker-host') && <ProtectionLines slug={slug} name={e.name} />}
        {e.restoreCheck && <DeploymentLine label="After restore"><Status word={e.restoreLabel} /> {restoreCheckWords(e.restoreCheck)}</DeploymentLine>}
        {e.dataAfterRestore && (
          <DeploymentLine label="Data after restore">
            {e.dataAfterRestore.state === 'waiting' && <>Its data from {PRODUCT}'s backup goes back in when it is rebuilt, before the app starts.</>}
            {e.dataAfterRestore.state === 'filled' && <>Its data came back from {PRODUCT}'s backup{e.dataAfterRestore.set ? <> (set {e.dataAfterRestore.set})</> : null} before the app started.</>}
            {e.dataAfterRestore.state === 'failed' && (
              <span className="error">
                Its data did not come back from {PRODUCT}'s backup: {e.dataAfterRestore.detail}.
                {e.dataAfterRestore.set ? <> Once it runs, restore set {e.dataAfterRestore.set} from its backups.</> : null}
              </span>
            )}
          </DeploymentLine>
        )}
        {e.readiness && e.readiness !== 'reconciled' && e.readiness !== 'uncertain' && <DeploymentLine label="Readiness"><Status word={e.readinessLabel} /><TechnicalWords shown={technical}>Deployment: {e.readiness}.</TechnicalWords></DeploymentLine>}
        {e.readiness === 'uncertain' && (
          <DeploymentLine label="Readiness">
            <Status word={e.readinessLabel} />{' '}
            {PRODUCT} does not know whether this deployment stands: its last attempt was cut off, or its
            records disagree. Refresh status asks its host and settles what it finds; it deploys nothing.
          </DeploymentLine>
        )}
        {(action || err) && <div className="row deploy-more">
          {action}
          {err && <span className="error small">{err}</span>}
        </div>}
      </div>
    </div>
  )
}

// The address stays available when resource warnings need attention.
function DeploymentFact({ env }: { env: DeploymentView }) {
  return <span className="muted">{env.url ? <AppAddress url={env.url} hostOnly={env.hostOnly} /> : 'no address yet'}</span>
}

function AddDeployment({ slug, choices, existing, onDone }: { slug: string; choices: OfferedTarget[]; existing: DeploymentView[]; onDone: (added: boolean) => void }) {
  const [name, setName] = useState('')
  const [target, setTarget] = useState('')
  const [feed, setFeed] = useState('manual')
  const [source, setSource] = useState('')
  const [audience, setAudience] = useState<string>('team')
  const [data, setData] = useState<string>('non-production')
  const [startFrom, setStartFrom] = useState('')
  const finalsQ = useGet<FinalCopies>(`/projects/${slug}/final-copies`)
  const { run, busy, err, setErr } = useAction()
  const picked = choices.find((t) => t.name === target) ?? choices[0]
  useEffect(() => { if (!target && choices[0]) setTarget(choices[0].name) }, [choices, target])
  const add = async () => {
    if (!name.trim()) { setErr('give it a name · lower-case letters, digits and dashes'); return }
    if (existing.some(e => e.name === name.trim())) { setErr(`there is already a deployment called ${name.trim()}`); return }
    const from = picked?.kind === 'do-app' ? startFrom : ''
    const r = await run(() => post(`/projects/${slug}/deployments`, { name: name.trim(), target: picked?.name, feed, ...(feed === 'promotion' ? { from: source } : {}), audience, data, ...(from ? { startFrom: from } : {}) }))
    if (r) { setName(''); onDone(true) }
  }
  const protectedPick = audience === 'partners' || audience === 'public' || data === 'production'
  return (
    <div className="card deploy-new">
      <strong>New deployment</strong>
      <div className="form-grid">
        <label>Name</label>
        <input aria-label="Deployment name" value={name} onChange={(e) => setName(e.target.value)} placeholder="staging, cloud, demo…" className="deployments-add-deployment-input" />
        <label>Deployment target</label>
        <div>
          <select aria-label="Target" value={picked?.name ?? ''} onChange={(e) => setTarget(e.target.value)} disabled={choices.length === 0}>
            {choices.map((t) => <option key={t.name} value={t.name}>{t.displayLabel || t.name} — {placeOf(t.kindInfo, t.address)}</option>)}
          </select>
          {picked?.offer && (
            <div className="muted small mt-1" >
              offered {picked.offer.implicit ? 'to everyone (built in)' : `to ${picked.offer.group === '*' ? 'everyone' : picked.offer.group}`}
              {capsWords(picked.offer.caps) ? ` · ${capsWords(picked.offer.caps)}` : ''}
              {picked.bound > 0 ? ` · ${picked.bound} of this project's deployments already here` : ''}
              {picked.capabilities.needs?.length ? ` · provides ${picked.capabilities.needs.join(', ')}` : ''}
            </div>
          )}
        </div>
        <label>Release feed</label>
        <select aria-label="Release feed" value={feed} onChange={e => setFeed(e.target.value)}>
          <option value="manual">By hand</option><option value="main">Follows main</option><option value="promotion">Promoted from a deployment</option>
        </select>
        {feed === 'promotion' && <><label>Source deployment</label><select aria-label="Source deployment" value={source} onChange={e => setSource(e.target.value)}>
          <option value="">Choose a source</option>{existing.filter(e => e.kind === 'standing').map(e => <option key={e.id} value={e.name}>{e.displayLabel || e.name}</option>)}
        </select></>}
        <label title="who can reach it">Audience</label>
        <select aria-label="Audience" value={audience} onChange={(e) => setAudience(e.target.value)}>
          {AUDIENCES.map((x) => <option key={x} value={x}>{x}</option>)}
        </select>
        <label title="what kind of data it holds">Data</label>
        <select aria-label="Data" value={data} onChange={(e) => setData(e.target.value)}>
          {DATA_CLASSES.map((x) => <option key={x} value={x}>{x}</option>)}
        </select>
        {picked?.kind === 'do-app' && (finalsQ.data?.copies.length ?? 0) > 0 && <>
          <label title="its first release fills its database from this copy, and checks it, before the app starts">Start from</label>
          <div>
            <select aria-label="Start from" value={startFrom} onChange={(e) => setStartFrom(e.target.value)}>
              <option value="">empty</option>
              {finalsQ.data!.copies.map((c) => (
                <option key={c.backup} value={c.backup ?? ''}>
                  the final copy of {c.deployment} (taken {fmtWhen(c.takenAt)}, {c.verified ? 'checked' : 'not checked'}, {fmtBytes(c.bytes)})
                </option>
              ))}
            </select>
            {startFrom !== '' && (
              <div className="muted small mt-1" >
                Putting a copy's data in needs the restore grant here and where it came from. A copy of production data goes only into a deployment that holds production data.
              </div>
            )}
          </div>
        </>}
      </div>
      {protectedPick && (
        <div className="muted small mt-2" >
          This makes the deployment <b>protected</b>: deploying to it takes the release grant, and its target and protection cannot be changed once something runs there.
        </div>
      )}
      <div className="row deployments-add-deployment-div">
        <button className="primary" disabled={busy || !picked || (feed === 'promotion' && !source)} onClick={add}>Create</button>
        <button className="subtle" disabled={busy} onClick={() => { setErr(''); onDone(false) }}>Cancel</button>
        {/* What the disabled Create waits for. */}
        {!busy && (!picked ? choices.length > 0 && <span className="muted small">Choose a deployment target</span> : feed === 'promotion' && !source && <span className="muted small">Choose the deployment to promote from</span>)}
        {err && <span className="error small">{err}</span>}
      </div>
    </div>
  )
}

// RetainedTable lists what take-downs left at providers: open ones first,
// settled ones faded.
export function RetainedTable({ rows }: { rows: Retained[] }) {
  const faded = (r: Retained) => r.disposition !== 'retained' ? 'retained-settled' : undefined
  return <ListTable label="Old resources" className="deployments-deployments-tab-table" rows={rows} rowKey={r => r.id ?? `${r.deployment}/${r.need}/${r.retainedAt}`} columns={[
    { key: 'what', label: 'What', phone: 'name', render: r => {
      const name = String(r.handle?.name ?? (r.handle?.requested as { name?: string } | undefined)?.name ?? '')
      const id = r.handle?.id ? String(r.handle.id) : ''
      // A size in words the driver wrote (an image repository's), or "unknown (why)".
      const size = typeof r.handle?.size === 'string' && r.handle?.kind === 'repository' ? r.handle.size : ''
      return <span className={faded(r)}>
        <b>{r.need}</b> {name}{id && <span className="muted small"> ({id})</span>}
        {size && <div className="muted small">{size}</div>}
        {typeof r.handle?.why === 'string' && <div className="muted small">{r.handle.why}</div>}
        {r.target && <div className="muted small">on {r.target}</div>}
      </span>
    } },
    { key: 'from', label: 'From', phone: 'extra', render: r => <span className={faded(r)}>{r.deployment}</span> },
    { key: 'torn', label: 'Torn down', phone: 'time', render: r => <span className={`muted small deployments-retained-row-td ${faded(r) ?? ''}`}>{fmtWhen(r.retainedAt)} by {r.retainedBy}</span> },
    { key: 'status', label: 'Status', phone: 'state', render: r => <span className={faded(r)}>{r.disposition === 'retained'
      ? <span className="badge">still at the provider</span>
      : <span className="badge">{r.disposition === 'released' ? 'deleted at the provider' : 'kept on purpose'}{r.settledBy ? ` · ${r.settledBy}` : ''}{r.note ? `: ${r.note}` : ''}</span>}</span> },
    { key: 'review', label: '', phone: 'action', render: r => r.disposition === 'retained' && r.target && <Link to={`/targets/${r.target}`}>Review on target →</Link> },
  ]} />
}

// What a take-down will do, said before anyone confirms it.

const dollars = (n: number) => `$${n.toFixed(2)}`
const periodWords: Record<string, string> = { daily: 'today', weekly: 'this week', monthly: 'this month' }

// allowanceWords says what an AI models key may still spend, over the same
// period as its allowance, with lifetime spend apart: a key that has spent
// $12 in all may still have $4 of its $5 this month.
export function allowanceWords(k: AIModelsKey): string {
  const a = k.allowance
  if (!a) return `allowance unknown${k.unknown ? ` · ${k.unknown}` : ''}`
  const now = a.reset ? periodWords[a.reset] ?? `this ${a.reset} period` : ''
  const left = a.remaining == null ? 'no limit'
    : `${dollars(a.remaining)}${a.limit == null ? '' : ` of ${dollars(a.limit)}`} left${now ? ` ${now}` : ', never renewed'}`
  return `${left} · ${dollars(a.lifetimeSpend)} spent in all${a.disabled ? ' · disabled' : ''}`
}

// AIModelsLine is what OpenRouter says now of the deployment's AI models
// keys, read when the card shows. Unknown is said, never shown as zero.
function AIModelsLine({ slug, name }: { slug: string; name: string }) {
  const q = useGet<DeploymentAIModels>(`/projects/${slug}/deployments/${name}/ai-models`)
  if (q.denied) return null
  return (
    <DeploymentLine label="AI models"><div>
      {q.loading ? <span className="muted">reading the allowance…</span>
        : !q.data ? <span className="error">allowance unknown · {q.err || 'no answer'}</span>
        : q.data.keys.map((k) => (
          <span key={k.name} title={`OpenRouter key ${k.name}`} className="deployments-span">
            {k.allowance ? allowanceWords(k) : <span className="muted">{allowanceWords(k)}</span>} {k.reading?.stale && <span className="muted">(old)</span>} {k.reading?.failure}
          </span>
        ))}
      <StaleNote q={q} inline />
    </div></DeploymentLine>
  )
}

// howWords says how a deployment's profile was chosen.
const howWords: Record<string, string> = { chosen: 'chosen', preview: 'the preview profile', default: 'the default' }

// ResourceLines is what the deployment's running generation applied of its
// profile, part by part, and its operators' overrides; what each part uses
// now is in the containers table. A limit nobody declared reads "no limit"; a generation from
// before AppTown kept the record reads "not recorded"; memory use the
// target does not report is unknown with its reason, never zero.
export function ResourceLines({ slug, name }: { slug: string; name: string }) {
  const q = useGet<DeploymentResources>(`/projects/${slug}/deployments/${name}/resources`)
  // The deployment's one profile, for app.yaml and secretspec.toml alike,
  // and every name it may be given: chosen here, applied at the next deploy.
  const choice = useGet<ProjectSecrets>(`/projects/${slug}/secrets`)
  const { run, busy, err } = useAction()
  const [open, setOpen] = useState(false)
  const [part, setPart] = useState('')
  const [memory, setMemory] = useState('')
  const [reason, setReason] = useState('')
  const [said, setSaid] = useState('')
  if (q.denied) return null
  if (q.loading) return <DeploymentLine label="Limits"><span className="muted">reading its resources…</span></DeploymentLine>
  if (!q.data) return <DeploymentLine label="Limits"><span className="error">resources unknown · {q.err || 'no answer'}</span></DeploymentLine>
  const r = q.data
  const parts = r.parts.filter((p) => p.part && !p.suppliedService)
  const picked = parts.find((p) => p.part === part)?.part || parts[0]?.part
  const done = (o: OverrideResult | undefined) => {
    if (!o) return
    setSaid(o.release ? `redeploying it as release ${o.release.number}` : o.notYet || '')
    setOpen(false); setMemory(''); setReason('')
    q.reload()
  }
  const mine = choice.data?.deployments.find((d) => d.name === name)
  const choose = async (profile: string) => {
    const done = await run(() => put(`/projects/${slug}/secrets/${name}`, { profile }))
    if (done) { choice.reload(); q.reload() }
  }
  const set = async () => {
    if (!picked) return
    done(await run(() => put<OverrideResult>(`/projects/${slug}/deployments/${name}/resources`, { part: picked, memory, reason })))
  }
  const clear = async (p: string) => done(await run(() => del<OverrideResult>(`/projects/${slug}/deployments/${name}/resources`, { part: p, reason: 'cleared by hand' })))
  type Row = DeploymentResources['parts'][number]
  const role = (p: Row) => p.suppliedService ? 'workflow service' : p.need ? 'service' : p.public && p.part ? 'front door' : ''
  const source = (p: Row) => p.memoryDefault ? `${p.memoryDefault.kind === 'catalogue' ? `catalogue ${p.memoryDefault.name}` : (p.memoryDefault.kind === 'apptown' || p.memoryDefault.kind === 'forge') ? PRODUCT : p.memoryDefault.kind} default` : ''
  const columns: ListColumn<Row>[] = [
    { key: 'part', label: 'Part', phone: 'name', render: p => <><strong>{p.part || 'app'}</strong>{role(p) && <span className="muted small"> {role(p)}</span>}</> },
    { key: 'memory', label: 'Memory', phone: 'state', render: p => <>{p.memory}{p.memoryUnlimited && <> <span className="badge">explicit no limit</span></>}{source(p) && <div className="muted small">{source(p)}</div>}</> },
    { key: 'cpu', label: 'CPU', phone: 'detail', render: p => p.cpu },
    { key: 'runs', label: 'Runs on', phone: 'extra', render: p => <span className="muted">{[p.size && `size ${p.size}`, p.runsOn].filter(Boolean).join(' · ') || '—'}</span> },
    { key: 'override', label: 'Override', phone: 'time', render: p => p.override
      ? <span><Status word={p.overrideLabel} help={`${p.override.reason} · ${p.override.setBy}, ${p.override.setAt}`} />{' '}{p.override.memoryUnlimited ? 'no memory limit' : p.override.resources.memoryBytes ? fmtBytes(p.override.resources.memoryBytes) : ''}{p.override.resources.cpus ? ` ${p.override.resources.cpus} cpu` : ''}{p.overrideApplied ? '' : ' · not applied yet'}{' '}
        {!p.suppliedService && <button className="link small" disabled={busy || r.reading?.unknown} onClick={() => clear(p.part)}>clear</button>}</span>
      : <span className="muted">—</span> },
  ]
  return (
    <DeploymentLine label="Limits"><div className="deployment-limits">
      {r.reading?.stale && <div className="muted">Last values (old)</div>}
      {r.reading?.failure && <div className="muted">{r.reading.failure}</div>}
      <div className="deployment-limits-profile">
        <span className="muted">Profile</span>{' '}
        {r.recorded ? <><b>{r.profile}</b> <span className="muted">({howWords[r.how || ''] || r.how})</span></> : <span className="muted">not recorded</span>}
        {mine && choice.data && choice.data.choosable.length > 1 && (
          <select aria-label="Profile" value={mine.chosen} disabled={busy || r.reading?.unknown} onChange={(ev) => choose(ev.target.value)}
            title="the profile this deployment uses from its next deploy, for app.yaml and secretspec.toml alike" className="deployments-resource-lines-select">
            {choice.data.choosable.map((n) => <option key={n} value={n}>{n}</option>)}
          </select>
        )}
        {mine && r.recorded && mine.chosen !== r.profile && <span className="badge deployments-resource-lines-badge" title="takes effect at the next deploy">next: {mine.chosen}</span>}
      </div>
      <ListTable label="Limits" className="deployment-limits-table" columns={columns} rows={r.parts} rowKey={p => p.part || 'app'} />
      {r.parts.some((p) => p.suppliedService) && <div className="muted small">Workflow service limits are set in the workflow service declaration.</div>}
      {parts.length > 0 && !open && !r.reading?.unknown && <div><button onClick={() => setOpen(true)}>Override memory…</button></div>}
      {open && picked && (
        <div className="row deployments-resource-lines-div">
          {parts.length > 1 && (
            <select aria-label="App part" value={picked} onChange={(ev) => setPart(ev.target.value)}>
              {parts.map((p) => <option key={p.part} value={p.part}>{p.part}</option>)}
            </select>
          )}
          <input aria-label="Memory limit" placeholder="memory, e.g. 2Gi or none" value={memory} onChange={(ev) => setMemory(ev.target.value)} className="deployments-resource-lines-input" />
          <input aria-label="Reason for override" placeholder="why" value={reason} onChange={(ev) => setReason(ev.target.value)} className="deployments-resource-lines-input-2" />
          <button className="primary" disabled={busy || r.reading?.unknown || !memory || !reason.trim()} onClick={set} title="records who and why, then redeploys what runs so it applies now">Override and redeploy</button>
          <button className="link small" onClick={() => setOpen(false)}>cancel</button>
        </div>
      )}
      {said && <div className="muted">{said}</div>}
      {err && <div className="error">{err}</div>}
      <StaleNote q={q} inline />
    </div></DeploymentLine>
  )
}

// limitKindWords is what each kind of limit event says happened.
const limitKindWords: Record<string, string> = {
  'out-of-memory': 'killed: out of memory',
  exited: 'exited',
}

// timesWords: "once", "twice", "3 times".
export function timesWords(n: number): string {
  return n === 1 ? 'once' : n === 2 ? 'twice' : `${n} times`
}

// LimitEventLines says what happened to the deployment's parts at their
// limits over the last day: each kind with how often and when last. Where
// the target reports none, it says so, so an empty list is never read as
// "nothing happened".
export function LimitEventLines({ r }: { r: DeploymentResources }) {
  const [technical] = useTechnicalDetail()
  if (!r.limitsKnown) return <div className="muted">Limit events were not reported. <TechnicalWords shown={technical}>{r.limitsSource}</TechnicalWords></div>
  if (r.limits.length === 0) return <div className="muted" title={r.limitsSource}>no out-of-memory kills or exits recorded in the last day</div>
  return (
    <div title={r.limitsSource}>
      {r.limits.map((l) => (
        <div key={`${l.part}/${l.kind}`}>
          <b>{l.part || 'app'}</b>: {limitKindWords[l.kind] || l.kind}, {timesWords(l.count)} in the last day <span className="muted">(last {fmtWhen(l.last)})</span>
        </div>
      ))}
    </div>
  )
}

// HostLine is always present, so the page keeps its shape: whether the host
// takes new deploys, and its memory against what its apps ask for. A host
// check that failed is the host's own problem, shown on its page; here the
// facts it could not read are unknown.
export function HostLine({ share, target, old }: { share?: DeploymentResources['share']; target: string; old?: boolean }) {
  const [technical] = useTechnicalDetail()
  const link = <Link to={`/targets/${encodeURIComponent(target)}`}>host →</Link>
  if (share?.elastic) return <DeploymentLine label="Host" trailer={link}>{target} · grows on demand</DeploymentLine>
  const current = !!share?.readAt && !share.error && !share.stale && !old
  const deploys = !current ? <span className="muted">unknown</span>
    : share!.diskRefuses ? <span className="error">new deploys blocked: low on disk</span>
    : share!.lowDisk ? <span className="deploy-warning">ready for deploys · low on disk</span>
    : <>ready for deploys</>
  const memory = current && share!.requested?.known && share!.hostMemory.known
    ? <span className={share!.overcommitted ? 'deploy-warning' : undefined}>memory {fmtBytes(share!.requested.value)} requested of {fmtBytes(share!.hostMemory.value)}</span>
    : null
  return <DeploymentLine label="Host" trailer={link}>
    {target} · {deploys}{memory && <> · {memory}</>}{' '}
    {share?.readAt && <TechnicalWords shown={technical}>read {fmtStamp(share.readAt)}</TechnicalWords>}
  </DeploymentLine>
}

// What holds a deployment after AppTown was restored, as a chip and as a
// sentence.
function restoreCheckWords(check: string): string {
  switch (check) {
    case 'unconfirmed':
      return `${PRODUCT} was restored from a backup and has not yet read what this deployment's provider runs; nothing acts on it until it has.`
    case 'gone':
      return `${PRODUCT} was restored from a backup and the provider no longer has this app. Take it down, or release it again once ${PRODUCT} can rebuild it.`
    case 'not-filled':
      return `${PRODUCT} was restored from a backup and this deployment's data did not come back, so it is not started on empty data by itself. Release it to start it without that data, then restore its data from its backups.`
    default:
      return `${PRODUCT} was restored from a backup; this deployment is rebuilt with its data before anything else runs it.`
  }
}
