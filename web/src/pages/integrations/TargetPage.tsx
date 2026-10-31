import { useHostStates } from '../../hostState'
import { GlanceRow } from '../../GlanceRow'
import { STATUS_LIGHT, Status } from '../../Status'
import { changedTargetSettings, editTarget, NEEDS_MODES, type TargetSettings as SettingsCommand } from '../../targetCommands'
import { DisplayLabelEdit } from '../../DisplayLabel'
import { PRODUCT } from '../../brand'
import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { HttpError, post } from '../../api'
import { QuietConfirm } from '../../QuietConfirm'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { TargetView, capsWords, placeOf } from '../../targets'
import { ResourceTables, type Resource, type TargetInfra } from '../Infrastructure'
import { Capacity } from './Capacity'

// resourceWord: one word per resource for the "made of" column — a stack,
// a container, a data volume, an app, a database, its need's name when it has one.
const resourceWord = (r: Resource) => {
  const need = (r.hosts ?? []).find((h) => h.need)?.need
  const noun = r.kind === 'volume' ? 'data volume' : r.kind
  return need ? `${noun} for ${need}` : noun
}
import { fmtStamp } from '../../format'
import type { InfrastructureTargets, RegistryReading, TargetPage } from '../../wire.gen'
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
  if (r.unknown) return <div className="small" style={{ marginTop: 6 }}>Container registry{r.registry ? ` ${r.registry}` : ''}: <span className="error">unknown</span> — {r.unknown}<span className="muted">{when}</span></div>
  if (r.none) return <div className="small muted" style={{ marginTop: 6 }}>No container registry yet; the first release creates it{when}</div>
  const used = r.included > 0 ? `${r.used} of ${r.included} repositories` : `${r.used} repositories, no limit`
  return (
    <div className="small" style={{ marginTop: 6 }}>
      Container registry {r.registry}: {r.tier} plan, {used}
      {r.gate && <span className="muted"> (one holds {PRODUCT}'s gate image, for restored copies)</span>}
      {r.full && <> · <span className="chip fail">plan full</span> a project that has not released here before cannot until the plan is upgraded or a deployment is taken down</>}
      <span className="muted">{when}</span>
    </div>
  )
}

const EVENT_WORDS: Record<string, string> = {
  'target.checked': 'checked', 'target.offered': 'offered', 'target.offer-withdrawn': 'offer withdrawn', 'target.retired': 'retired',
  'target.refreshed': 'refreshed from its configuration',
}

export function TargetPage() {
  const { name } = useParams()
  const hostState = useHostStates()
  const nav = useNavigate()
  const pageQ = useGet<TargetPage>(`/targets/${name}`)
  const load = pageQ.reload
  const { data } = pageQ
  const { run, busy, err: actErr } = useAction()
  const infra = useGet<InfrastructureTargets>('/infrastructure', { poll: 30000 })
  const here = infra.data?.targets.find((x) => x.target === name)

  if (!data || !data.target) return <ReadStatus q={pageQ} what="the deployment target" />
  const t = data.target
  const state = hostState(t.name, t.statusLabel)
  const caps = t.capabilities
  const facts: string[] = []
  if (caps.needs?.length) facts.push(`provides ${caps.needs.join(', ')}`)
  if (caps.rollout?.length) facts.push(caps.rollout.join(', '))
  facts.push(caps.persistentVolumes ? 'has disks' : 'no disks · networked databases only')
  if (caps.previews) facts.push('previews may bind here')
  if (caps.ownLiveness) facts.push('restarts its own containers')
  const domain = typeof t.ingress?.domain === 'string' ? t.ingress.domain : ''
  const retired = t.status === 'retired'
  const addr = t.address as Record<string, string | number | undefined>
  const concrete: string[] = []
  if (addr.user && addr.hostname) concrete.push(`ssh ${addr.user}@${addr.hostname}${addr.port && addr.port !== 22 ? ':' + addr.port : ''}`)
  if (addr.keyFrom) concrete.push(`key from secret ${addr.keyFrom}`)
  if (addr.url) concrete.push(String(addr.url))
  if (addr.urlFrom) concrete.push(`URL from secret ${addr.urlFrom}`)
  if (addr.apiKeyFrom) concrete.push(`API key from secret ${addr.apiKeyFrom}`)
  if (addr.environment) concrete.push(`Portainer environment ${addr.environment}`)
  if (addr.region) concrete.push(`region ${addr.region}`)

  const retire = async () => {
    const r = await run(() => post(`/targets/${t.name}/retire`, { confirm: t.name }))
    if (r) nav('/integrations/targets')
  }

  return (
    <div style={{ maxWidth: 860 }}>
      <div className="muted small" style={{ marginBottom: 8 }}><Link to="/infrastructure">Infrastructure</Link> › {t.name}</div>
      <StaleNote q={pageQ} />
      <div className="card" style={{ padding: 20 }}>
        <div className="row" style={{ justifyContent: 'space-between', alignItems: 'flex-start' }}>
          <div>
            <h1 style={{ margin: 0 }}>{t.displayLabel || t.name}</h1>

            <div className="muted">{domain ? `*.${domain}` : 'App address not set'}</div>
          </div>
          <Status word={state.word} tone={state.word === 'working' ? 'pass' : STATUS_LIGHT[state.word]} help={state.help} />
        </div>
        {t.kind === 'do-app' && <p>Cloud hosting: apps update without downtime.</p>}
        {t.retained > 0 && <p className="error">{t.retained} old {t.retained === 1 ? 'resource may still cost' : 'resources may still cost'} money. <a href="#target-resources" onClick={() => { const details = document.getElementById('target-resources') as HTMLDetailsElement | null; if (details) details.open = true }}>Review {t.retained === 1 ? 'it' : 'them'}.</a></p>}
        {state.summary && <p>{state.summary}</p>}
        {here?.resources?.some(r => r.record === 'missing') && <p>Check missing resources below.</p>}

        {!retired && (
          <>
            <h3 style={{ margin: '18px 0 6px' }}>Capacity</h3>
            <Capacity name={t.name} />
          </>
        )}

        <details className="target-deployments"><summary>Apps ({t.deployments.length})</summary>
        {t.deployments.length === 0
          ? <div className="muted small">No deployments on this target.</div>
          : (
            <table className="small">
              <thead><tr><th>deployment</th><th>address</th><th>status</th><th>audience</th><th>data</th><th>made of</th></tr></thead>
              <tbody>
                {t.deployments.map((e) => {
                  const mine = (here?.resources ?? []).filter((r) => (r.hosts ?? []).some((h) => h.project === e.project && h.deployment === e.name))
                  return (
                    <GlanceRow name={0} state={2} labels={["App", "Address", "State", "Audience", "Data", "Resources"]} key={e.project + '/' + e.name}>
                      <td><Link to={`/projects/${e.project}/settings/deployments`}>{e.project}/{e.name}</Link>{e.generation ? <span className="muted"> gen {e.generation}</span> : null}</td>
                      <td>{e.url ? <a href={e.url} target="_blank" rel="noreferrer">{e.url.replace(/^https?:\/\//, '')}</a> : <span className="muted">—</span>}</td>
                      <td><Status word={e.statusLabel} tone={STATUS_LIGHT[e.statusLabel]} help={`Deployment: ${e.readiness}.`} /></td>
                      <td>{e.audience}</td>
                      <td>{e.data}</td>
                      <td>{mine.length === 0
                        ? <span className="muted">{here ? 'nothing found at the target' : '…'}</span>
                        : mine.map((r) => <span key={r.kind + r.name}>{resourceWord(r)} <Status word={r.statusLabel} tone={STATUS_LIGHT[r.statusLabel]} help={r.name + (r.record === 'missing' ? ' · recorded, gone from the target' : '')} /></span>)}</td>
                    </GlanceRow>
                  )
                })}
              </tbody>
            </table>
          )}
        </details>
        <details id="target-resources" className="target-resources"><summary>Resources</summary>
        {here
          ? <Unaccounted here={here} onChanged={() => { infra.reload(); load() }} />
          : retired ? <div className="muted small">retired: not checked</div>
            : infra.err ? <div className="error small">The target's inventory could not be read: {infra.reason}</div>
              : <div className="muted small">checking…</div>}

        {here && (here.resources ?? []).length > 0 && (
          <details style={{ marginTop: 14 }}>
            <summary className="small" style={{ cursor: 'pointer' }}>All resources at the target ({(here.resources ?? []).length})</summary>
            <div style={{ marginTop: 8 }}><ResourceTables t={here} onChanged={() => { infra.reload(); load() }} /></div>
          </details>
        )}

        </details>
        <details className="target-settings"><summary>Settings</summary>
        <DisplayLabelEdit value={t.displayLabel || t.name} machineKey={t.name} path={`/targets/${t.id}/label`} />
        <div className="muted small">{placeOf(t.kindInfo, t.address)}</div>
        <div className="muted small" style={{ marginTop: 6 }}>{facts.join(' · ')} · needs are {t.needsMode}</div>
        {t.ingress?.rehearsals === true && <div className="small" style={{ marginTop: 6 }}><b>Kept for restore rehearsals.</b> {rehearsalHostMeaning}</div>}
        {t.copies && <CopyRunnerLine t={t} />}
        {(concrete.length > 0 || t.provider) && (
          <div className="small" style={{ marginTop: 6 }}>
            {concrete.join(' · ')}
            {t.provider && <> · through the <Link to="/integrations">{t.provider}</Link> connection</>}
          </div>
        )}


        {t.kindInfo.environment && !t.providerScope && !retired && <div className="small" style={{ marginTop: 12 }}>Provider endpoint: <b>unknown</b>. Releases are refused until {PRODUCT} reads the endpoint.</div>}
        {data.registry && <RegistryLine r={data.registry} />}

        {!retired && <button disabled={busy} onClick={() => void run(async () => { await post(`/targets/${t.name}/check`, {}); await load() })}>Check target</button>}
        {actErr && <div className="error small">{actErr}</div>}
        {!retired && !data.builtIn && <EditTarget t={t} current={pageQ.current} onSaved={load} />}
        {!retired && !data.builtIn && <QuietConfirm label="Retire this target" ask={`Retire ${t.name}? This fails while any deployment uses it; its offers are withdrawn.`} onYes={retire} yes="Retire" className="subtle" />}
        </details>

        <details><summary>Access and history</summary>
        <h3 style={{ margin: '18px 0 6px' }}>Offers</h3>
        {t.offers.length === 0
          ? <div className="muted small">Not offered to anyone, so projects cannot deploy here. Offer it from the <Link to="/integrations/targets">targets list</Link>.</div>
          : t.offers.map((o) => (
            <div key={o.id || 'implicit'} className="small">
              <span className="chip accent">{o.group === '*' ? 'everyone' : o.group}</span>{' '}
              {capsWords(o.caps) || <span className="muted">no limits</span>}{o.implicit ? <span className="muted"> · by construction</span> : <span className="muted"> · by {o.createdBy}</span>}
            </div>
          ))}

        <h3 style={{ margin: '18px 0 6px' }}>History</h3>
        {data.history.length === 0
          ? <div className="muted small">No history yet.</div>
          : (
            <div className="small" style={{ background: 'var(--code-bg)', borderRadius: 'var(--radius-sm)', padding: '8px 12px' }}>
              {data.history.map((e, i) => (
                <div key={i}>
                  <span className="muted">{fmtStamp(e.at)}</span> · {EVENT_WORDS[e.kind] ?? e.kind} by {shownActor(e.actor)}
                  {e.kind === 'target.checked' && <> — {(e.payload as Record<string, unknown>).ok ? '✓' : '✗'} {String((e.payload as Record<string, unknown>).detail ?? '')}</>}
                  {e.kind === 'target.offered' && <> — to {String((e.payload as Record<string, unknown>).group) === '*' ? 'everyone' : String((e.payload as Record<string, unknown>).group)}</>}
                  {e.kind === 'target.offer-withdrawn' && <> — from {String((e.payload as Record<string, unknown>).group) === '*' ? 'everyone' : String((e.payload as Record<string, unknown>).group)}</>}
                </div>
              ))}
            </div>
          )}

        {data.builtIn && <div className="muted small" style={{ marginTop: 18 }}>This is {PRODUCT}'s own host; it cannot be retired.</div>}
        </details>
      </div>
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
  if (t.copies?.available) return <div className="small" style={{ marginTop: 6 }}>Copy runner: <Link to={`/infrastructure/${runner}`}>{runner}</Link></div>
  return (
    <div className="card tone-warn small" role="note" style={{ marginTop: 8 }}>
      <b>No copy runner chosen: backups and restore tests of deployments on this target are unavailable.</b> {t.copies?.why}
    </div>
  )
}

// EditTarget: the facts AppTown cannot read off a target and the owner
// must state — the apps domain, whether it keeps disks, whether previews
// may bind, how needs are met. Each save is an audit row with before and after.
// said quotes a value for a sentence, and names an empty one.
const said = (v: string) => (v === '' ? 'empty' : `“${v}”`)
type TargetSettings = Omit<SettingsCommand, 'evals'>
const settingsOf = (t: TargetView): TargetSettings => ({
  domain: typeof t.ingress?.domain === 'string' ? t.ingress.domain : '',
  persistentVolumes: t.capabilities.persistentVolumes, previews: t.capabilities.previews, needsMode: t.needsMode,
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
    <div style={{ marginTop: 14 }}>
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
    <div className="card" style={{ padding: 12, marginTop: 14 }}>
      <div className="row" style={{ gap: 12, flexWrap: 'wrap', alignItems: 'center' }}>
        <label className="small">apps domain <input value={draft.domain} disabled={busy} onChange={(e) => set('domain', e.target.value)} placeholder="none" style={{ width: 200 }} /></label>
        <label className="small"><input type="checkbox" checked={draft.persistentVolumes} disabled={busy} onChange={(e) => set('persistentVolumes', e.target.checked)} /> keeps disks (a dataDir may bind)</label>
        <label className="small"><input type="checkbox" checked={draft.previews} disabled={busy} onChange={(e) => set('previews', e.target.checked)} /> previews may bind</label>
        <label className="small">needs are <select value={draft.needsMode} disabled={busy} onChange={(e) => set('needsMode', e.target.value)}>
          {NEEDS_MODES.map(({ value, title }) => <option key={value} value={value}>{title}</option>)}
        </select></label>
        {t.copies && (
          <label className="small" title={copyRunnerMeaning}>
            copy runner <input value={draft.copyRunner} disabled={busy} onChange={(e) => set('copyRunner', e.target.value.trim())} placeholder="a Docker host target" style={{ width: 160 }} />
          </label>
        )}
        {t.kindInfo.environment && (
          <label className="small" title="The Portainer environment this target releases to, as Portainer lists it.">
            Portainer environment <input value={draft.environment} disabled={busy || Boolean(t.providerScope)} onChange={(e) => set('environment', e.target.value)} placeholder="local" style={{ width: 140 }} />
            {Boolean(t.providerScope) && <span className="muted"> Verified endpoint; attach another target to change it.</span>}
          </label>
        )}
        {t.kind === 'docker-host' && t.origin === 'attached' && (
          <label className="small" title="restore rehearsals boot a deployment's release on a copy of its data here, check it and take it down. With no host kept, rehearsals run on the app's own host.">
            <input type="checkbox" checked={draft.rehearsals} disabled={busy} onChange={(e) => set('rehearsals', e.target.checked)} /> kept for restore rehearsals
          </label>
        )}
      </div>
      {t.copies && <div className="muted small" style={{ marginTop: 6 }}>{copyRunnerMeaning}{draft.copyRunner === '' ? ' Without one, backups and restore tests of deployments on this target are unavailable.' : ''}</div>}
      {draft.rehearsals && !base.settings.rehearsals && <div className="card tone-warn small" style={{ marginTop: 8 }}>{rehearsalHostMeaning}</div>}
      {conflict && (
        <div className="card tone-warn" style={{ marginTop: 8 }}>
          <div>Someone changed this target's settings after you started. Your edits are kept here, unsaved.</div>
          {(Object.keys(now) as (keyof TargetSettings)[]).filter((k) => now[k] !== base.settings[k]).map((k) => (
            <div key={k} className="muted small" style={{ marginTop: 4 }}>{k} is now {said(String(now[k]))} (it was {said(String(base.settings[k]))} when you started).</div>
          ))}
          <div className="row" style={{ marginTop: 8 }}>
            <button onClick={keepMine} disabled={t.settingsRevision <= base.revision}>Keep my edits on top of theirs</button>
            {t.settingsRevision <= base.revision && <span className="muted small">reading the current settings…</span>}
            <button className="subtle" onClick={close}>Discard my edits</button>
          </div>
        </div>
      )}
      <div className="row" style={{ gap: 8, marginTop: 8, alignItems: 'center' }}>
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
export function Unaccounted({ here, onChanged }: { here: TargetInfra; onChanged: () => void }) {
  const failed = !here.live && !!here.error
  return (
    <>
      {failed && <div className="error small" style={{ marginBottom: 6 }}>The target's inventory could not be read: {here.error}{here.asOf ? ` · showing the inventory read ${fmtStamp(here.asOf)}.` : ' · nothing has been read from it, so what is unaccounted for is not known.'}</div>}
      {here.live || here.asOf
        ? <ResourceTables t={here} onChanged={onChanged} only={(r) => r.record !== 'recorded'} empty={here.live ? 'Nothing unaccounted for.' : `Nothing unaccounted for in the inventory read ${fmtStamp(here.asOf!)}.`} />
        : !failed && <div className="muted small">checking…</div>}
      {here.shared && <div className="muted small" style={{ marginTop: 6 }}>{here.shared}</div>}
    </>
  )
}
