import { useState, type ReactNode } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { post } from '../../api'
import { useAction, useGet, type Read } from '../../data'
import { FeatureOnly } from '../../features'
import { useHolds } from '../../instancePages'
import { ListRow } from '../../ListRow'
import { PageBar } from '../../PageBar'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Status } from '../../Status'
import { fmtBytes, fmtStamp } from '../../format'
import { PRODUCT } from '../../brand'
import { useNow } from '../../hooks'
import { deploymentName } from '../../deploymentName'
import { TechnicalWords, useTechnicalDetail } from '../change/TechnicalDetail'
import { backupVerdict, caveatNote, copyDoor, DISK, leadNote, itemBadge, latestEnded, liveFilesLimit, managedCopy, takenDownWords } from '../Backups'
import { copyLine, currentStep, drillLine, heldBack, itemLabel, kindWords, nativeLine, rehearsalLine, rehearseTitle, RestoredCopyLines, restoreTestLine, restoreTestTitle, stateWords, writesStopped } from '../DataProtection'
import { useBackUp } from './DeploymentMenu'
import { DeploymentLine } from './settings/DeploymentLine'
import type { AllBackups, BackupItem, BackupStoreState, DeploymentBackups, DeploymentProtection, DeploymentView, FinalCopies, ItemProtection, Operation, OperationAdmitted, ProjectBackup } from '../../wire.gen'
import '../../overview-line.css'
import './settings/deployments.css'
import './deployment-status.css'
import '../../parts/runActivity.css'

// DeploymentBackupsPage is one deployment's backups, one tap from its
// Status tab and from its row on Backups: what is wrong and the act that
// fixes it, any operation under way or that went wrong, the last fourteen
// nights at a glance, every copy newest first with Restore… on each, and
// beside them what each item's copies hold and miss, where they are kept and
// whether a restore has been tested. A deployment that was taken down keeps
// this page for its history, read-only.
export function DeploymentBackupsPage() {
  const { slug = '', name = '' } = useParams()
  const q = useGet<DeploymentView[]>(`/projects/${slug}/deployments`, { poll: 5000 })
  const env = q.data?.find(e => e.name === name)
  const sets = useGet<DeploymentBackups>(`/projects/${slug}/deployments/${encodeURIComponent(name)}/backups`, { poll: 30000 })
  const label = env ? deploymentName(env) : sets.data?.backups[0]?.deploymentLabel || name
  return <>
    <PageBar title={<>Backups <span className="title-context">{label}</span></>} actions={env ? <BarBackUp slug={slug} env={env} /> : undefined} />
    <StaleNote q={[q, sets]} />
    {!q.data ? <ReadStatus q={q} what="the deployment" />
      : env ? <StandingBackups slug={slug} env={env} setsRead={sets} />
      : !sets.data ? <ReadStatus q={sets} what="its backups" />
      : sets.data.backups.length ? <TakenDownBackups slug={slug} name={name} sets={sets.data.backups} />
      : <p className="muted">No deployment named {name}, and no backups of one.</p>}
  </>
}

const protectionPath = (slug: string, name: string) => `/projects/${slug}/deployments/${encodeURIComponent(name)}/protection`

// BarBackUp is Back up now in the bar while the deployment's backups are
// fine; in trouble, the lead row carries it instead.
export function BarBackUp({ slug, env }: { slug: string; env: DeploymentView }) {
  const p = useGet<DeploymentProtection>(env.backups && env.generation ? protectionPath(slug, env.name) : null, { poll: 5000 })
  const backUp = useBackUp(slug, env)
  if (!backUp.backUp || !p.data || !holdsData(p.data) || backupVerdict(p.data, p.refreshFailed).backUp) return null
  const door = copyDoor(p.data)
  return <div className="row row-2">
    <button disabled={backUp.busy || !env.generation || !!p.data.running || door.disabled} title={door.why} onClick={backUp.backUp}>Back up now</button>
    {backUp.err && <span className="error small" role="alert">{backUp.err}</span>}
  </div>
}

