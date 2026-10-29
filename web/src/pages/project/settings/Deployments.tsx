import { DeploymentAddressEdit } from './DeploymentAddress'
import { ReleaseDefaultPicker } from './ReleaseDefault'
import { DisplayLabelEdit } from '../../../DisplayLabel'
import { PRODUCT } from '../../../brand'
import { AppAddress, RuntimeStatus } from '../../../runtime'
import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { del, post, put } from '../../../api'
import { QuietConfirm } from '../../../QuietConfirm'
import { useAction, useGet, useRefresh } from '../../../data'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import { fmtBytes, fmtWhen } from '../../../format'
import { AUDIENCES, DATA_CLASSES, OfferedTarget, Retained, capsWords, placeOf, placeWords, readinessChip } from '../../../targets'
import { ProtectionLines } from '../../DataProtection'
import { DiagnosticsLines } from './Diagnostics'
import type { AIModelsKey, DeploymentAIModels, DeploymentProtection, DeploymentResources, OverrideResult, ProjectSecrets, DeploymentView, DeploymentChecked, DeploymentRemoved, FinalCopies, OperationAdmitted, ProjectTargets, ReleaseRequested, RetainedList } from '../../../wire.gen'

// Settings → Deployments (design/subsystems/deployment.md §2.3, §2.7, §15.7): the
// standing deployments of this project, each bound to a target the
// project was offered. A project opts in here; what it may opt into is
// whatever its group, or everyone, was offered. Deploying ships the latest
// bundle and opens the release; tearing down backs up the deployment's data,
// then deletes everything AppTown made for it. Data on a target without
// backups, and anything a delete failed on, is listed below until a person
// deletes it and says so.

