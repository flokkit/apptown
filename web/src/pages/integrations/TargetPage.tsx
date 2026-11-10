import { Tech } from '../../Tech'
import { statusProps, failedStatusRefresh } from '../../runtime'
import { BetaChip, targetFeatureArea } from '../../features'
import { usePageName } from '../../chrome'

import './TargetPage.css'
import { useHostStates } from '../../hostState'
import { BackgroundReading } from '../../BackgroundReading'
import { Status } from '../../Status'
import { changedTargetSettings, editTarget, NEEDS_MODES, type TargetSettings as SettingsCommand } from '../../targetCommands'
import { DisplayLabelEdit } from '../../DisplayLabel'
import { PRODUCT } from '../../brand'
import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { HttpError, del, post } from '../../api'
import { QuietConfirm } from '../../QuietConfirm'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { TargetView, capsWords, placeOf } from '../../targets'
import type { TargetInfra } from '../Infrastructure'
import { TargetResourcesTable } from './TargetResources'
import { TargetTabs } from './TargetTabs'
import { Capacity } from './Capacity'
import { CapacityStrip, targetCapacityReadings } from '../../CapacityStrip'
import type { TargetCapacity } from '../../wire.gen'
import { ListTable } from '../../ListTable'
import { Section } from '../../Section'

import { fmtStamp, lastSeen, readingAge } from '../../format'
import type { InfrastructureTargets, RegistryReading, TargetList, TargetPage } from '../../wire.gen'
import { OfferForm } from './Targets'
import { TargetRetained } from './TargetRetained'
import { shownActor } from '../../systemActors'

// One deploy target (design/subsystems/deployment.md §2.2): what it is, what
// is bound to it, whether it answers right now, and the doors an
// administrator has — check it, retire it. Everything the page shows is a
// record; "Check target" records a new one.


