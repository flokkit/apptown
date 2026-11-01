import { useHostStates } from '../../hostState'
import { PhoneDetails } from '../../GlanceRow'
import { useNarrow } from '../../chrome'
import { Status } from '../../Status'
import { offerTarget, targetOffer } from '../../targetCommands'
import { PRODUCT } from '../../brand'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { del } from '../../api'
import { QuietConfirm } from '../../QuietConfirm'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { AUDIENCES, DATA_CLASSES, FAMILY_TITLE, TargetView, capsWords, familyRank, placeOf } from '../../targets'
import type { TargetList } from '../../wire.gen'

// Deploy targets (design/subsystems/deployment.md §2.2, §15.7): the places this AppTown
// can put software, and who they are offered to. The instance attaches a
// cloud account and its targets appear here; an administrator offers each
// to a group, or to everyone, with caps; a project then opts in under its
// own Settings → Deployments. The catalog of kinds comes from AppTown,
// so a new provider is a new card, not a new page.


export function TargetsPanel() {
  const q = useGet<TargetList>('/targets')
  const { data, reload: load } = q
  if (!data) return <ReadStatus q={q} what="the deployment targets" />

  const byFamily = new Map<string, TargetView[]>()
  for (const t of data.targets) byFamily.set(t.kindInfo.family, [...(byFamily.get(t.kindInfo.family) ?? []), t])
  const families = [...byFamily.keys()].sort((a, b) => familyRank(a) - familyRank(b))
  const cloud = byFamily.get('cloud') ?? []

  return (
    <div>
      <StaleNote q={q} />
      <details className="small"><summary>About resources</summary><p className="muted small" style={{ maxWidth: 720, marginTop: 0 }}>
        A target is a place software can run: {PRODUCT}'s own Docker host, the intranet, a region of a
        cloud. Connecting a cloud account brings its targets here. A target is usable by a project only
        once it is <b>offered</b> to that project's group · or to everyone · and the offer can cap what
        goes there.
      </p></details>
      {cloud.length === 0 && (
        <div className="card" style={{ marginBottom: 14 }}>
          <div style={{ fontWeight: 600 }}>No cloud targets yet.</div>
          <div className="muted small" style={{ marginTop: 4 }}>
            <Link to="/infrastructure/new">Add one</Link> (DigitalOcean is supported). A new target is offered
            to nobody until you offer it.
          </div>
        </div>
      )}
      <div style={{ marginBottom: 14 }}>
        <Link className="small" to="/infrastructure/new">+ add a deployment target</Link>
      </div>
      {families.map((f) => (
        <div key={f} style={{ marginBottom: 18 }}>
          <h3 style={{ margin: '0 0 8px' }}>{FAMILY_TITLE[f] ?? f}</h3>
          <div className="conn-grid">
            {(byFamily.get(f) ?? []).map((t) => <TargetCard key={t.id} t={t} groups={data.groups} onChanged={load} />)}
          </div>
        </div>
      ))}
    </div>
  )
}

