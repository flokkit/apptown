import { changedTargetSettings, editTarget, offerTarget, targetOffer, NEEDS_MODES, type TargetSettings } from '../targetCommands'
import { PRODUCT } from '../brand'
import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { HttpError, post } from '../api'
import { useQueryClient } from '@tanstack/react-query'
import { firstErr, getKey, useAction, useGet } from '../data'
import { AUDIENCES, DATA_CLASSES } from '../targets'
import type { CloudAccountCheck, Target, TargetAttached, TargetCheck, TargetEdited, TargetPage, TargetList } from '../wire.gen'
import { Glyph } from '../parts/Glyph'
import { SecretNameStatus } from './integrations/InstanceSecrets'

// Add a deployment target (design/subsystems/deployment.md §2.2, §8.3a): a
// short wizard on its own page. Kind, then connection — the target must
// answer before it is recorded — then the offer and the facts AppTown
// cannot read off it. The doors underneath are the ones the target page
// uses: POST /targets (dryRun for the check), POST /targets/{name}/offers,
// PATCH /targets/{name}. Finish lands on the target's own page.

type Kind = 'docker-host' | 'portainer' | 'do-app' | 's3-bucket'
const DO_REGIONS = ['syd', 'sgp', 'blr', 'nyc', 'sfo', 'tor', 'lon', 'fra', 'ams']
const KINDS: { kind: Kind; title: string; vendor: string; blurb: string }[] = [
  { kind: 'docker-host', title: 'Docker host', vendor: 'over SSH', blurb: `A machine with Docker, reached over SSH (for example a droplet). ${PRODUCT} builds on the host, gives the data volume to the image's user, and routes apps through the host's Traefik when it has an apps domain.` },
  { kind: 'portainer', title: 'Portainer environment', vendor: 'Portainer', blurb: 'Experimental: no qualification has released a real app to it yet. An environment Portainer administers. Portainer owns restarts; apps are reached under the apps domain through its Traefik.' },
  { kind: 'do-app', title: 'DigitalOcean App Platform', vendor: 'DigitalOcean', blurb: 'Experimental: no qualification has released a real app to it yet. A DigitalOcean team: App Platform runs the app, Managed Databases hold its data, the container registry holds its images. One target per account and region.' },
  { kind: 's3-bucket', title: 'Bucket website', vendor: 'S3-compatible storage', blurb: `Experimental: no qualification has released a real app to it yet. Static-only apps published as files to a bucket (DigitalOcean Spaces first) and served from its website address. Each deployment gets a bucket of its own; taking the deployment down deletes it, so keep nothing else there. The key must be able to create buckets: a full-access key reaches every bucket in its team, so use one made for ${PRODUCT} alone.` },
]

export function InfrastructureNew() {
  const nav = useNavigate()
  const [step, setStep] = useState<1 | 2 | 3>(1)
  const [kind, setKind] = useState<Kind | null>(null)
  const [saved, setSaved] = useState<Target | null>(null)
  const chosen = KINDS.find((k) => k.kind === kind)
  return (
    <div style={{ maxWidth: 860 }}>
      <div className="row" style={{ gap: 8, alignItems: 'baseline' }}>
        <Link to="/infrastructure" className="small">Infrastructure</Link><span className="muted small">/</span>
        <h2 style={{ margin: 0 }}>Add a deployment target</h2>
      </div>
      <ol className="wiz-steps">
        <li className={step === 1 ? 'current' : 'done'}>Kind{chosen && step > 1 ? <small>{chosen.title}</small> : null}</li>
        <li className={step === 2 ? 'current' : step > 2 ? 'done' : ''}>Connection{saved ? <small>{saved.name} connected</small> : null}</li>
        <li className={step === 3 ? 'current' : ''}>Offer and facts</li>
      </ol>
      {step === 1 && <KindStep kind={kind} onPick={(k) => { setKind(k); setStep(2) }} />}
      {step === 2 && kind && <ConnectionStep kind={kind} onBack={() => setStep(1)} onSaved={(t) => { setSaved(t); setStep(3) }} />}
      {step === 3 && saved && <OfferStep target={saved} onDone={() => nav(`/infrastructure/${saved.name}`)} />}
    </div>
  )
}

function KindStep({ kind, onPick }: { kind: Kind | null; onPick: (k: Kind) => void }) {
  return (
    <div>
      <p className="muted small" style={{ marginTop: 0 }}>What kind of target is this?</p>
      <div className="infra-grid">
        {KINDS.map((k) => (
          <button key={k.kind} className={'card infra-card wiz-kind' + (kind === k.kind ? ' active' : '')} onClick={() => onPick(k.kind)}>
            <div className="infra-head">
              <span className="infra-glyph"><Glyph kind={k.kind} /></span>
              <div>
                <div className="infra-title">{k.title}</div>
                <div className="infra-kind">{k.vendor}</div>
              </div>
            </div>
            <div className="small muted" style={{ textAlign: 'left' }}>{k.blurb}</div>
          </button>
        ))}
      </div>
    </div>
  )
}