function StandingBackups({ slug, env, setsRead }: { slug: string; env: DeploymentView; setsRead: Read<DeploymentBackups> }) {
  const sets = setsRead.data?.backups, note = setsRead.data?.note
  const name = env.name
  const q = useGet<DeploymentProtection>(env.backups && env.generation ? protectionPath(slug, name) : null, { poll: 5000 })
  const all = useGet<AllBackups>('/backups', { poll: 30000 })
  const store = useGet<BackupStoreState>('/backup-store')
  const deploy = useHolds('deployment:deploy', slug)
  const backUp = useBackUp(slug, env)
  useNow(1000, !!q.data?.running)
  const p = q.data
  const own = sets ?? []
  const newestSet = own.find(holds)
  const newest = newestSet ? { backup: newestSet.id, takenAt: newestSet.takenAt, bytes: newestSet.bytes, sha256: '' } : null
  const v = p ? backupVerdict(p, q.refreshFailed || setsRead.refreshFailed, newest, !!store.data?.store, false) : null
  const inPlace = env.target?.kind === 'docker-host' || env.target?.kind === 'portainer'
  const restoreOn = (b: ProjectBackup) => deploy && inPlace && holds(b) && !managedCopy(b) && !!p && !p.running
  const siblings = (all.data?.backups ?? []).filter(b => b.project === slug && b.deployment !== name && b.trigger === 'take-down' && holds(b) && !managedCopy(b))
  const ended = p ? latestEnded(p) : undefined
  return <div className="deployment-status">
    <div className="section-body deployment-status-rows">
      <ReadStatus q={q} what="how its data is protected" className="deployment-status-read" />
      <ReadStatus q={setsRead} what="its copies" className="deployment-status-read" />
      <ReadStatus q={all} what="the project's kept final copies" className="deployment-status-read" />
      <StaleNote q={[all, store]} />
      {p && v?.lead && <ListRow className="card row run-list-row" state={<span className="row-status">{v.state}</span>}
        identity={<><strong>{newest ? `Newest copy ${fmtStamp(newest.takenAt)}` : 'No copy yet'}</strong><span className="muted small">{v.why}</span></>}
        action={v.backUp && backUp.backUp ? <button disabled={backUp.busy || !!p.running || copyDoor(p).disabled} title={copyDoor(p).why} onClick={backUp.backUp}>Back up now</button> : undefined}
        detail={backUp.err ? <span className="error" role="alert">{backUp.err}</span> : undefined} />}
      {p && !holdsData(p) && !p.unknown && own.length === 0 && <ListRow className="card row run-list-row"
        identity={<><strong>Nothing to back up</strong>{note && <span className="muted small">{note}</span>}</>} />}
      {p?.running && <OperationRow op={p.running} />}
      {ended && (ended.kind === 'deployment.restore' || ended.state === 'failed' || ended.state === 'unknown') && <OperationRow op={ended} />}
      {p?.restoredCopies.filter(c => !c.endedAt).map(c => <div key={c.id} className="card list-row run-list-row"><RestoredCopyLines slug={slug} source={name} copy={c} asRead="" /></div>)}
      <Copies slug={slug} name={name} sets={own} where={all.data?.where ?? ''} restoreOn={restoreOn} extra={siblings} />
    </div>
    <div className="stack stack-3 deployment-status-facts">
      <div className="section-body"><div className="card deploy-entry"><div className="deploy-lines">
        {env.backups && env.generation ? <ProtectionCard slug={slug} name={name} newest={newestSet} bucket={store.data?.store?.bucket} /> : <DeploymentLine label="Covers"><span className="muted">nothing yet: it has not been deployed</span></DeploymentLine>}
      </div></div></div>
    </div>
  </div>
}

// OperationRow is an operation on the deployment's data: under way, or the
// last one when it was a restore or did not finish well. A good backup
// state never hides a restore that ended unknown.
function OperationRow({ op }: { op: Operation }) {
  const running = op.state === 'queued' || op.state === 'running'
  const restored = op.kind === 'deployment.restore' && op.state === 'succeeded'
  return <ListRow className="card row run-list-row" to={`/operations/${op.id}`}
    state={<span className="row-status"><Status word={op.statusLabel} reason={restored ? 'restored' : undefined} /></span>}
    identity={<><strong><Link className="run-link" to={`/operations/${op.id}`}>{kindWords[op.kind] ?? op.kind}{!running && op.finishedAt ? ` ${fmtStamp(op.finishedAt)}` : ''}</Link></strong>
      <span className="muted small">{running ? currentStep(op) : stateWords(op)} · by {op.askedBy}{writesStopped(op) && ' · writes stopped'}</span></>} />
}

