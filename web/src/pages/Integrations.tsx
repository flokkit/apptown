import { DisplayLabelEdit } from '../DisplayLabel'
import { PRODUCT } from '../brand'
import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { del, post } from '../api'
import { ConnIcon, iconForBinding } from '../icons'
import { QuietConfirm } from '../QuietConfirm'
import { fmtWhen, relTime } from '../format'
import { useAction, useGet, useRefresh } from '../data'
import { ReadStatus, StaleNote, type Gated } from '../ReadGate'
import type { ConnectorCatalog, ConnectorEntry, ConnectorPreset, IntegrationEntry, IntegrationHealth, IntegrationList, IntegrationUse } from '../wire.gen'
import { CatalogPicker, DocsModal } from './integrations/Catalog'
import { ConnectorForm, asNumber } from './integrations/ConnectorForm'
import { InstanceSecretsPanel } from './integrations/InstanceSecrets'
import { ModelSlotsPanel } from './integrations/ModelSlots'
import { ServiceKindsPanel } from './integrations/ServiceKinds'
import { TargetsPanel } from './integrations/Targets'
import { useNow } from '../hooks'
import {
  CAP_ORDER, HEALTH_CHIP, HEALTH_WORDS, HealthState, capabilityMeta, credentialKeys, fmtVal, healthState, specFor, usedByCounts, usedFor,
} from './integrations/kinds'

// Integrations: the outside services this AppTown works with. The list says,
// for each, what it is, what it is for and whether it is working right now;
// opening one (/integrations/<name>) gives its own page with the same shape
// every time — what it is for, its state and the next thing to do, then its
// credentials, configuration and record.
//
// Health is read live: AppTown checks each service as it answers the
// list and stamps each reading with its own clock (checkedAt). A kind with
// no check says so (checked false); it is never shown as working.

// Settings that sit beside the services rather than being one. Each has its
// own page; their paths are reserved, so an integration with one of these
// names is shown in the list but opens the setting.
const SECTIONS: Record<string, { title: string; blurb: string }> = {
  slots: { title: 'Model slots', blurb: 'Map the model names workflows ask for to real models, per project profile.' },
  targets: { title: 'Deployment targets', blurb: 'Deployment targets and who they are offered to.' },
  services: { title: 'Service kinds', blurb: 'Organisational services described in YAML, which projects can attach.' },
  secrets: { title: 'Secrets', blurb: `Credentials ${PRODUCT} itself uses. Values are never shown.` },
}

export default function Integrations() {
  const { name } = useParams()
  if (name && name in SECTIONS) return <SectionPage which={name} />
  return <IntegrationsBook name={name} />
}

type Editing = { spec: ConnectorEntry; name: string; config: Record<string, unknown>; icon: string }