// ConnectionStep: the kind's fields, named as secrets AppTown already
// holds. Check asks the target to answer without recording anything; the
// words it answers with are shown; only then may the target be added.
function ConnectionStep({ kind, onBack, onSaved }: { kind: Kind; onBack: () => void; onSaved: (t: Target) => void }) {
  const [name, setName] = useState('')
  const [host, setHost] = useState('ssh://root@')
  const [keyFrom, setKeyFrom] = useState('')
  const [urlFrom, setUrlFrom] = useState('')
  const [apiKeyFrom, setApiKeyFrom] = useState('')
  const [environment, setEnvironment] = useState('')
  const [tokenFrom, setTokenFrom] = useState('DIGITALOCEAN_ACCESS_TOKEN')
  const [region, setRegion] = useState('syd')
  const [registry, setRegistry] = useState('')
  const [endpoint, setEndpoint] = useState('https://syd1.digitaloceanspaces.com')
  const [bucketRegion, setBucketRegion] = useState('syd1')
  const [websiteEndpoint, setWebsiteEndpoint] = useState('https://{bucket}.{region}-static.digitaloceanspaces.com')
  const [accessKeyFrom, setAccessKeyFrom] = useState('SPACES_ACCESS_KEY')
  const [secretKeyFrom, setSecretKeyFrom] = useState('SPACES_SECRET_KEY')
  const [answer, setAnswer] = useState<{ for: string; detail: string; registry?: string } | null>(null)
  const { run, busy, err } = useAction()
  const body = kind === 'docker-host' ? { kind, name, host, keyFrom }
    : kind === 'portainer' ? { kind, name, urlFrom, apiKeyFrom, environment }
    : kind === 's3-bucket' ? { kind, name, endpoint, region: bucketRegion, websiteEndpoint, accessKeyFrom, secretKeyFrom }
    : { kind, name, tokenFrom, region }
  const key = JSON.stringify(body)
  const filled = !!name && (kind === 'docker-host' ? !!host && !!keyFrom : kind === 'portainer' ? !!urlFrom && !!apiKeyFrom && !!environment
    : kind === 's3-bucket' ? !!endpoint && !!bucketRegion && !!websiteEndpoint && !!accessKeyFrom && !!secretKeyFrom : !!tokenFrom && !!region)
  const checked = answer?.for === key
  const check = async () => {
    const r = await run(() => post<TargetCheck | CloudAccountCheck>('/targets', { ...body, dryRun: true }))
    if (r) setAnswer({ for: key, detail: r.detail, registry: 'registry' in r ? r.registry : undefined })
  }
  const add = async () => {
    const r = await run(() => post<TargetAttached>('/targets', kind === 'do-app' && registry ? { ...body, registry } : body))
    if (r?.target) onSaved(r.target)
  }
  const field = (label: string, value: string, set: (v: string) => void, placeholder = '', width = 200) => (
    <label className="small">{label}<input value={value} onChange={(e) => set(e.target.value)} placeholder={placeholder} style={{ width }} /></label>
  )
  return (
    <div className="card" style={{ padding: 14 }}>
      <p className="muted small" style={{ marginTop: 0 }}>
        {KINDS.find((k) => k.kind === kind)?.blurb} Each credential is the name of a secret {PRODUCT} holds. Set any missing one below; its value is stored
        in {PRODUCT}'s secrets file, not on the target, and is not shown again.
      </p>
      <div className="wiz-fields">
        {field('name', name, setName, kind === 'do-app' ? 'do-syd' : kind === 'portainer' ? 'home-portainer' : kind === 's3-bucket' ? 'spaces-syd' : 'droplet-syd', 160)}
        {kind === 'docker-host' && <>
          {field('address', host, setHost, 'ssh://root@1.2.3.4', 240)}
          {field('SSH key secret', keyFrom, setKeyFrom, 'DROPLET_SSH_KEY', 180)}
        </>}
        {kind === 'portainer' && <>
          {field('URL secret', urlFrom, setUrlFrom, 'PORTAINER_URL', 170)}
          {field('API key secret', apiKeyFrom, setApiKeyFrom, 'PORTAINER_API_KEY', 180)}
          {field('environment', environment, setEnvironment, 'production', 140)}
        </>}
        {kind === 's3-bucket' && <>
          {field('endpoint', endpoint, setEndpoint, 'https://syd1.digitaloceanspaces.com', 260)}
          {field('region', bucketRegion, setBucketRegion, 'syd1', 90)}
          {field('website address', websiteEndpoint, setWebsiteEndpoint, 'https://{bucket}.{region}-static.digitaloceanspaces.com', 380)}
          {field('access key secret', accessKeyFrom, setAccessKeyFrom, 'SPACES_ACCESS_KEY', 180)}
          {field('secret key secret', secretKeyFrom, setSecretKeyFrom, 'SPACES_SECRET_KEY', 180)}
        </>}
        {kind === 'do-app' && <>
          {field('token secret', tokenFrom, setTokenFrom, '', 240)}
          <label className="small">region<select value={region} onChange={(e) => setRegion(e.target.value)}>{DO_REGIONS.map((r) => <option key={r} value={r}>{r}</option>)}</select></label>
        </>}
      </div>
      <div style={{ marginTop: 8, display: 'grid', gap: 4 }}>
        {(kind === 'docker-host' ? [keyFrom] : kind === 'portainer' ? [urlFrom, apiKeyFrom] : kind === 's3-bucket' ? [accessKeyFrom, secretKeyFrom] : [tokenFrom]).map((n, i) => <SecretNameStatus key={i} name={n} />)}
      </div>
      <div className="wiz-answer">
        {busy ? <span className="muted small">checking…</span>
          : err ? <span className="error small">{err}</span>
          : checked ? <span className="small">✓ {answer.detail}</span>
          : answer ? <span className="muted small">fields changed since the last check — check again</span>
          : <span className="muted small">not checked yet</span>}
      </div>
      {kind === 'do-app' && checked && answer.registry === '' && (
        <div className="wiz-fields" style={{ marginTop: 8 }}>
          {field('container registry to create', registry, setRegistry, 'apptown', 200)}
          <span className="muted small">The account has no container registry. App Platform pulls images from one, so {PRODUCT} creates it on the first release. Names are unique across DigitalOcean.</span>
        </div>
      )}
      <div className="row" style={{ gap: 8, marginTop: 10, alignItems: 'center' }}>
        <button className="subtle" onClick={onBack} disabled={busy}>Back</button>
        <button onClick={check} disabled={busy || !filled}>Check</button>
        <button className="primary" onClick={add} disabled={busy || !checked} title={checked ? '' : 'check first'}>Add target</button>
      </div>
    </div>
  )
}