// TakenDownBackups is a taken-down deployment's history: what its newest
// retained set is, its copies read-only, and the way to start a new
// deployment from its final copy when the owner offers that copy.
function TakenDownBackups({ slug, name, sets }: { slug: string; name: string; sets: ProjectBackup[] }) {
  const finals = useGet<FinalCopies>(`/projects/${slug}/final-copies`)
  const all = useGet<AllBackups>('/backups')
  const reads = <><ReadStatus q={finals} what="whether a new deployment can start from it" className="deployment-status-read" /><StaleNote q={[finals, all]} /></>
  const newest = sets.find(b => b.status !== 'pruned') ?? sets[0]
  const startable = finals.data?.copies.some(c => c.deployment === name)
  return <div className="deployment-status">
    <div className="section-body deployment-status-rows">
      {reads}
      <ListRow className="card row run-list-row" state={<span className="row-status"><Status word="stopped" reason="taken down" /></span>}
        identity={<><strong>Taken down</strong><span className="muted small">{takenDownWords(newest)}</span></>}
        action={startable ? <Link to={`/projects/${slug}/deployments`}>Start a new deployment from it</Link> : undefined} />
      <Copies slug={slug} name={name} sets={sets} where={all.data?.where ?? ''} restoreOn={() => false} extra={[]} />
    </div>
    <div className="stack stack-3 deployment-status-facts">
      <div className="section-body"><div className="card deploy-entry"><div className="deploy-lines">
        <DeploymentLine label="Kept">{newest.store === 'store' ? 'backup store' : DISK}</DeploymentLine>
      </div></div></div>
    </div>
  </div>
}

const holds = (b: ProjectBackup) => b.status === 'ok' || b.status === 'partial'

// holdsData says the deployment keeps data AppTown copies (an item declared
// disposable starts empty).
const holdsData = (p: DeploymentProtection) => p.items.some(it => it.copy !== 'disposable')
const day = (iso: string) => new Date(iso).toLocaleDateString('en-AU', { day: 'numeric', month: 'short' })

// why is what took a copy, in words, with the release it was taken before.
function why(slug: string, b: ProjectBackup): ReactNode {
  switch (b.trigger) {
    case 'scheduled': return 'nightly'
    case 'pre-release': return b.beforeRelease ? <Link to={`/projects/${slug}/releases/${b.beforeRelease}`}>before release {b.beforeRelease}</Link> : 'before a release'
    case 'release': return 'after a release'
    case 'manual': return `Back up now · ${b.actor}`
    case 'take-down': return 'final copy, taken down'
    case 'pre-restore': return 'safety copy before a restore'
    default: return b.trigger
  }
}

// setPlace is a set's <project>/<deployment>/<time>, the same in the store
// and under the backups folder on AppTown's disk: a folder left on the disk
// is matched to its set by it (deployment-backups guide).
const setPlace = (b: ProjectBackup, where: string) => b.store === 'store' ? b.path : where && b.path.startsWith(where + '/') ? b.path.slice(where.length + 1) : b.path

type Group = { key: string | null; list: ProjectBackup[] }