function IntegrationsBook({ name }: { name?: string }) {
  const navigate = useNavigate()
  const list = useGet<IntegrationList>('/integrations')
  const catalogQ = useGet<ConnectorCatalog>('/integrations/catalog', { kind: 'config' })
  const catalog = catalogQ.data?.catalog ?? []
  const [editing, setEditing] = useState<Editing | null>(null)
  const [picking, setPicking] = useState(false)
  const [docsFor, setDocsFor] = useState<ConnectorEntry | null>(null)
  const refresh = useRefresh('/integrations', '/services', '/secrets')
  const now = useNow(30_000)

  const openFor = (spec: ConnectorEntry, from?: IntegrationEntry, preset?: ConnectorPreset) => {
    const src = from?.config ?? preset?.config ?? {}
    const cfg: Record<string, unknown> = {}
    for (const f of spec.fields ?? []) {
      const v = src[f.key]
      cfg[f.key] = f.type === 'command' && Array.isArray(v) ? v.join(' ')
        : f.type === 'secretRef' || f.type === 'modelList' ? (Array.isArray(v) ? v : [])
          : f.type === 'number' ? asNumber(v)
            : v ?? (f.type === 'bool' ? false : '')
    }
    const icon = from ? iconForBinding(from) : preset?.icon ?? spec.icon ?? spec.capability
    setPicking(false)
    setEditing({ spec, name: from ? (from.managed ? `${from.name}-custom` : from.name) : preset?.suggest ?? '', config: cfg, icon })
  }

  if (editing) {
    return <ConnectorForm spec={editing.spec} icon={editing.icon} initialName={editing.name} initialConfig={editing.config}
      onDone={(saved) => { setEditing(null); if (saved) { refresh(); navigate(`/integrations/${saved}`) } }} />
  }

  // Cloud accounts are added from Infrastructure, so the catalogue offered
  // here leaves them out.
  const creatable = catalog.filter((c) => c.capability !== 'cloud-account')
  const modals = <>
    {picking && <CatalogPicker catalog={creatable} onPick={(spec, preset) => openFor(spec, undefined, preset)} onClose={() => setPicking(false)} />}
    {docsFor && <DocsModal spec={docsFor} onClose={() => setDocsFor(null)} />}
  </>

  if (!list.data) {
    return (
      <div>
        <h1>Integrations</h1>
        <ReadStatus q={list} what="the integrations" />
      </div>
    )
  }
  // The server sends no list (null) when nothing is connected.
  const bindings = list.data.integrations ?? []
  const reading: Reading = { q: list, now }

  if (name) {
    const b = bindings.find((x) => x.name === name)
    if (b) {
      return <>
        {modals}
        <Detail key={b.name} b={b} catalog={catalog} reading={reading} current={list.current}
          onEdit={openFor} onDocs={setDocsFor} />
      </>
    }
    if (!bindings.some((x) => x.capability === name) && !catalog.some((c) => c.capability === name)) {
      return (
        <div>
          <div className="muted small int-crumb"><Link to="/integrations">Integrations</Link> › {name}</div>
          <h1>{name}</h1>
          <div className="card">No integration is called “{name}”. It may have been removed or renamed.{' '}
            <Link to="/integrations">See them all</Link>.</div>
        </div>
      )
    }
    // An older link to a kind (/integrations/scanner) opens the list at it.
  }

  return <>
    {modals}
    <ListPage bindings={bindings} catalog={catalog} offered={creatable} catalogErr={catalogQ.err} reading={reading} refreshing={list.refreshing}
      focus={name} onNew={() => setPicking(true)} onConnect={(spec) => openFor(spec, undefined, spec.presets?.[0])} />
  </>
}

// What the page knows about the health reading it shows.
type Reading = { q: Gated; now: number }


const ago = (at: number, now: number) => relTime(new Date(at).toISOString(), now) || 'just now'
const clock = (at: number) => new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })

// uncheckedWords is what an unchecked reading means for a kind: one the
// AppTown cannot check at all, or one only Test connection asks.
export const uncheckedWords = (testable: boolean) => testable
  ? `${PRODUCT} does not check this kind of service on its own; Test connection asks it now.`
  : `${PRODUCT} has no live check for this kind of service, so whether it works is unknown.`

function StatusChip({ state, stale, testable }: { state: HealthState; stale: boolean; testable: boolean }) {
  if (stale && state !== 'unchecked') {
    return <span className="chip stale" title={`${PRODUCT} could not check again; this is the last answer it gave.`}>{HEALTH_WORDS[state]} at last check</span>
  }
  return <span className={`chip ${HEALTH_CHIP[state]}`}
    title={state === 'unchecked' ? uncheckedWords(testable) : undefined}>{HEALTH_WORDS[state]}</span>
}

// When AppTown took a reading, by the server's clock; 0 for none.
const checkedAt = (h: IntegrationHealth): number => (h.checked && h.checkedAt ? Date.parse(h.checkedAt) || 0 : 0)

function lastChecked(h: IntegrationHealth, now: number): string {
  const at = checkedAt(h)
  return at ? ago(at, now) : 'no check'
}