function TargetCard({ t, groups, onChanged }: { t: TargetView; groups: string[]; onChanged: () => void }) {
  const narrow = useNarrow()
  const state = useHostStates()(t.name, t.statusLabel)
  const [offering, setOffering] = useState(false)
  const [showAll, setShowAll] = useState(false)
  const caps = t.capabilities
  const facts: string[] = []
  if (caps.needs?.length) facts.push(`provides ${caps.needs.join(', ')}`)
  if (caps.rollout?.length) facts.push(caps.rollout.join(', '))
  facts.push(caps.persistentVolumes ? 'has disks' : 'no disks · networked databases only')
  if (caps.previews) facts.push('previews')
  const domain = typeof t.ingress?.domain === 'string' ? t.ingress.domain : ''
  const withdraw = (id: string) => del(`/targets/${t.name}/offers/${id}`).then(onChanged)

  return (
    <div className="conn-card" style={{ flexDirection: 'column', alignItems: 'stretch', gap: 6 }}>
      {narrow && <div className="host-row"><strong><Link to={`/infrastructure/${t.name}`}>{t.displayLabel || t.name}</Link></strong><Status observation word={state.word} /><span className="muted small">{state.summary || `${t.deployments.length} ${t.deployments.length === 1 ? 'app' : 'apps'}`}</span></div>}
      <PhoneDetails>
      <div className="row" style={{ justifyContent: 'space-between' }}>
        <div>
          <div style={{ fontWeight: 700 }}><Link to={`/infrastructure/${t.name}`}>{t.displayLabel || t.name}</Link></div>
          <div className="muted small">{placeOf(t.kindInfo, t.address)}{domain ? ` · *.${domain}` : ''}</div>
        </div>
        <Status observation word={state.word} help={state.help} />
      </div>
      <div className="muted small">{facts.join(' · ')}</div>

      <div className="small" style={{ marginTop: 4 }}>
        <span className="muted">Offered to</span>{' '}
        {t.offers.length === 0 && <span className="muted">nobody yet, so projects cannot deploy here</span>}
        <span className="row" style={{ display: 'inline-flex', gap: 6 }}>
          {t.offers.map((o) => (
            <span key={o.id || 'implicit'} className="badge accent" title={o.implicit ? 'offered to everyone by construction' : `offered by ${o.createdBy}`}>
              {o.group === '*' ? 'everyone' : o.group}{capsWords(o.caps) ? ` · ${capsWords(o.caps)}` : ''}
              {!o.implicit && (
                <QuietConfirm label="×" ask={`Withdraw the offer to ${o.group === '*' ? 'everyone' : o.group}? Deployments already bound stay.`}
                  onYes={() => withdraw(o.id!)} className="subtle" yes="Withdraw" title="withdraw this offer" />
              )}
            </span>
          ))}
        </span>
        {!t.offers.some((o) => o.implicit) && !offering && (
          <button className="subtle" style={{ marginLeft: 6 }} onClick={() => setOffering(true)}>+ offer</button>
        )}
      </div>
      {offering && <OfferForm target={t.name} groups={groups} onDone={(saved) => { setOffering(false); if (saved) onChanged() }} />}

      {t.deployments.length > 0 && (
        <div className="small" style={{ marginTop: 4 }}>
          <span className="muted">Running here</span>{' '}
          {(showAll ? t.deployments : t.deployments.slice(0, 6)).map((e) => (
            <span key={`${e.project}/${e.name}`} style={{ marginRight: 8 }}>
              <Link to={`/projects/${e.project}/settings/deployments`}>{e.project}/{e.name}</Link>
              {e.generation > 0 && <span className="muted"> gen {e.generation}</span>}
            </span>
          ))}
          {!showAll && t.deployments.length > 6 && (
            <button className="subtle" onClick={() => setShowAll(true)}>+ {t.deployments.length - 6} more</button>
          )}
        </div>
      )}
      {t.retained > 0 && (
        <div className="small muted">
          {t.retained} resource{t.retained === 1 ? '' : 's'} retained from deployments that were torn down · see the projects' Settings → Deployments.
        </div>
      )}
      </PhoneDetails>
    </div>
  )
}

export function OfferForm({ target, groups, onDone }: { target: string; groups: string[]; onDone: (saved: boolean) => void }) {
  const [group, setGroup] = useState('*')
  const [maxStanding, setMaxStanding] = useState('')
  const [audiences, setAudiences] = useState<string[]>([])
  const [data, setData] = useState<string[]>([])
  const { run, busy, err } = useAction()
  const toggle = (list: string[], set: (v: string[]) => void, x: string) => set(list.includes(x) ? list.filter((y) => y !== x) : [...list, x])
  const save = async () => {
    const r = await run(() => offerTarget(target, targetOffer(group, maxStanding, audiences, data)))
    if (r) onDone(true)
  }
  return (
    <div className="card" style={{ padding: 10, marginTop: 4 }}>
      <div className="form-grid" style={{ maxWidth: 'none' }}>
        <label>To</label>
        <select value={group} onChange={(e) => setGroup(e.target.value)}>
          <option value="*">everyone</option>
          {groups.map((g) => <option key={g} value={g}>{g}</option>)}
        </select>
        <label title="leave empty for no limit">At most</label>
        <span className="row"><input value={maxStanding} onChange={(e) => setMaxStanding(e.target.value)} placeholder="no limit" style={{ width: 90 }} inputMode="numeric" /> <span className="muted small">standing deployments per project</span></span>
        <label title="leave all unticked to allow any">Audiences</label>
        <span className="row">{AUDIENCES.map((x) => (
          <label key={x} className="small"><input type="checkbox" checked={audiences.includes(x)} onChange={() => toggle(audiences, setAudiences, x)} /> {x}</label>
        ))}</span>
        <label title="leave all unticked to allow any">Data</label>
        <span className="row">{DATA_CLASSES.map((x) => (
          <label key={x} className="small"><input type="checkbox" checked={data.includes(x)} onChange={() => toggle(data, setData, x)} /> {x}</label>
        ))}</span>
      </div>
      <div className="row" style={{ marginTop: 8 }}>
        <button className="primary" disabled={busy} onClick={save}>Offer</button>
        <button className="subtle" disabled={busy} onClick={() => onDone(false)}>Cancel</button>
        {err && <span className="error small">{err}</span>}
      </div>
    </div>
  )
}