// Copies is the deployment's sets, newest first, under the last fourteen
// nights: a run of failures with the same error, or of copies the keep rule
// removed, is one row. Every copy is listed (the keep rule caps how many
// there are). Each copy that holds data has Restore… where
// restoreOn allows it; it asks in place, with what the copy does not hold.
function Copies({ slug, name, sets, where, restoreOn, extra }: { slug: string; name: string; sets: ProjectBackup[]; where: string; restoreOn: (b: ProjectBackup) => boolean; extra: ProjectBackup[] }) {
  const [technical] = useTechnicalDetail()
  const nav = useNavigate()
  const { run, busy, err } = useAction(undefined, { affects: ['/backups', `/projects/${slug}/deployments`] })
  // The copy whose restore is being asked about: the question takes the
  // row's full width under it, as QuietConfirm asks in place.
  const [asking, setAsking] = useState<string | null>(null)
  const restore = async (b: ProjectBackup) => {
    const r = await run(() => post<OperationAdmitted>(`/projects/${slug}/deployments/${encodeURIComponent(name)}/backups/${b.id}/restore`, { confirm: name }))
    if (r?.operationId) nav(`/operations/${r.operationId}`)
  }
  const groups: Group[] = []
  for (const b of sets) {
    const key = b.status === 'failed' ? `f|${b.error ?? ''}` : b.status === 'pruned' ? 'r' : null
    const last = groups[groups.length - 1]
    if (key && last && last.key === key) last.list.push(b); else groups.push({ key, list: [b] })
  }
  const row = (g: Group, from?: ProjectBackup) => {
    const b = g.list[0], n = g.list.length
    const span = n > 1 ? `${day(g.list[n - 1].takenAt)} to ${day(b.takenAt)}` : fmtStamp(b.takenAt)
    // A copy that holds all its data needs no word: only the unusual ones
    // (partial, failed, removed) carry a status.
    const state = b.status === 'ok' ? null
      : b.status === 'partial' ? <Status word="failed" reason="partial" />
      : b.status === 'failed' ? <Status word="failed" reason={n > 1 ? `${n} failed` : undefined} />
      : <Status word="removed" reason={n > 1 ? `${n} removed` : undefined} />
    // Each failed item says its own reason; the set's error is said too when
    // it is not one of theirs (it carries only the first when all fail).
    const failedItems = b.items.filter(i => i.error)
    const missed = failedItems.map(i => `${i.name} not copied: ${i.error}`).join('; ')
    const setError = b.error && !failedItems.some(i => i.error === b.error) ? b.error : ''
    const sub: ReactNode = b.status === 'pruned' ? 'by the keep rule'
      : b.status === 'ok' ? <>{from ? `final copy of ${from.deploymentLabel || from.deployment}, taken down` : why(slug, b)} · {fmtBytes(b.bytes)}</>
      : <>{n === 1 && <>{why(slug, b)} · </>}{[missed, setError].filter(Boolean).join('; ')}{b.status === 'partial' && ` · ${fmtBytes(b.bytes)}`}</>
    return <ListRow key={b.id} className="card row run-list-row" state={state ? <span className="row-status">{state}</span> : undefined}
      identity={<><strong id={`backup-${b.id}`}>{span}</strong>{g.list.slice(1).map(m => <span key={m.id} id={`backup-${m.id}`} />)}<span className="muted small">{sub}</span>
        {b.status !== 'pruned' && <TechnicalWords shown={technical}><span className="muted small">folder <code className="backup-location">{setPlace(b, where)}</code> · {b.store === 'store' ? 'backup store' : DISK}</span></TechnicalWords>}</>}
      action={restoreOn(b) && asking !== b.id ? <button onClick={() => setAsking(b.id)}>Restore…</button> : undefined}
      detail={restoreOn(b) && asking === b.id ? <span className="small row quiet-confirm">
        <span>Restore {name} to {fmtStamp(b.takenAt)}? What is there now is copied first; the app stops while its data is put back.{caveatNote(b.items)}</span>
        <button className="danger" disabled={busy} onClick={() => void restore(b)}>{busy ? '…' : 'Restore'}</button>
        <button disabled={busy} onClick={() => setAsking(null)}>No</button>
        {err && <span className="error small" role="alert">{err}</span>}
      </span> : undefined} />
  }
  const rows = [...groups.map(g => row(g)), ...extra.map(b => row({ key: null, list: [b] }, b))]
  return <>
    {sets.length > 0 && <Nights sets={sets} />}
    {rows}
  </>
}

// Nights is the last fourteen nights at a glance, newest at the left: ✓ a
// copy that holds data, ✗ only failed copies, · only copies the keep rule
// removed, nothing when no copy was taken. Each mark opens its copy. A
// phone shows the last seven.
function Nights({ sets }: { sets: ProjectBackup[] }) {
  const today = new Date(); today.setHours(0, 0, 0, 0)
  const nights = Array.from({ length: 14 }, (_, i) => {
    const start = new Date(today); start.setDate(today.getDate() - i)
    const end = new Date(start); end.setDate(start.getDate() + 1)
    const that = sets.filter(b => { const t = new Date(b.takenAt); return t >= start && t < end })
    const pick = that.find(holds) ?? that.find(b => b.status === 'failed') ?? that.find(b => b.status === 'pruned')
    return { start, pick }
  })
  const mark = (b?: ProjectBackup) => !b ? null : holds(b) ? <span className="ch-mark ok">✓</span> : b.status === 'failed' ? <span className="ch-mark bad">✗</span> : <span className="muted">·</span>
  const word = (b: ProjectBackup) => holds(b) ? 'kept' : b.status === 'failed' ? 'failed' : 'removed'
  return <div className="card backup-nights"><table aria-label="The last fourteen nights">
    <thead><tr>{nights.map((n, i) => <th key={i} scope="col" className={i >= 7 ? 'backup-night-older' : undefined}>{i === 0 ? 'today' : n.start.getDate()}</th>)}</tr></thead>
    <tbody><tr>{nights.map((n, i) => <td key={i} className={i >= 7 ? 'backup-night-older' : undefined}>
      {n.pick && <a href={`#backup-${n.pick.id}`} aria-label={`${day(n.start.toISOString())}: ${word(n.pick)}`} title={`${day(n.start.toISOString())}: ${word(n.pick)}`}>{mark(n.pick)}</a>}
    </td>)}</tr></tbody>
  </table></div>
}