// RegistryLine: an App Platform target's container registry as its newest
// check read it — the plan and how many of its repositories are used (and
// whether AppTown's gate image takes one), or why it is unknown — with
// when it was read.
export function RegistryLine({ r }: { r: RegistryReading }) {
  const when = r.readAt ? ` · read ${fmtStamp(r.readAt)}` : ''
  if (r.unknown) return <div className="small target-page-registry-line-div">Container registry{r.registry ? ` ${r.registry}` : ''}: <span className="error">unknown</span> — {r.unknown}<span className="muted">{when}</span></div>
  if (r.none) return <div className="small muted target-page-registry-line-div">No container registry yet; the first release creates it{when}</div>
  const used = r.included > 0 ? `${r.used} of ${r.included} repositories` : `${r.used} repositories, no limit`
  return (
    <div className="small target-page-registry-line-div">
      Container registry {r.registry}: {r.tier} plan, {used}
      {r.gate && <span className="muted"> (one holds {PRODUCT}'s gate image, for restored copies)</span>}
      {r.full && <> · <Status observation word="failed" help="The registry plan is full. Upgrade the plan or remove an unused repository before deploying another app." /> a project that has not released here before cannot until the plan is upgraded or a deployment is taken down</>}
      <span className="muted">{when}</span>
    </div>
  )
}

const EVENT_WORDS: Record<string, string> = {
  'target.checked': 'checked', 'target.offered': 'offered', 'target.offer-withdrawn': 'offer withdrawn', 'target.retired': 'retired',
  'target.refreshed': 'refreshed from its configuration',
}

// What a target is and how it is reached, as its pages say it.
function targetFacts(t: TargetView) {
  const caps = t.capabilities
  const facts: string[] = []
  if (caps.needs?.length) facts.push(`provides ${caps.needs.join(', ')}`)
  if (caps.rollout?.length) facts.push(caps.rollout.join(', '))
  facts.push(caps.persistentVolumes ? 'has disks' : 'no disks · networked databases only')
  if (caps.ownLiveness) facts.push('restarts its own containers')
  const addr = t.address as Record<string, string | number | undefined>
  const concrete: string[] = []
  if (addr.host) concrete.push(String(addr.host))
  if (addr.url) concrete.push(String(addr.url))
  if (addr.urlFrom) concrete.push(`URL from secret ${addr.urlFrom}`)
  if (addr.apiKeyFrom) concrete.push(`API key from secret ${addr.apiKeyFrom}`)
  if (addr.environment) concrete.push(`Portainer environment ${addr.environment}`)
  if (addr.region) concrete.push(`region ${addr.region}`)
  return { facts, addr, concrete }
}

// TargetPage owns the target: inventory, settings, offers and checks.
export function TargetPage({ tab = 'overview' }: { tab?: 'overview' | 'capacity' | 'history' }) {
  const { name } = useParams()
  const hostState = useHostStates()
  const pageQ = useGet<TargetPage>(`/targets/${name}`)
  const load = pageQ.reload
  const { data } = pageQ
  const { run, busy, err: actErr } = useAction()
  const infra = useGet<InfrastructureTargets>('/infrastructure', { poll: 30000 })
  const here = infra.data?.targets.find((x) => x.target === name)
  const capacity = useGet<TargetCapacity>(`/targets/${name}/capacity`)

  usePageName(`/targets/${name}`, data?.target?.displayLabel || name)
  if (!data || !data.target) return <ReadStatus q={pageQ} what="the deployment target" />
  const t = data.target
  const state = hostState(t.name, t.statusLabel)
  const domain = typeof t.ingress?.domain === 'string' ? t.ingress.domain : ''
  const retired = t.status === 'retired'

  const reloadAll = () => { infra.reload(); load() }
  const resources = here?.resources ?? []
  const unaccounted = resources.filter((r) => r.record !== 'recorded').length
  // The header's status is the host's one state (the same word the
  // infrastructure list shows); its reason is said once, in the section it
  // is about, except a failed connection, which is about the whole host.
  return (
    <div>
      <StaleNote q={pageQ} />
      <StaleNote q={infra} />
      <div className="row target-page-div">
        <h1 className="grow target-page-h1">{tab === 'overview' ? t.displayLabel || t.name : tab === 'capacity' ? 'Capacity' : 'History'}</h1>
 {targetFeatureArea(t.kind) !== 'deployments' && <BetaChip />}

        <Status observation word={state.word} help={state.help.startsWith('Host connection') ? undefined : state.help} />
        {!retired && <button className="primary" disabled={busy} onClick={() => void run(async () => { await post(`/targets/${t.name}/check`, {}); await load() })}>Check target</button>}
      </div>
      <div className="muted small mt-1" >{[domain ? `*.${domain}` : '', t.kind === 'do-app' ? 'Cloud hosting: apps update without downtime' : '', state.word === 'unknown' || t.statusLabel === 'failed' ? state.summary : ''].filter(Boolean).join(' · ')}</div>
      <p className="small" data-testid="target-check">Target check: {t.check.detail !== t.check.failure && t.check.detail} <BackgroundReading reading={t.check} /></p>
      {actErr && <div className="error small">{actErr}</div>}
      {t.retained > 0 && <p className="error">{t.retained} old {t.retained === 1 ? 'resource may still cost' : 'resources may still cost'} money. <a href={tab === 'overview' ? '#target-retained' : `/targets/${t.name}#target-retained`}>Review {t.retained === 1 ? 'it' : 'them'}.</a></p>}
      {resources.some(r => r.record === 'missing') && <p>Check missing resources below.</p>}

      <TargetTabs name={t.name} current={tab} />
      {tab === 'capacity' ? <Capacity name={t.name} /> : tab === 'history' ? <>      <Section title="History" count={data.history.length}>
        {data.history.length === 0
          ? <p className="muted small">No history yet.</p>
          : <ListTable className="small" label="Target history" rows={data.history.map((event, index) => ({ ...event, index }))} rowKey={e => String(e.index)} columns={[
            { key: 'event', label: 'Event', phone: 'name', render: e => <>{EVENT_WORDS[e.kind] ?? e.kind} by {shownActor(e.actor)}</> },
            { key: 'detail', label: 'Detail', render: e => <>
              {e.kind === 'target.checked' && <> — {(e.payload as Record<string, unknown>).ok ? '✓' : '✗'} {String((e.payload as Record<string, unknown>).detail ?? '')}</>}
              {e.kind === 'target.offered' && <> — to {String((e.payload as Record<string, unknown>).group) === '*' ? 'everyone' : String((e.payload as Record<string, unknown>).group)}</>}
              {e.kind === 'target.offer-withdrawn' && <> — from {String((e.payload as Record<string, unknown>).group) === '*' ? 'everyone' : String((e.payload as Record<string, unknown>).group)}</>}
            </> },
            { key: 'at', label: 'When', kind: 'time', render: e => fmtStamp(e.at) },
          ]} />}
      </Section></> : <>
      {!retired && <><div className="stack stack-3 mt-3">{targetCapacityReadings(capacity.refreshFailed ? undefined : capacity.data ?? undefined).map(r => <CapacityStrip key={r.label} name={r.label} used={r.used} total={r.total} format={r.format} basis={r.basis} />)}</div><StaleNote q={capacity} /></>}

      <Section title="What runs here" count={t.deployments.length}>
        {t.deployments.length === 0 && <p className="muted small">No deployments on this target.</p>}
        {!!t.deployments.length && <ListTable className="target-deployments-table" label="What runs here" rows={t.deployments} rowKey={e => `${e.project}/${e.name}`} columns={[
          { key: 'name', label: 'Deployment', phone: 'name', render: e => <Link to={`/projects/${e.project}/deployments/${e.name}`}>{e.project}/{e.displayLabel || e.name}</Link> },
          { key: 'state', label: 'State', phone: 'state', render: e => <Status observation {...statusProps(pageQ.refreshFailed ? failedStatusRefresh(e.status, pageQ.reason) : e.status)} /> },
          { key: 'address', label: 'Address', kind: 'technical', render: e => e.url ? <Tech>{e.url.replace(/^https?:\/\//, '')}</Tech> : 'unknown' },
          { key: 'resources', label: 'Resources', kind: 'number', render: e => {
            // Recorded resources remain counted when the host cannot be reached.
            if (!here) return 'unknown'
            const count = resources.filter(r => (r.hosts ?? []).some(h => h.project === e.project && h.deployment === e.name)).length
            return <Link to={`/targets/${t.name}/resources?deployment=${encodeURIComponent(`${e.project}/${e.name}`)}`}>{count}<span className="record-phone"> {count === 1 ? 'resource' : 'resources'}</span></Link>
          } },
          { key: 'generation', label: 'Generation', kind: 'number', wideOnly: true, render: e => e.generation || 'unknown' },
        ]} />}

      </Section>

      <Section title="Not accounted for" count={here && (here.live || here.asOf) ? unaccounted : undefined} label="Resources not accounted for">
        {here
          ? <Unaccounted here={here} onChanged={reloadAll} />
          : retired ? <p className="muted small">Retired: not checked.</p>
            : infra.err ? <p className="muted small">Inventory is unknown: {infra.reason}</p>
              : <p className="muted small">Checking…</p>}
      </Section>

      <TargetRetained name={t.name} />
      <TargetSettings />
      </>}

    </div>
  )
}

// TargetSettingsPage: setting up one deployment target — how it is reached,
// its settings, and which groups may deploy to it.
function TargetSettings() {
  const { name } = useParams()
  const nav = useNavigate()
  const pageQ = useGet<TargetPage>(`/targets/${name}`)
  const listQ = useGet<TargetList>('/targets')
  const load = () => { pageQ.reload(); listQ.reload() }
  const { data } = pageQ
  const { run, err: actErr } = useAction()
  const [offering, setOffering] = useState(false)

  if (!data || !data.target) return <ReadStatus q={pageQ} what="the deployment target" />
  const t = data.target

  const retired = t.status === 'retired'
  const { facts, addr, concrete } = targetFacts(t)
  const retire = async () => {
    const r = await run(() => post(`/targets/${t.name}/retire`, { confirm: t.name }))
    if (r) nav('/targets')
  }
  const withdraw = (id: string) => run(() => del(`/targets/${t.name}/offers/${id}`)).then(load)

  return (
    <div>
      <StaleNote q={pageQ} />
      {actErr && <div className="error small">{actErr}</div>}

      <div className="target-settings"><Section title="Settings">
        <DisplayLabelEdit keyLabel="Target ID" value={t.displayLabel || t.name} machineKey={t.name} path={`/targets/${t.id}/label`} />
        <dl className="facts">
          <dt>Kind</dt><dd>{placeOf(t.kindInfo, t.address)}</dd>
          <dt>Provides</dt><dd>{facts.join(' · ')} · needs are {t.needsMode || 'unknown'}</dd>
          {(concrete.length > 0 || t.provider) && <><dt>Address</dt><dd>{concrete.join(' · ')}{t.provider && <> · through the <Link to="/integrations">{t.provider}</Link> connection</>}</dd></>}
          {addr.keyFrom && <><dt>SSH key</dt><dd>Secret <code>{addr.keyFrom}</code><div className="mt-1"><Link to="/integrations/secrets">Edit secret</Link></div></dd></>}
        </dl>
        {t.ingress?.rehearsals === true && <p className="small"><b>Kept for restore rehearsals.</b> {rehearsalHostMeaning}</p>}
        {t.copies && <CopyRunnerLine t={t} />}
        {t.kindInfo.environment && !t.providerScope && !retired && <p className="small">Provider endpoint: <b>unknown</b>. Releases are refused until {PRODUCT} reads the endpoint.</p>}
        {data.registry && <RegistryLine r={data.registry} />}
        {!retired && !data.builtIn && <EditTarget t={t} current={pageQ.current} onSaved={load} />}
        {!retired && !data.builtIn && <div className="target-page-target-settings-page-div"><QuietConfirm label="Retire this target" ask={`Retire ${t.name}? This fails while any deployment uses it; its offers are withdrawn.`} onYes={retire} yes="Retire" className="subtle danger" /></div>}
        {data.builtIn && <p className="muted small">This is {PRODUCT}'s own host; it cannot be retired.</p>}
      </Section></div>

      <Section title="Offered to" count={t.offers.length}
        action={!retired && !offering && !t.offers.some((o) => o.implicit) && <button onClick={() => setOffering(true)}>Offer</button>}>
        {offering && <OfferForm target={t.name} groups={listQ.data?.groups ?? []} onDone={(saved) => { setOffering(false); if (saved) load() }} />}
        {t.offers.length === 0
          ? <p className="muted small">Not offered to anyone, so projects cannot deploy here.</p>
          : <ListTable className="small" label="Target offers" rows={t.offers} rowKey={o => o.id || 'implicit'} columns={[
            { key: 'group', label: 'Group', phone: 'name', render: o => <span className="badge accent">{o.group === '*' ? 'everyone' : o.group}</span> },
            { key: 'limits', label: 'Limits', render: o => capsWords(o.caps) || <span className="muted">no limits</span> },
            { key: 'by', label: 'Offered by', className: 'muted', render: o => o.implicit ? 'by construction' : `by ${o.createdBy}` },
            { key: 'action', label: '', phone: 'action', render: o => !o.implicit && <QuietConfirm label="Withdraw" ask={`Withdraw the offer to ${o.group === '*' ? 'everyone' : o.group}? Deployments already here stay.`} onYes={() => void withdraw(o.id!)} yes="Withdraw" className="subtle" /> },
          ]} />}
      </Section>

    </div>
  )
}

// What a copy runner is, said beside the field that names one.
export const copyRunnerMeaning = 'A copy runner is a Docker host that runs the commands that copy this target’s databases to the backup store, check a copy comes back, and fill a restored copy.'

// CopyRunnerLine is the copy runner an App Platform target names, or the
// AppTown's own sentence for why backups and restore tests of deployments
// here cannot start without one.
export function CopyRunnerLine({ t }: { t: TargetView }) {
  const runner = typeof t.address?.copyRunner === 'string' ? t.address.copyRunner : ''
  if (t.copies?.available) return <div className="small target-page-registry-line-div">Copy runner: <Link to={`/targets/${runner}`}>{runner}</Link></div>
  return (
    <div className="card tone-warn small mt-2" role="note" >
      <b>No copy runner chosen: backups and restore tests of deployments on this target are unavailable.</b> {t.copies?.why}
    </div>
  )
}

// EditTarget: the facts AppTown cannot read off a target and the owner
// must state — the apps domain, whether it keeps disks, how needs are met. Each save is an audit row with before and after.
// said quotes a value for a sentence, and names an empty one.
const said = (v: string) => (v === '' ? 'empty' : `“${v}”`)
type TargetSettings = Omit<SettingsCommand, 'evals'>
const settingsOf = (t: TargetView): TargetSettings => ({
  domain: typeof t.ingress?.domain === 'string' ? t.ingress.domain : '',
  persistentVolumes: t.capabilities.persistentVolumes, needsMode: t.needsMode,
  copyRunner: typeof t.address?.copyRunner === 'string' ? t.address.copyRunner : '',
  rehearsals: t.ingress?.rehearsals === true,
  environment: typeof t.address?.environment === 'string' ? t.address.environment : '',
})

// What keeping a host for restore rehearsals means, shown to the platform
// administrator before they save it; AppTown keeps the same words with
// the mark (deployment.RehearsalHostMeaning).
export const rehearsalHostMeaning = 'Production data will sit on this host while a restore rehearsal runs. Anyone who controls this host can read it: root, anyone with its Docker socket, any process on it and any container on its network. Keep it for rehearsals and nothing else.'

// EditTarget follows design/architecture/web-data.md: it opens only on an
// answer the page read itself, keeps the draft while the page polls, saves
// only what changed with the settings revision it started from (status checks
// never move it), and on a conflict shows the current settings and lets the
// person keep their edits on top or discard them.
export function EditTarget({ t, current = true, onSaved }: { t: TargetView; current?: boolean; onSaved: () => void }) {
  const [base, setBase] = useState<{ settings: TargetSettings; revision: number } | null>(null)
  const [draft, setDraft] = useState<TargetSettings | null>(null)
  const [conflict, setConflict] = useState(false)
  const { run, busy, err, failure, setErr } = useAction(undefined, { affects: [`/targets/${t.name}`, '/targets', '/infrastructure'] })
  // A refused save means the page's reading is old: read the target again so
  // the person sees what changed before choosing.
  const reread = useRef(onSaved)
  reread.current = onSaved
  useEffect(() => {
    if (failure instanceof HttpError && failure.code === 'settings-moved') { setConflict(true); reread.current() }
  }, [failure])
  const openEditor = () => {
    const start = { settings: settingsOf(t), revision: t.settingsRevision }
    setBase(start); setDraft(start.settings); setConflict(false)
  }
  if (!base || !draft) return (
    <div className="target-page-edit-target-div">
      <button className="subtle" onClick={openEditor} disabled={!current}>Edit target</button>
      {!current && <span className="muted small"> reading the current settings before editing…</span>}
    </div>
  )
  const changes = changedTargetSettings(base.settings, draft)
  const set = <K extends keyof TargetSettings>(k: K, v: TargetSettings[K]) => setDraft((d) => d && { ...d, [k]: v })
  const close = () => { setBase(null); setDraft(null); setConflict(false); setErr('') }
  const save = async () => {
    if (Object.keys(changes).length === 0) { close(); return }
    const r = await run(() => editTarget(t.name, base.revision, changes))
    if (r) { close(); onSaved() }
  }
  const now = settingsOf(t)
  const keepMine = () => {
    setDraft((d) => d && (Object.fromEntries((Object.keys(d) as (keyof TargetSettings)[]).map((k) => [k, d[k] !== base.settings[k] ? d[k] : now[k]])) as TargetSettings))
    setBase({ settings: now, revision: t.settingsRevision })
    setConflict(false); setErr('')
  }
  return (
    <div className="card target-page-edit-target-card">
      <div className="row target-page-edit-target-div-2">
        <label className="small">apps domain <input value={draft.domain} disabled={busy} onChange={(e) => set('domain', e.target.value)} placeholder="none" className="target-page-edit-target-input" /></label>
        <label className="small"><input type="checkbox" checked={draft.persistentVolumes} disabled={busy} onChange={(e) => set('persistentVolumes', e.target.checked)} /> keeps disks (a dataDir may bind)</label>
        <label className="small">needs are <select value={draft.needsMode} disabled={busy} onChange={(e) => set('needsMode', e.target.value)}>
          {NEEDS_MODES.map(({ value, title }) => <option key={value} value={value}>{title}</option>)}
        </select></label>
        {t.copies && (
          <label className="small" title={copyRunnerMeaning}>
            copy runner <input value={draft.copyRunner} disabled={busy} onChange={(e) => set('copyRunner', e.target.value.trim())} placeholder="a Docker host target" className="target-page-edit-target-input-2" />
          </label>
        )}
        {t.kindInfo.environment && (
          <label className="small" title="The Portainer environment this target releases to, as Portainer lists it.">
            Portainer environment <input value={draft.environment} disabled={busy || Boolean(t.providerScope)} onChange={(e) => set('environment', e.target.value)} placeholder="local" className="target-page-edit-target-input-3" />
            {Boolean(t.providerScope) && <span className="muted"> Verified endpoint; attach another target to change it.</span>}
          </label>
        )}
        {t.kind === 'docker-host' && t.origin === 'attached' && (
          <label className="small" title="restore rehearsals boot a deployment's release on a copy of its data here, check it and take it down. With no host kept, rehearsals run on the app's own host.">
            <input type="checkbox" checked={draft.rehearsals} disabled={busy} onChange={(e) => set('rehearsals', e.target.checked)} /> kept for restore rehearsals
          </label>
        )}
      </div>
      {t.copies && <div className="muted small target-page-registry-line-div">{copyRunnerMeaning}{draft.copyRunner === '' ? ' Without one, backups and restore tests of deployments on this target are unavailable.' : ''}</div>}
      {draft.rehearsals && !base.settings.rehearsals && <div className="card tone-warn small mt-2" >{rehearsalHostMeaning}</div>}
      {conflict && (
        <div className="card tone-warn mt-2" >
          <div>Someone changed this target's settings after you started. Your edits are kept here, unsaved.</div>
          {(Object.keys(now) as (keyof TargetSettings)[]).filter((k) => now[k] !== base.settings[k]).map((k) => (
            <div key={k} className="muted small mt-1" >{k} is now {said(String(now[k]))} (it was {said(String(base.settings[k]))} when you started).</div>
          ))}
          <div className="row mt-2" >
            <button onClick={keepMine} disabled={t.settingsRevision <= base.revision}>Keep my edits on top of theirs</button>
            {t.settingsRevision <= base.revision && <span className="muted small">reading the current settings…</span>}
            <button className="subtle" onClick={close}>Discard my edits</button>
          </div>
        </div>
      )}
      <div className="row mt-2 target-page-edit-target-div-3">
        <button onClick={save} disabled={busy || conflict}>{busy ? 'Saving…' : 'Save'}</button>
        <button className="subtle" onClick={close} disabled={busy}>cancel</button>
        {err && !conflict && <span className="error small">{err}</span>}
      </div>
    </div>
  )
}

// Unaccounted is what is at a target that no deployment accounts for. "All
// accounted for" is an answer only the provider can give: under a read that
// failed it says what failed and how old the list shown is, and with no list
// yet it claims nothing.
export function Unaccounted({ here }: { here: TargetInfra; onChanged: () => void }) {
  const failed = !here.live && !!here.error
  return (
    <>
      {here.asOf && <div className="muted small">Inventory {readingAge(here.asOf, !here.live)}{here.invalidated ? " · before the latest change" : ""}</div>}
      {failed && <div className="muted small target-page-unaccounted-error">Inventory is unknown: {(here.error || 'No complete inventory reading').replace(/[.!?]$/, '')}{here.asOf ? `; showing the inventory ${lastSeen(here.asOf)}.` : ', and nothing has been read from it yet, so unaccounted resources are unknown.'}</div>}
      {here.live || here.asOf
        ? <TargetResourcesTable t={here} only={(r) => r.record !== 'recorded'} empty={here.live ? 'Nothing unaccounted for.' : here.invalidated ? 'Nothing unaccounted for in the inventory read before the latest change.' : `Nothing unaccounted for in the inventory ${lastSeen(here.asOf!)}.`} />
        : !failed && <div className="muted small">checking…</div>}
      {here.shared && <div className="muted small target-page-registry-line-div">{here.shared}</div>}
    </>
  )
}