const kindOf = (b: IntegrationEntry, spec?: ConnectorEntry) => spec?.title ?? `${b.capability} · ${b.provider}`

function ListPage({ bindings, catalog, offered, catalogErr, reading, refreshing, focus, onNew, onConnect }: {
  bindings: IntegrationEntry[]
  catalog: ConnectorEntry[]
  // offered: the connectors that can be connected from this page.
  offered: ConnectorEntry[]
  catalogErr: string
  reading: Reading
  refreshing: boolean
  focus?: string
  onNew: () => void
  onConnect: (spec: ConnectorEntry) => void
}) {
  const navigate = useNavigate()
  useEffect(() => {
    if (focus) document.getElementById(`kind-${focus}`)?.scrollIntoView({ block: 'start' })
  }, [focus])

  const byCap = new Map<string, IntegrationEntry[]>()
  for (const b of bindings) byCap.set(b.capability, [...(byCap.get(b.capability) ?? []), b])
  // A kind the catalogue offers with nothing connected yet shows as one
  // "not connected" row, so it can be connected from where it would be.
  const unbound = new Map<string, ConnectorEntry>()
  for (const c of offered) {
    if (c.multi && !c.managed && !byCap.has(c.capability) && !unbound.has(c.capability)) unbound.set(c.capability, c)
  }
  const all = [...byCap.keys(), ...unbound.keys()]
  const caps = [
    ...CAP_ORDER.filter((c) => all.includes(c)),
    ...all.filter((c) => !CAP_ORDER.includes(c) && c !== 'cloud-account'),
    ...all.filter((c) => c === 'cloud-account'),
  ]

  const states = bindings.map((b) => healthState(b.health))
  const newest = Math.max(0, ...bindings.map((b) => checkedAt(b.health)))
  const failing = states.filter((s) => s === 'failing').length
  const unchecked = states.filter((s) => s === 'unchecked').length
  const summary = [
    `${bindings.length} connected`,
    failing > 0 ? `${failing} not working` : '',
    unchecked > 0 ? `${unchecked} with no live check` : '',
  ].filter(Boolean).join(' · ')

  return (
    <div>
      <div className="int-pagehead">
        <div>
          <h1>Integrations</h1>
          <div className="int-sub">Outside services {PRODUCT} connects to.</div>
        </div>
        <button className="primary btn-big" onClick={onNew} disabled={offered.length === 0}
          title={offered.length === 0 ? 'The connector catalogue has not been read' : undefined}>Connect a service</button>
      </div>

      {catalogErr && <div className="error small">The connector catalogue could not be read, so connecting and editing are unavailable: {catalogErr}</div>}
      <StaleNote q={reading.q} />
      {bindings.length > 0 && (
        <div className={`int-summary ${failing > 0 ? 'bad' : ''}`}>
          {summary}{refreshing && !reading.q.refreshFailed ? <span className="muted"> · checking again…</span> : null}
          {!reading.q.refreshFailed && newest > 0 && <span className="muted"> · checked {ago(newest, reading.now)}</span>}
        </div>
      )}

      {caps.length === 0 ? (
        <div className="card">
          <div className="int-lead">No services are connected yet.</div>
          <div className="muted">Connect source control, a model provider or a scanner to start.</div>
        </div>
      ) : (
        <section className="int-sheet">
          <table className="int-table stack">
            <thead>
              <tr><th>Service</th><th>Used for</th><th>Used by</th><th>Status</th><th>Last checked</th><th><span className="sr-only">open</span></th></tr>
            </thead>
            {caps.map((cap) => {
              const rows = byCap.get(cap) ?? []
              const spare = unbound.get(cap)
              const meta = capabilityMeta(cap, (rows[0] && specFor(catalog, rows[0])?.title) ?? spare?.title)
              return (
                <tbody key={cap} id={`kind-${cap}`} className={focus === cap ? 'int-focus' : undefined}>
                  <tr className="int-group">
                    <th colSpan={6}>{meta.title}{meta.blurb && <span className="int-group-blurb">{meta.blurb}</span>}</th>
                  </tr>
                  {rows.map((b) => {
                    const spec = specFor(catalog, b)
                    const state = healthState(b.health)
                    const to = `/integrations/${b.name}`
                    return (
                      <tr key={b.id} className="int-row" onClick={() => navigate(to)}>
                        <td className="stack-lead">
                          <div className="int-who">
                            <ConnIcon name={iconForBinding(b)} size={13} />
                            <div style={{ minWidth: 0 }}>
                              <Link to={to} className="int-name tap-open" onClick={(e) => e.stopPropagation()}>{b.displayLabel || b.name}</Link>
                              <div className="int-kind">{kindOf(b, spec)}{b.managed ? ' · managed' : ''}</div>
                            </div>
                          </div>
                        </td>
                        <td className="int-used">{usedFor(b, spec)}</td>
                        <td className="int-usedby" data-label="used by">{usedByCounts(b.usedBy) || '—'}</td>
                        <td><StatusChip state={state} stale={reading.q.refreshFailed} testable={b.testable} /></td>
                        <td className="int-when">{lastChecked(b.health, reading.now)}</td>
                        <td className="int-open"><Link to={to} onClick={(e) => e.stopPropagation()}>Open</Link></td>
                      </tr>
                    )
                  })}
                  {rows.length === 0 && spare && (
                    <tr className="int-row int-unbound">
                      <td className="stack-lead">
                        <div className="int-who">
                          <ConnIcon name={spare.icon ?? spare.capability} size={13} />
                          <div><span className="int-name">{spare.title}</span><div className="int-kind">nothing connected</div></div>
                        </div>
                      </td>
                      <td className="int-used">{usedFor({ capability: cap, config: {} }, spare)}</td>
                      <td className="int-usedby" data-label="used by">—</td>
                      <td><span className="chip">Not connected</span></td>
                      <td className="int-when">—</td>
                      <td className="int-open"><button className="subtle" onClick={() => onConnect(spare)}>Connect</button></td>
                    </tr>
                  )}
                </tbody>
              )
            })}
          </table>
        </section>
      )}

      <h2>Related settings</h2>
      <div className="int-related">
        {Object.entries(SECTIONS).map(([k, s]) => (
          <Link key={k} to={`/integrations/${k}`} className="int-related-item">
            <span className="int-name">{s.title}</span>
            <span className="muted">{s.blurb}</span>
          </Link>
        ))}
      </div>
    </div>
  )
}