// copyOf is the newest set's copy of a protected item: a data folder by its
// volume, a need by its name.
function copyOf(it: ItemProtection, set?: ProjectBackup): BackupItem | undefined {
  return set?.items.find(i => it.volume ? i.kind === 'volume' && (i.volume === it.volume || i.name === it.volume) : i.name === it.need)
}

// ProtectionCard reads the deployment's protection (the page's own read,
// shared) and says what its backups hold and where.
export function ProtectionCard({ slug, name, newest, bucket }: { slug: string; name: string; newest?: ProjectBackup; bucket?: string }) {
  const q = useGet<DeploymentProtection>(protectionPath(slug, name), { poll: 5000 })
  if (q.denied) return null
  if (!q.data) return <ReadStatus q={q} what="how its data is protected" />
  return <ProtectionFacts slug={slug} name={name} p={q.data} old={q.refreshFailed} newest={newest} bucket={bucket} />
}

// ProtectionFacts is what the deployment's backups hold and where: each
// item's own copy account, how it is copied and, once, what that copy
// misses; where copies are kept; whether a restore has been tested, with
// the doors to test one; and what the answer covers.
function ProtectionFacts({ slug, name, p, old: refreshFailed, newest, bucket }: { slug: string; name: string; p: DeploymentProtection; old: boolean; newest?: ProjectBackup; bucket?: string }) {
  const [technical] = useTechnicalDetail()
  const { run, busy, err } = useAction(undefined, { affects: [protectionPath(slug, name)] })
  const canEditTargets = useHolds('platform:manage-deploy-targets')
  const ops = p.operations
  const applies = (o: { reason?: string }) => o.reason !== 'not-on-this-platform' && o.reason !== 'backups-page'
  const start = (what: 'drills' | 'rehearsals' | 'restored-copies') => run(() => post<OperationAdmitted>(`/projects/${slug}/deployments/${name}/${what}`, {}))
  const missing = p.items.filter(it => it.copy === 'none')
  // The lead row says the backups problem's why once.
  const said = leadNote(p)
  const missingId = (it: ItemProtection) => `backup-${name}-${it.need}-${it.volume || 'data'}`
  const onHost = ops?.copy.reason === 'backups-page'
  const production = p.dataClass === 'production'
  const rehearses = !!ops && p.rehearsalNote !== 'never rehearsed: no rehearsal place'
  const doors = ops && !p.running && holdsData(p) ? [
    ...(applies(ops.check) ? [<button key="check" disabled={busy || !ops.check.available} onClick={() => start('drills')} title={ops.check.why || 'fill the newest copy into a new database, check it, and remove it'}>Check it comes back</button>] : []),
    ...(rehearses ? [<FeatureOnly key="rehearse" area="restore-rehearsals"><button disabled={busy || !ops.rehearsal.available} aria-describedby={missing.length ? missing.map(missingId).join(' ') : undefined} onClick={() => start('rehearsals')} title={ops.rehearsal.why || rehearseTitle}>Test a restore</button></FeatureOnly>] : []),
    ...(production && applies(ops.restore) ? [<FeatureOnly key="here" area="restore-rehearsals"><button disabled={busy || !ops.restore.available} onClick={() => start('restored-copies')} title={ops.restore.why || restoreTestTitle}>Test a restore here</button></FeatureOnly>] : []),
  ] : []
  // Each greyed button's reason, said once: several share one (no copy
  // runner). "No good copy yet" is the item's own line already.
  const greyed = ops ? [ops.copy, ops.check, ...(rehearses ? [ops.rehearsal] : []), ...(production ? [ops.restore] : [])].filter(o => applies(o) && !o.available && o.why && (!missing.length || o.reason !== 'no-copy')) : []
  const whyNot = [...new Set(greyed.map(o => o.why!))]
  const tested = p.rehearsal || p.restoreTest
  const old = refreshFailed || p.reading?.unknown || p.reading?.stale
  const asRead = refreshFailed ? ' (as read before; it could not be read again)' : ''
  return <>
    {p.startFrom && !p.startFrom.filled && <DeploymentLine label="Started from"><span className="muted">a backup set: its first release fills its data before the app starts</span></DeploymentLine>}
    {p.items.map(it => {
      const c = copyOf(it, newest)
      const badge = c ? itemBadge(c) : null
      const limits = [c && !c.error && c.copy === 'live-files' ? liveFilesLimit : '', c?.caveat ?? ''].filter(Boolean).join(' ')
      return <DeploymentLine key={`${it.need}:${it.volume ?? ''}`} label={itemLabel(it, p.items)}>
        {it.copy === 'none' ? <span id={missingId(it)} className={old ? 'muted' : 'error'}>✗ No backup yet</span>
          : <>{(old || it.copyLabel !== 'fresh') && <><Status observation word={old ? 'unknown' : it.copyLabel} reason={!old && it.copyLabel === 'overdue' ? 'overdue' : undefined} help={old ? `Last answer shown${asRead}.` : undefined} /> </>}{it.lastCopy ? `newest ${fmtStamp(it.lastCopy.takenAt)} · ${fmtBytes(it.lastCopy.bytes)}` : copyLine(it)}</>}
        {it.copyNote && it.copyNote !== said && !(it.copy === 'none' && onHost) && <div className="muted small">{it.copyNote}</div>}
        {badge && <div className="muted small">{badge}</div>}
        {limits && <div className="muted small">{limits}</div>}
        {nativeLine(it) && <div className="muted small">{nativeLine(it)}</div>}
        {(!ops || applies(ops.check)) && <div className={!old && it.drill && !it.drill.passed ? 'error small' : 'muted small'}>{drillLine(it)}</div>}
        <TechnicalWords shown={technical}><div className="muted small">{it.where}</div></TechnicalWords>
      </DeploymentLine>
    })}
    {p.unknown && <DeploymentLine label="Holds"><Status word="unknown" /> <span className="muted">{p.unknown}</span></DeploymentLine>}
    {holdsData(p) && <DeploymentLine label="Kept">{onHost
      ? p.keptOn === 'store' ? <>backup store{bucket && ` · ${bucket}`}</> : p.keptOn === 'host' ? DISK : <span className="muted">no copy yet</span>
      : p.note ? <span className={!old && production && !p.offHost ? 'error' : undefined}>{p.note}</span> : p.offHost ? <>backup store{bucket && ` · ${bucket}`}</> : <span className="muted">—</span>}</DeploymentLine>}
    {(production || tested) && <DeploymentLine label="Restore test" trailer={doors.length ? <span className="row row-2">{doors}</span> : undefined}>
      {!tested ? <span className="muted">never</span> : <>
        {p.rehearsal && <div className={!old && !p.rehearsal.passed ? 'error' : undefined}>{rehearsalLine(p.rehearsal, p.rehearsalNote)}</div>}
        {p.rehearsal && <div className="muted small">{heldBack(p.rehearsal)}</div>}
        {p.rehearsal?.previous && <div className="muted small">before that: {rehearsalLine(p.rehearsal.previous).replace('last restore rehearsal ', '')}</div>}
        {p.restoreTest && <div className={!old && !(p.restoreTest.outcome ?? '').startsWith('passed') ? 'error' : undefined}>{restoreTestLine(p.restoreTest, p.restoreTestNote)}</div>}
      </>}
      {whyNot.map(w => <div key={w} className="muted small">{w}</div>)}
      {greyed.some(o => o.reason === 'no-copy-runner') && canEditTargets && <Link to={`/targets/${p.target}`}>Open {p.target}'s settings</Link>}
      {err && <div className="error small">{err}</div>}
    </DeploymentLine>}
    {!production && !tested && doors.length > 0 && <DeploymentLine label="Restore test" trailer={<span className="row row-2">{doors}</span>}>{null}</DeploymentLine>}
    <DeploymentLine label="Covers" trailer={<Link to="/docs/guides/deployment-backups">how backups work →</Link>}>{p.unknown ? <span className="muted">unknown</span> : 'what the app declares'}
      <TechnicalWords shown={technical}><div className="muted small">covers: {p.coverage.assessed.length ? [...new Set(p.coverage.assessed.map(n => /postgres|maria|database|volume/.test(n) ? 'database' : n))].join(', ') : p.unknown ? 'nothing assessed' : `no data ${PRODUCT} knows of`}</div></TechnicalWords>
      <TechnicalWords shown={technical}>{p.coverage.notAssessed.map(n => <div key={n} className="muted small">not assessed: {n}</div>)}</TechnicalWords>
    </DeploymentLine>
    {p.takeDown && !ops?.finalCopy.available && <DeploymentLine label="Take down"><span className="error">{p.takeDown}</span></DeploymentLine>}
  </>
}