// OfferStep: who may bind here and with what caps, and the facts only the
// owner can state. The target is already recorded; skipping leaves it
// offered to nobody, which the target page can change later.
type OfferFacts = Pick<TargetSettings, 'domain' | 'persistentVolumes' | 'previews' | 'needsMode'>
const factsOf = (target: Target): OfferFacts => ({
  domain: typeof target.ingress?.domain === 'string' ? target.ingress.domain : '',
  persistentVolumes: target.capabilities.persistentVolumes,
  previews: target.capabilities.previews, needsMode: target.needsMode,
})

export function OfferStep({ target, onDone }: { target: Target; onDone: () => void }) {
  const groupsQ = useGet<TargetList>('/targets')
  const groups = groupsQ.data?.groups ?? []
  const [offer, setOffer] = useState(true)
  const [group, setGroup] = useState('*')
  const [maxStanding, setMaxStanding] = useState('')
  const [audiences, setAudiences] = useState<string[]>([])
  const [data, setData] = useState<string[]>([])
  const [base, setBase] = useState(target)
  const [draft, setDraft] = useState(() => factsOf(target))
  const [conflict, setConflict] = useState(false)
  const path = `/targets/${target.name}`
  const currentQ = useGet<TargetPage>(conflict ? path : null)
  const client = useQueryClient()
  const { run, busy, err: actErr, setErr } = useAction()
  const err = firstErr(actErr, groupsQ.err, currentQ.err)
  const set = <K extends keyof OfferFacts>(key: K, value: OfferFacts[K]) => setDraft((d) => ({ ...d, [key]: value }))
  const toggle = (list: string[], set: (v: string[]) => void, x: string) => set(list.includes(x) ? list.filter((y) => y !== x) : [...list, x])
  const finish = async () => {
    if (conflict) return
    const ok = await run(async () => {
      const offered = offer ? targetOffer(group, maxStanding, audiences, data) : null
      const before = factsOf(base)
      const changes = changedTargetSettings(before, draft)
      if (Object.keys(changes).length) {
        let saved: TargetEdited
        try {
          saved = await editTarget(target.name, base.settingsRevision, changes)
        } catch (error) {
          if (error instanceof HttpError && error.code === 'settings-moved') setConflict(true)
          throw error
        }
        if (!saved.target) throw new Error('The saved target was not returned. Open its page before retrying.')
        // This edit succeeded even if offering subsequently fails. Preserve the
        // server's normalized facts and revision, not the submitted spelling.
        const confirmed = saved.target
        setBase(confirmed); setDraft(factsOf(confirmed))
        client.setQueryData<TargetPage>(getKey(path), (old) => old?.target
          ? { ...old, target: { ...old.target, ...confirmed } } : old)
        void client.invalidateQueries({ queryKey: getKey(path) })
      }
      if (offered) await offerTarget(target.name, offered)
      return true
    })
    if (ok) onDone()
  }
  const current = currentQ.current && !currentQ.refreshing ? currentQ.data?.target : null
  const resolve = (keep: boolean) => {
    if (!current || busy) return
    const before = factsOf(base), now = factsOf(current)
    setDraft(keep ? Object.fromEntries((Object.keys(draft) as (keyof OfferFacts)[])
      .map((key) => [key, draft[key] !== before[key] ? draft[key] : now[key]])) as OfferFacts : now)
    setBase(current); setConflict(false); setErr('')
  }
  const cloud = target.kind === 'do-app'
  return (
    <div className="card" style={{ padding: 14 }}>
      <p className="muted small" style={{ marginTop: 0 }}><b>{target.name}</b> is added. Choose who it is offered to, and describe what {PRODUCT} cannot detect.</p>
      <fieldset disabled={busy} style={{ border: 0, padding: 0, margin: 0 }}>
      <div className="wiz-section">Offer</div>
      <div className="form-grid" style={{ maxWidth: 'none' }}>
        <label>To</label>
        <span className="row">
          <select value={offer ? group : ''} onChange={(e) => { if (e.target.value === '') setOffer(false); else { setOffer(true); setGroup(e.target.value) } }}>
            <option value="*">everyone</option>
            {groups.map((g) => <option key={g} value={g}>{g}</option>)}
            <option value="">nobody yet</option>
          </select>
        </span>
        {offer && <>
          <label title="leave empty for no limit">At most</label>
          <span className="row"><input value={maxStanding} onChange={(e) => setMaxStanding(e.target.value)} placeholder="no limit" style={{ width: 90 }} inputMode="numeric" /> <span className="muted small">standing deployments per project</span></span>
          <label title="leave all unticked to allow any">Audiences</label>
          <span className="row">{AUDIENCES.map((x) => <label key={x} className="small"><input type="checkbox" checked={audiences.includes(x)} onChange={() => toggle(audiences, setAudiences, x)} /> {x}</label>)}</span>
          <label title="leave all unticked to allow any">Data</label>
          <span className="row">{DATA_CLASSES.map((x) => <label key={x} className="small"><input type="checkbox" checked={data.includes(x)} onChange={() => toggle(data, setData, x)} /> {x}</label>)}</span>
        </>}
      </div>
      <div className="wiz-section">Facts</div>
      <div className="form-grid" style={{ maxWidth: 'none' }}>
        {!cloud && <>
          <label>Apps domain</label>
          <span className="row"><input value={draft.domain} onChange={(e) => set('domain', e.target.value)} placeholder="apps.example.com" style={{ width: 220 }} /> <span className="muted small">apps are reached at name.domain; empty means host:port</span></span>
        </>}
        <label>Disks</label>
        <span><label className="small"><input type="checkbox" checked={draft.persistentVolumes} onChange={(e) => set('persistentVolumes', e.target.checked)} /> keeps disks (a dataDir may bind)</label></span>
        <label>Previews</label>
        <span><label className="small"><input type="checkbox" checked={draft.previews} onChange={(e) => set('previews', e.target.checked)} /> previews may bind</label></span>
        <label>Needs</label>
        <span><select value={draft.needsMode} onChange={(e) => set('needsMode', e.target.value)}>
          {NEEDS_MODES.map(({ value, title }) => <option key={value} value={value}>{title}</option>)}
        </select></span>
      </div>
      </fieldset>
      {conflict && <div role="alert">
        <p>The target settings changed. Your edits are kept.</p>
        {current ? <p>Current facts: domain {factsOf(current).domain || '(none)'}, disks {String(current.capabilities.persistentVolumes)}, previews {String(current.capabilities.previews)}, needs {current.needsMode}.</p>
          : <p>Read the current settings before choosing.</p>}
        <button disabled={!current || busy} onClick={() => resolve(true)}>Keep my edits on current settings</button>
        <button disabled={!current || busy} onClick={() => resolve(false)}>Discard my fact edits</button>
        <button disabled={busy || currentQ.refreshing} onClick={() => void currentQ.reload()}>Read current settings</button>
      </div>}
      <div className="row" style={{ gap: 8, marginTop: 12, alignItems: 'center' }}>
        <button className="primary" onClick={finish} disabled={busy || conflict}>{busy ? 'saving…' : 'Finish'}</button>
        <Link to={`/infrastructure/${target.name}`} className="small">skip for now</Link>
        {err && <span className="error small">{err}</span>}
      </div>
    </div>
  )
}