function Detail({ b, catalog, reading, current, onEdit, onDocs }: {
  b: IntegrationEntry
  catalog: ConnectorEntry[]
  reading: Reading
  current: boolean
  onEdit: (spec: ConnectorEntry, from: IntegrationEntry) => void
  onDocs: (spec: ConnectorEntry) => void
}) {
  const navigate = useNavigate()
  const spec = specFor(catalog, b)
  // Only a connector people can make more of can be edited or cloned here;
  // cloud accounts are looked after under Infrastructure.
  const editSpec = b.capability === 'cloud-account' ? undefined
    : catalog.find((c) => c.capability === b.capability && c.provider === b.provider && c.multi)
  const docs = catalog.find((c) => c.capability === b.capability && c.provider === b.provider && c.hasDocs)
  const meta = capabilityMeta(b.capability, spec?.title)
  const state = healthState(b.health)
  // A test's answer, or why AppTown could not be asked.
  const [test, setTest] = useState<{ h?: IntegrationHealth; err?: string; at: number } | null>(null)
  const [testing, setTesting] = useState(false)
  const remove = useAction(() => navigate('/integrations'), { affects: ['/services', '/secrets'] })

  const runTest = async () => {
    setTesting(true); setTest(null)
    try {
      const h = await post<IntegrationHealth>('/integrations/test', { capability: b.capability, provider: b.provider, config: b.config })
      setTest({ h, at: checkedAt(h) || Date.now() })
    } catch (e) { setTest({ err: (e as Error).message, at: Date.now() }) } finally { setTesting(false) }
  }

  const creds = credentialKeys(b, spec)
  // Every setting, empty ones included: a missing value is often why it is not working.
  const settings = Object.entries(b.config).filter(([k]) => !creds.includes(k))
  const tone = reading.q.refreshFailed ? 'unknown' : state === 'working' ? 'ok' : state === 'failing' ? 'bad' : 'unknown'
  const testState = test?.h ? healthState(test.h) : null
  const at = checkedAt(b.health)

  return (
    <div className="int-detail">
      <div className="muted small int-crumb">
        <Link to="/integrations">Integrations</Link> › <Link to={`/integrations/${b.capability}`}>{meta.title}</Link>
      </div>
      <div className="int-detail-head">
        <ConnIcon name={iconForBinding(b)} size={22} />
        <div style={{ minWidth: 0, flex: 1 }}>
          <h1>{b.displayLabel || b.name}</h1>
 <DisplayLabelEdit value={b.displayLabel || b.name} machineKey={b.name} path={`/integrations/${b.id}/label`} />
          <div className="int-sub">{kindOf(b, spec)} · {usedFor(b, spec)}</div>
        </div>
        {b.managed && <span className="chip" title={`Configured by ${PRODUCT}; it re-syncs with each release.`}>managed by {PRODUCT}</span>}
      </div>

      <div className={`card tone-${tone} int-state`}>
        <div className="row">
          <StatusChip state={state} stale={reading.q.refreshFailed} testable={b.testable} />
          <span className="int-lead">
            {state === 'unchecked' ? uncheckedWords(b.testable) : b.health.detail}
          </span>
        </div>
        <div className="muted int-checked">
          {state === 'unchecked' || !at
            ? (b.testable ? 'Its answer is shown here; nothing is saved.' : 'This kind has no test either.')
            : `Checked by ${PRODUCT} ${ago(at, reading.now)} (${clock(at)}).`}
        </div>
        <StaleNote q={reading.q} />
        {test?.h && testState && (
          <div className={`int-test ${testState === 'failing' ? 'bad' : ''}`}>
            <span className={`chip ${HEALTH_CHIP[testState]}`}>
              {testState === 'working' ? 'Test passed' : testState === 'failing' ? 'Test failed' : 'No test for this kind'}
            </span>{' '}
            {testState === 'unchecked' ? '' : test.h.detail}
            <span className="muted"> · {clock(test.at)}</span>
          </div>
        )}
        {test?.err && <div className="int-test bad">The test could not be run: {test.err}</div>}
        <div className="row int-actions">
          <button className={state === 'working' ? '' : 'primary'} onClick={runTest} disabled={testing}>{testing ? 'Testing…' : 'Test connection'}</button>
          {editSpec && (
            <button onClick={() => onEdit(editSpec, b)} disabled={!current}
              title={current ? undefined : 'Waiting for the current settings'}>
              {b.managed ? 'Clone…' : 'Edit'}
            </button>
          )}
          {docs && <button className="subtle" onClick={() => onDocs(docs)}>Docs</button>}
          <span style={{ flex: 1 }} />
          {!b.managed && b.capability !== 'cloud-account' && (
            <QuietConfirm label="Remove" ask={`Remove “${b.name}”? Past runs keep the configuration they recorded.`}
              onYes={() => remove.run(() => del(`/integrations/${b.name}`))} />
          )}
        </div>
        {remove.err && <div className="error small">{remove.err}</div>}
      </div>

      {b.capability === 'cloud-account' && (
        <div className="muted int-note">Its deployment targets are managed under <Link to="/infrastructure">Infrastructure</Link>.</div>
      )}
      {b.capability === 'model' && (
        <div className="muted int-note">Which workflows use which model is set in <Link to="/integrations/slots">Model slots</Link>.</div>
      )}

      {b.usedBy && <UsedBy use={b.usedBy} />}

      {creds.length > 0 && (
        <section>
          <h2>Credentials</h2>
          <p className="muted int-section-note">Read from the secret store by name. The values never leave {PRODUCT}.</p>
          <table className="kv">
            <tbody>
              {creds.map((k) => (
                <tr key={k}>
                  <td className="kv-key">{k}</td>
                  <td className="kv-val int-wrap">{fmtVal(b.config[k]) || <span className="muted">not set</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      )}

      <section>
        <h2>Configuration</h2>
        {settings.length === 0 ? <div className="muted">No other settings.</div> : (
          <table className="kv">
            <tbody>
              {settings.map(([k, v]) => (
                <tr key={k}>
                  <td className="kv-key">{k}</td>
                  <td className="kv-val int-wrap">{fmtVal(v) || <span className="muted">not set</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <section>
        <h2>Record</h2>
        <table className="kv">
          <tbody>
            <tr><td className="kv-key">version</td><td className="kv-val">v{b.version}</td></tr>
            <tr><td className="kv-key">last changed</td><td className="kv-val">{b.updatedAt ? fmtWhen(b.updatedAt) : '—'}</td></tr>
            <tr><td className="kv-key">configuration hash</td><td className="kv-val int-wrap">{b.configHash}</td></tr>
            <tr><td className="kv-key">kind</td><td className="kv-val">{b.capability} · {b.provider}</td></tr>
          </tbody>
        </table>
        <p className="muted int-section-note">Each run records the version and configuration hash it used.
          {b.managed ? ` ${PRODUCT} configures this one itself and re-syncs it with each release; clone it to change it.` : ''}</p>
      </section>
    </div>
  )
}

// What names this integration: the projects that attach it and the standing
// deployments on targets it reaches. An AppTown-wide service is used without
// being named, so none here does not mean it is unused.
function UsedBy({ use }: { use: IntegrationUse }) {
  const none = use.projects.length === 0 && use.deployments.length === 0
  return (
    <section>
      <h2>Used by</h2>
      {none ? (
        <p className="muted int-section-note">No project attaches it and no deployment runs on a target it reaches.
          A service the whole of {PRODUCT} uses, such as source control, is not attached by projects.</p>
      ) : <>
        {use.projects.length > 0 && <>
          <div className="int-section-note">Projects that attach it</div>
          <ul className="int-uses">
            {use.projects.map((p) => (
              <li key={p.slug}><Link to={`/projects/${p.slug}`}>{p.name}</Link> <span className="muted">({p.slug})</span></li>
            ))}
          </ul>
        </>}
        {use.deployments.length > 0 && <>
          <div className="int-section-note">Deployments on targets it reaches</div>
          <ul className="int-uses">
            {use.deployments.map((d) => (
              <li key={`${d.project}/${d.name}`}>
                <Link to={`/projects/${d.project}/settings/deployments`}>{d.project}/{d.name}</Link>{' '}
                <span className="muted">on <Link to={`/infrastructure/${d.target}`}>{d.target}</Link></span>
              </li>
            ))}
          </ul>
        </>}
      </>}
    </section>
  )
}

function SectionPage({ which }: { which: string }) {
  const s = SECTIONS[which]
  return (
    <div>
      <div className="muted small int-crumb"><Link to="/integrations">Integrations</Link> › {s.title}</div>
      <h1>{s.title}</h1>
      {which !== 'targets' && <div className="int-sub" style={{ marginBottom: 16 }}>{s.blurb}</div>}
      {which === 'slots' ? <ModelSlotsPanel /> : which === 'targets' ? <TargetsPanel /> : which === 'secrets' ? <InstanceSecretsPanel /> : <ServiceKindsPanel />}
    </div>
  )
}