export function DeploymentsTab({ slug }: { slug: string }) {
  const envsQ = useGet<DeploymentView[]>(`/projects/${slug}/deployments`)
  const offeredQ = useGet<ProjectTargets>(`/projects/${slug}/targets`)
  const retainedQ = useGet<RetainedList>(`/projects/${slug}/retained`)
  const envs = envsQ.data && offeredQ.data && retainedQ.data ? envsQ.data.filter((x) => x.kind !== 'preview') : null
  const offered = offeredQ.data?.targets ?? []
  const retained = retainedQ.data?.retained ?? []
  const load = useRefresh('/projects')

  if (!envs) return <ReadStatus q={[envsQ, offeredQ, retainedQ]} what="the deployments" />
  const open = retained.filter((r) => r.disposition === 'retained')
  const settled = retained.filter((r) => r.disposition !== 'retained')

  return (
    <div>
      <StaleNote q={[envsQ, offeredQ, retainedQ]} />
      <h2 style={{ marginTop: 0 }}>Deployments</h2>
      <p className="muted small" style={{ maxWidth: 720, marginTop: 0 }}>
        Where this app runs. <b>main</b> is fed by every landing. Any other standing deployment is fed by
        promotion: it ships the latest thing that landed when someone deploys it. Each runs on a
        deployment target offered to this project.
      </p>
      <ReleaseDefaultPicker slug={slug} deployments={envs} />
      <div className="conn-grid">
        {envs.map((e) => <EnvCard key={e.name} slug={slug} env={e} onChanged={load} />)}
      </div>

      <AddDeployment slug={slug} offered={offered} existing={envs.map((e) => e.name)} onAdded={load} />

      {(open.length > 0 || settled.length > 0) && (
        <div style={{ marginTop: 22 }}>
          <h3 style={{ marginBottom: 4 }}>Left behind</h3>
          <p className="muted small" style={{ maxWidth: 720, marginTop: 0 }}>
            What a taken-down deployment left at its target: data {PRODUCT} took no final copy of (Portainer
            volumes; an App Platform database when no backup store is attached), and anything {PRODUCT} failed
            to delete, with why. Delete it at the provider, then say so here. Until then, data left here keeps a new
            deployment from taking the old one's name on that target.
          </p>
          <table style={{ fontVariantNumeric: 'tabular-nums' }}>
            <thead><tr><th>What</th><th>From</th><th>Torn down</th><th>Status</th><th></th></tr></thead>
            <tbody>
              {[...open, ...settled].map((r) => <RetainedRow key={r.id} slug={slug} r={r} onChanged={load} />)}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

export function EnvCard({ slug, env: e, onChanged }: { slug: string; env: DeploymentView; onChanged: () => void }) {
  const nav = useNavigate()
  const { run, busy, err } = useAction()
  // A take-down is done now (DeploymentRemoved), or admitted as an
  // operation to follow (202, OperationAdmitted).
  const [torn, setTorn] = useState<DeploymentRemoved | OperationAdmitted | null>(null)
  const [checked, setChecked] = useState<DeploymentChecked | null>(null)
  const checkAgain = async () => {
    const r = await run(() => post<DeploymentChecked>(`/projects/${slug}/deployments/${e.name}/check`, {}))
    if (r) { setChecked(r); onChanged() }
  }
  const deploy = async () => {
    const r = await run(() => post<ReleaseRequested>(`/projects/${slug}/deployments/${e.name}/deploy`, {}))
    if (r) nav(`/projects/${slug}/releases/${r.number}`)
  }
  // What a take-down would do to a cloud deployment's data is AppTown's
  // answer (the same read its protection lines poll): a final copy first,
  // or none possible, when only an explicit choice takes it down.
  const holdsData = e.target?.kind === 'do-app' && !!e.needs && e.needs.length > 0
  const prot = useGet<DeploymentProtection>(holdsData ? `/projects/${slug}/deployments/${e.name}/protection` : null, { poll: 5000 })
  // Only a current answer may state what confirming means: a stale or
  // failed read could offer the choice to keep the data after a final copy
  // became possible, or the other way round. The question is asked again
  // on open, and an open question closes if its answer goes stale.
  const known = !holdsData || prot.current
  const noFinalCopy = known && !!prot.data?.takeDown && !prot.data.operations?.finalCopy.available
  const tearDown = async () => {
    const r = await run(() => del<DeploymentRemoved | OperationAdmitted>(`/projects/${slug}/deployments/${e.name}`, noFinalCopy ? { withoutFinalCopy: e.name } : undefined))
    if (r) { setTorn(r); onChanged() }
  }
  const place = e.target ? placeWords(e.target.title, e.target.vendor, e.target.region) : 'not bound'
  const host = e.url.replace(/^https?:\/\//, '').replace(/\/$/, '')
  return (
    <div className="conn-card" style={{ flexDirection: 'column', alignItems: 'stretch', gap: 6 }}>
      <div className="row" style={{ justifyContent: 'space-between' }}>
        <div>
          <div style={{ fontWeight: 700 }}>
            {e.displayLabel || e.name}
            {e.protected && <span className="chip overridden" style={{ marginLeft: 8 }} title="reaches partners or the public, or holds production data — deploys need the release grant">protected</span>}
          </div>
          <DisplayLabelEdit value={e.displayLabel || e.name} machineKey={e.name} path={`/projects/${slug}/deployments/${e.id}/label`} onChanged={onChanged} />
 <div className="muted small">{place}{e.target ? ` (${e.target.displayLabel || e.target.name})` : ''}</div>
        </div>
        <span className="row" style={{ gap: 6 }}>
          <RuntimeStatus observation={e} />
          {e.readiness && e.readiness !== 'reconciled' && <span className={`chip ${readinessChip(e.readiness)}`}>{e.readiness}</span>}
          {e.restoreCheck && <span className={`chip ${e.restoreCheck === 'gone' ? 'fail' : 'running'}`} title={restoreCheckWords(e.restoreCheck)}>{restoreCheckChip(e.restoreCheck)}</span>}
        </span>
      </div>
      <div className="small">
        {e.url ? <AppAddress url={e.url} hostOnly={e.hostOnly}>{e.hostOnly ? null : host}</AppAddress> : <span className="muted">no address yet</span>}
        {e.generation ? <span className="muted"> · generation {e.generation}</span> : null}
      </div>
      <div className="muted small">
        fed by {e.feed === 'landing' ? 'every landing' : 'promotion'} · for {e.audience === 'organisation' ? 'the organisation' : `the ${e.audience}`} · {e.data} data
      </div>
      {e.needs && e.needs.length > 0 && (
        <div className="small">
          <span className="muted">backed by</span>{' '}
          {e.needs.map((n) => (
            <span key={n.need} title={n.id ? `the provider's id: ${n.id}` : undefined} style={{ marginRight: 8 }}>
              {n.need} <b>{n.name}</b>
              <span className="muted"> {[n.region, n.version && (n.need === 'postgres' ? `pg ${n.version}` : n.version), n.size].filter(Boolean).join(' · ')}</span>
              {n.status !== 'ready' && <span className={`chip ${n.status === 'failed' ? 'fail' : 'running'}`} style={{ marginLeft: 4 }}>{n.status}</span>}
            </span>
          ))}
          <Link className="small" to="/deployments/infrastructure">see the infrastructure</Link>
        </div>
      )}
      {e.target?.kind === 'portainer' && e.id && <DeploymentAddressEdit slug={slug} id={e.id} />}
      {e.aiModels && e.aiModels.length > 0 && <AIModelsLine slug={slug} name={e.name} />}
      {e.generation ? <ResourceLines slug={slug} name={e.name} /> : null}
      {e.generation ? <DiagnosticsLines slug={slug} name={e.name} /> : null}
      {(e.target?.kind === 'do-app' || e.target?.kind === 'docker-host') && <ProtectionLines slug={slug} name={e.name} />}
      {e.restoreCheck && <div className="small">{restoreCheckWords(e.restoreCheck)}</div>}
      {e.dataAfterRestore && (
        <div className="small">
          {e.dataAfterRestore.state === 'waiting' && <>Its data from {PRODUCT}'s backup goes back in when it is rebuilt, before the app starts.</>}
          {e.dataAfterRestore.state === 'filled' && <>Its data came back from {PRODUCT}'s backup{e.dataAfterRestore.set ? <> (set {e.dataAfterRestore.set})</> : null} before the app started.</>}
          {e.dataAfterRestore.state === 'failed' && (
            <span className="error">
              Its data did not come back from {PRODUCT}'s backup: {e.dataAfterRestore.detail}.
              {e.dataAfterRestore.set ? <> Once it runs, restore set {e.dataAfterRestore.set} from its backups.</> : null}
            </span>
          )}
        </div>
      )}
      {e.readiness === 'uncertain' && (
        <div className="small">
          {PRODUCT} does not know whether this deployment stands: its last attempt was cut off, or its
          records disagree. Check again asks its host and settles what it finds; it deploys nothing.
        </div>
      )}
      <div className="row" style={{ marginTop: 4 }}>
        {e.readiness === 'uncertain' && (
          <button className="primary" disabled={busy} onClick={checkAgain} title="ask the host what runs and settle the deployment from that">Check again</button>
        )}
        {e.feed !== 'landing' && e.target && (
          <button className="primary" disabled={busy} onClick={deploy} title="ship the latest bundle here and open the release">Deploy latest</button>
        )}
        {e.name !== 'main' && !torn && (
          <QuietConfirm key={known ? 'known' : 'unknown'} label="Tear down" disabled={!known}
            title={known ? undefined : 'reading what a take-down would do to its data…'}
            prepare={holdsData ? async () => { await prot.reload() } : undefined}
            ask={<>Take down <b>{e.name}</b>? {prot.data?.takeDown || tearDownWords(e.target?.kind)}{e.aiModels?.length ? ' Its AI models key is revoked.' : ''}</>}
            onYes={tearDown} yes={noFinalCopy ? 'Take it down without a final copy' : 'Take it down'} />
        )}
        <Link className="small" to={`/projects/${slug}/releases`}>releases</Link>
        {err && <span className="error small">{err}</span>}
      </div>
      {checked && (
        <div className="small" style={{ marginTop: 4 }}>
          {checked.checked.map((c) => (
            <div key={c.attempt}>attempt {c.attempt}: {c.outcome}{c.reason ? ` — ${c.reason}` : ''}</div>
          ))}
        </div>
      )}
      {torn && 'operationId' in torn && (
        <div className="small" style={{ marginTop: 4 }}>
          Taking it down: the app goes first so nothing writes, then a final copy is taken and checked before the database
          cluster is deleted. <Link to={`/operations/${torn.operationId}`}>Follow it</Link>.
        </div>
      )}
      {torn && !('operationId' in torn) && (
        <div className="small" style={{ marginTop: 4 }}>
          Taken down.
          {torn.finalBackup && <> Its data is in a final backup ({fmtBytes(torn.finalBackup.bytes)}), kept with the project.</>}
          {torn.deleted.length > 0 && <> Deleted: {torn.deleted.join('; ')}.</>}
          {torn.orphans.length > 0 ? <> Left at the target: {torn.orphans.join('; ')}.</> : ' Nothing was left behind.'}
        </div>
      )}
    </div>
  )
}

function AddDeployment({ slug, offered, existing, onAdded }: { slug: string; offered: OfferedTarget[]; existing: string[]; onAdded: () => void }) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [target, setTarget] = useState('')
  const [audience, setAudience] = useState<string>('team')
  const [data, setData] = useState<string>('non-production')
  const [startFrom, setStartFrom] = useState('')
  const finalsQ = useGet<FinalCopies>(open ? `/projects/${slug}/final-copies` : null)
  const { run, busy, err, setErr } = useAction()
  const choices = offered.filter((t) => t.origin !== 'built-in' || t.kind === 'docker-host')
  const picked = choices.find((t) => t.name === target) ?? choices[0]
  useEffect(() => { if (!target && choices[0]) setTarget(choices[0].name) }, [choices, target])
  const add = async () => {
    if (!name.trim()) { setErr('give it a name — lower-case letters, digits and dashes'); return }
    if (existing.includes(name.trim())) { setErr(`there is already a deployment called ${name.trim()}`); return }
    const from = picked?.kind === 'do-app' ? startFrom : ''
    const r = await run(() => post(`/projects/${slug}/deployments`, { name: name.trim(), target: picked?.name, feed: 'promotion', audience, data, ...(from ? { startFrom: from } : {}) }))
    if (r) { setOpen(false); setName(''); onAdded() }
  }
  if (!open) {
    return (
      <div style={{ marginTop: 12 }}>
        <button onClick={() => setOpen(true)}>+ New deployment</button>
        {choices.length === 0 && <span className="muted small" style={{ marginLeft: 8 }}>no deployment target has been offered to this project</span>}
      </div>
    )
  }
  const protectedPick = audience === 'partners' || audience === 'public' || data === 'production'
  return (
    <div className="card" style={{ marginTop: 12, maxWidth: 720 }}>
      <h3 style={{ marginTop: 0 }}>New deployment</h3>
      <div className="form-grid">
        <label>Name</label>
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="staging, cloud, demo…" style={{ width: 240 }} />
        <label>Deployment target</label>
        <div>
          <select value={picked?.name ?? ''} onChange={(e) => setTarget(e.target.value)} disabled={choices.length === 0}>
            {choices.map((t) => <option key={t.name} value={t.name}>{t.name} — {placeOf(t.kindInfo, t.address)}</option>)}
          </select>
          {picked?.offer && (
            <div className="muted small" style={{ marginTop: 4 }}>
              offered {picked.offer.implicit ? 'to everyone (built in)' : `to ${picked.offer.group === '*' ? 'everyone' : picked.offer.group}`}
              {capsWords(picked.offer.caps) ? ` — ${capsWords(picked.offer.caps)}` : ''}
              {picked.bound > 0 ? ` · ${picked.bound} of this project's deployments already here` : ''}
              {picked.capabilities.needs?.length ? ` · provides ${picked.capabilities.needs.join(', ')}` : ''}
            </div>
          )}
        </div>
        <label title="who can reach it">Audience</label>
        <select value={audience} onChange={(e) => setAudience(e.target.value)}>
          {AUDIENCES.map((x) => <option key={x} value={x}>{x}</option>)}
        </select>
        <label title="what kind of data it holds">Data</label>
        <select value={data} onChange={(e) => setData(e.target.value)}>
          {DATA_CLASSES.map((x) => <option key={x} value={x}>{x}</option>)}
        </select>
        {picked?.kind === 'do-app' && (finalsQ.data?.copies.length ?? 0) > 0 && <>
          <label title="its first release fills its database from this copy, and checks it, before the app starts">Start from</label>
          <div>
            <select value={startFrom} onChange={(e) => setStartFrom(e.target.value)}>
              <option value="">empty</option>
              {finalsQ.data!.copies.map((c) => (
                <option key={c.backup} value={c.backup ?? ''}>
                  the final copy of {c.deployment} (taken {fmtWhen(c.takenAt)}, {c.verified ? 'checked' : 'not checked'}, {fmtBytes(c.bytes)})
                </option>
              ))}
            </select>
            {startFrom !== '' && (
              <div className="muted small" style={{ marginTop: 4 }}>
                Putting a copy's data in needs the restore grant here and where it came from. A copy of production data goes only into a deployment that holds production data.
              </div>
            )}
          </div>
        </>}
      </div>
      {protectedPick && (
        <div className="muted small" style={{ marginTop: 8 }}>
          This makes the deployment <b>protected</b>: deploying to it takes the release grant, and its target and protection cannot be changed once something runs there.
        </div>
      )}
      <div className="row" style={{ marginTop: 10 }}>
        <button className="primary" disabled={busy || !picked} onClick={add}>Create</button>
        <button className="subtle" disabled={busy} onClick={() => { setOpen(false); setErr('') }}>Cancel</button>
        {err && <span className="error small">{err}</span>}
      </div>
    </div>
  )
}

export function RetainedRow({ slug, r, onChanged }: { slug: string; r: Retained; onChanged: () => void }) {
  const { run, busy, err } = useAction()
  const [note, setNote] = useState('')
  const settle = () => run(() => post(`/projects/${slug}/retained/${r.id}`, { disposition: 'released', note })).then((ok) => { if (ok) onChanged() })
  const name = String(r.handle?.name ?? (r.handle?.requested as { name?: string } | undefined)?.name ?? '')
  const id = r.handle?.id ? String(r.handle.id) : ''
  // A size in words the driver wrote (an image repository's), or "unknown (why)".
  const size = typeof r.handle?.size === 'string' && r.handle?.kind === 'repository' ? r.handle.size : ''
  return (
    <tr style={r.disposition !== 'retained' ? { opacity: 0.6 } : undefined}>
      <td>
        <b>{r.need}</b> {name}{id && <span className="muted small"> ({id})</span>}
        {size && <div className="muted small">{size}</div>}
        {typeof r.handle?.why === 'string' && <div className="muted small">{r.handle.why}</div>}
        {r.target && <div className="muted small">on {r.target}</div>}
      </td>
      <td>{r.deployment}</td>
      <td className="muted small" style={{ whiteSpace: 'nowrap' }}>{fmtWhen(r.retainedAt)} by {r.retainedBy}</td>
      <td>
        {r.disposition === 'retained'
          ? <span className="chip pending">still at the provider</span>
          : <span className="chip">{r.disposition === 'released' ? 'deleted at the provider' : 'kept on purpose'}{r.settledBy ? ` — ${r.settledBy}` : ''}{r.note ? `: ${r.note}` : ''}</span>}
      </td>
      <td>
        {r.disposition === 'retained' && (
          <span className="row" style={{ gap: 6 }}>
            <input value={note} onChange={(e) => setNote(e.target.value)} placeholder="note (optional)" style={{ width: 160 }} />
            <button className="subtle" disabled={busy} onClick={settle} title="you deleted it at the provider">I deleted it</button>
            {err && <span className="error small">{err}</span>}
          </span>
        )}
      </td>
    </tr>
  )
}

// What a take-down will do, said before anyone confirms it.
export function tearDownWords(kind?: string) {
  switch (kind) {
    case 'do-app':
      return 'The app is deleted first, so nothing writes, and so are its images when no other deployment of this project on that account uses them. With a backup store attached and a copy runner named, a final copy of its database goes to the store and is checked by filling a new database from it; only then is the database cluster deleted, and the copy is kept 30 days to start a new deployment from. If the copy falls short, the cluster stays. When no final copy can be taken (no store, no copy runner), the take-down is refused unless you choose to go without one, and the cluster stays.'
    case 's3-bucket':
      return 'The bucket and every file in it are deleted, and the site stops answering. A static site holds no data, so there is nothing to back up.'
    case 'portainer':
      return `Its data is backed up first, through Portainer; then the stack ${PRODUCT} made and the volumes its deploys made are deleted. A volume that was there before its first deploy is copied and kept. When ${PRODUCT} cannot reach the environment for its data, the take-down is refused unless you choose to go without a final copy, and the volumes stay.`
    default:
      return `Its data is backed up first; then everything ${PRODUCT} made for it is deleted. Stop anything outside this deployment that writes to its data before you go on.`
  }
}

const dollars = (n: number) => `$${n.toFixed(2)}`
const periodWords: Record<string, string> = { daily: 'today', weekly: 'this week', monthly: 'this month' }

// allowanceWords says what an AI models key may still spend, over the same
// period as its allowance, with lifetime spend apart: a key that has spent
// $12 in all may still have $4 of its $5 this month.
export function allowanceWords(k: AIModelsKey): string {
  const a = k.allowance
  if (!a) return `allowance unknown${k.unknown ? ` — ${k.unknown}` : ''}`
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
    <div className="small">
      <span className="muted">AI models</span>{' '}
      {q.loading ? <span className="muted">reading the allowance…</span>
        : !q.data ? <span className="error">allowance unknown — {q.err || 'no answer'}</span>
        : q.data.keys.map((k) => (
          <span key={k.name} title={`OpenRouter key ${k.name}`} style={{ marginRight: 8 }}>
            {k.allowance ? allowanceWords(k) : <span className="muted">{allowanceWords(k)}</span>}
          </span>
        ))}
      <StaleNote q={q} inline />
    </div>
  )
}

// howWords says how a deployment's profile was chosen.
const howWords: Record<string, string> = { chosen: 'chosen', preview: 'the preview profile', default: 'the default' }

// ResourceLines is what the deployment's running generation applied of its
// profile, part by part, what each part uses now, and its operators'
// overrides. A limit nobody declared reads "no limit"; a generation from
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
  if (q.loading) return <div className="muted small">reading its resources…</div>
  if (!q.data) return <div className="error small">resources unknown — {q.err || 'no answer'}</div>
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
  return (
    <div className="small">
      <div>
        <span className="muted">profile</span>{' '}
        {r.recorded ? <><b>{r.profile}</b> <span className="muted">({howWords[r.how || ''] || r.how})</span></> : <span className="muted">not recorded</span>}
        {mine && choice.data && choice.data.choosable.length > 1 && (
          <select style={{ marginLeft: 8 }} value={mine.chosen} disabled={busy} onChange={(ev) => choose(ev.target.value)}
            title="the profile this deployment uses from its next deploy, for app.yaml and secretspec.toml alike">
            {choice.data.choosable.map((n) => <option key={n} value={n}>{n}</option>)}
          </select>
        )}
        {mine && r.recorded && mine.chosen !== r.profile && <span className="chip pending" style={{ marginLeft: 6 }} title="takes effect at the next deploy">next: {mine.chosen}</span>}
      </div>
      <table className="small" style={{ marginTop: 2 }}>
        <thead><tr><th>part</th><th>memory</th><th>cpu</th><th>using now</th><th></th></tr></thead>
        <tbody>
          {r.parts.map((p) => (
            <tr key={p.part || 'app'}>
              <td>{p.part || 'app'}{p.public && p.part ? <span className="muted"> (front door)</span> : null}{p.need ? <span className="muted"> (service)</span> : null}{p.suppliedService ? <span className="muted"> (workflow service)</span> : null}</td>
              {/* 'forge' is AppTown's default as generations from before the AppTown rename recorded it. */}
              <td>{p.memory}{p.size && <div className="muted">size {p.size}</div>}{p.memoryDefault && <div className="muted">{p.memoryDefault.kind === 'catalogue' ? `catalogue ${p.memoryDefault.name}` : (p.memoryDefault.kind === 'apptown' || p.memoryDefault.kind === 'forge') ? PRODUCT : p.memoryDefault.kind} default</div>}{p.memoryUnlimited && <span className="chip pending">explicit no limit</span>}{p.runsOn && <div className="muted">{p.runsOn}</div>}</td>
              <td>{p.cpu}</td>
              <td>{p.observed.known ? fmtBytes(p.observed.usedBytes) : <span className="muted" title={p.observed.reason}>unknown</span>}</td>
              <td>
                {p.override && (
                  <span className={`chip ${p.overrideApplied ? 'overridden' : 'pending'}`} title={`${p.override.reason} — ${p.override.setBy}, ${p.override.setAt}`}>
                    override {p.override.memoryUnlimited ? "no memory limit " : ""}{p.override.resources.memoryBytes ? fmtBytes(p.override.resources.memoryBytes) : ''}{p.override.resources.cpus ? ` ${p.override.resources.cpus} cpu` : ''}
                    {p.overrideApplied ? '' : ' · not applied yet'}
                  </span>
                )}
                {p.override && !p.suppliedService && <button className="link small" disabled={busy} onClick={() => clear(p.part)}>clear</button>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {r.parts.some((p) => p.suppliedService) && <div className="muted">Workflow service limits are set in the workflow service declaration.</div>}
      <ShareLine share={r.share} />
      <LimitEventLines r={r} />
      {parts.length > 0 && !open && <button className="link small" onClick={() => setOpen(true)}>Override memory…</button>}
      {open && picked && (
        <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
          {parts.length > 1 && (
            <select value={picked} onChange={(ev) => setPart(ev.target.value)}>
              {parts.map((p) => <option key={p.part} value={p.part}>{p.part}</option>)}
            </select>
          )}
          <input placeholder="memory, e.g. 2Gi or none" value={memory} onChange={(ev) => setMemory(ev.target.value)} style={{ width: 120 }} />
          <input placeholder="why" value={reason} onChange={(ev) => setReason(ev.target.value)} style={{ minWidth: 220 }} />
          <button className="primary" disabled={busy || !memory || !reason.trim()} onClick={set} title="records who and why, then redeploys what runs so it applies now">Override and redeploy</button>
          <button className="link small" onClick={() => setOpen(false)}>cancel</button>
        </div>
      )}
      {said && <div className="muted">{said}</div>}
      {err && <div className="error">{err}</div>}
      <StaleNote q={q} inline />
    </div>
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
  if (!r.limitsKnown) return <div className="muted">limit events: {r.limitsSource}</div>
  if (r.limits.length === 0) return <div className="muted" title={r.limitsSource}>no out-of-memory kills or exits recorded in the last day</div>
  return (
    <ul className="small" style={{ margin: '2px 0', paddingLeft: 16 }} title={r.limitsSource}>
      {r.limits.map((l) => (
        <li key={`${l.part}/${l.kind}`}>
          <b>{l.part || 'app'}</b>: {limitKindWords[l.kind] || l.kind}, {timesWords(l.count)} in the last day <span className="muted">(last {new Date(l.last).toLocaleTimeString()})</span>
        </li>
      ))}
    </ul>
  )
}

// ShareLine says what the deployment holds of a fixed host: its declared
// limits and its use against the host's memory, and the host's state;
// on an elastic target, that it grows.
export function ShareLine({ share }: { share?: DeploymentResources['share'] }) {
  if (!share) return null
  if (share.elastic) return <div className="muted">on {share.target}, which grows on demand</div>
  const total = share.hostMemory.known ? share.hostMemory.value : 0
  const pct = (v: number) => (total > 0 ? ` (${Math.round((v / total) * 100)}%)` : '')
  return (
    <div>
      <span className="muted">holds</span>{' '}
      {share.declared.known ? <>{fmtBytes(share.declared.value)}{pct(share.declared.value)} declared</> : <span className="muted" title={share.declared.reason}>an unknown share (no limit on every part)</span>}
      {' '}<span className="muted">of {share.target}'s</span>{' '}
      {share.hostMemory.known ? fmtBytes(share.hostMemory.value) : <span className="muted" title={share.hostMemory.reason}>unknown memory</span>}
      {share.used.known && <>; uses {fmtBytes(share.used.value)}</>}
      {share.overcommitted && <span className="chip pending" style={{ marginLeft: 6 }} title="the limits on this host add up to more than it has">host overcommitted</span>}
      {share.diskRefuses
        ? <span className="chip failed" style={{ marginLeft: 6 }} title="the host's disk is below the space new work needs; a deploy reads it again when it starts">host short of disk: deploys refused</span>
        : share.lowDisk && <span className="chip failed" style={{ marginLeft: 6 }} title="free space is below the host's disk warning threshold">host disk low</span>}
    </div>
  )
}

// What holds a deployment after AppTown was restored, as a chip and as a
// sentence.
function restoreCheckChip(check: string): string {
  return check === 'unconfirmed' ? 'not yet confirmed since restore' : check === 'gone' ? 'gone since restore' : check === 'not-filled' ? 'not started since restore' : 'waiting for its data'
}

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
