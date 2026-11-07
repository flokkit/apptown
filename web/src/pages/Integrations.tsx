import { FeatureGate } from '../features'
import { ProjectCount } from './DependentProjects'

import { useTechnicalDetail } from './change/TechnicalDetail'
import { Section, Group } from '../Section'
import { PageHead } from '../PageHead'
import { ListRow } from '../ListRow'
import { Status } from '../Status'
import { DisplayLabelEdit } from '../DisplayLabel'
import { PRODUCT } from '../brand'
import { Fragment, type ReactNode, useState } from 'react'
import { Link, Navigate, useLocation, useNavigate, useParams } from 'react-router-dom'
import { del, post } from '../api'
import { ConnIcon, iconForBinding } from '../icons'
import { QuietConfirm } from '../QuietConfirm'
import { fmtWhen, relTime, lastSeen, fmtStamp } from '../format'
import { useAction, useGet, useRefresh } from '../data'
import { ReadStatus, StaleNote, type Gated } from '../ReadGate'
import type { ConnectorCatalog, ConnectorEntry, ConnectorPreset, IntegrationEntry, IntegrationHealth, IntegrationList, IntegrationUse } from '../wire.gen'
import { CatalogPicker, DocsModal } from './integrations/Catalog'
import { ConnectorForm, asNumber } from './integrations/ConnectorForm'
import { InstanceSecretsPanel } from './integrations/InstanceSecrets'
import { ModelProfiles } from './integrations/ModelSlots'
import { ServiceKindsPanel } from './integrations/ServiceKinds'
import { useNow } from '../hooks'
import {
  type Area, HealthState, INTEGRATION_MENU, MENU_KINDS, PAGE_KEYS, areaOf, builtInModel, kindPath, pageKinds, capabilityMeta, credentialKeys, fmtVal, healthState, specFor, usedFor,
} from './integrations/kinds'
import './Integrations.css'
import { ExecutorObservation } from './integrations/Executor'

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
// own page in the menu; their paths are reserved, as are the kinds', so an
// integration with one of these names opens the menu page.
const SECTIONS = ['profiles']

// Mounted at /integrations and /services: the two menu areas share the
// list, the kind pages and each connection's own page.
export default function Integrations() {
 const { name } = useParams()
 const feature = name && ({ model: 'models', profiles: 'models', slots: 'models', executor: 'worker', 'cloud-account': 'app-platform' } as Record<string, string>)[name]
 return feature ? <FeatureGate area={feature}><EnabledIntegrations /></FeatureGate> : <EnabledIntegrations />
}
function EnabledIntegrations() {
  const { name } = useParams()
  const area: Area = useLocation().pathname.startsWith('/services') ? 'services' : 'integrations'
  // The shared services page was /integrations/services before Services had a menu of its own.
  if (area === 'integrations' && name === 'services') return <Navigate to="/services/shared" replace />
  // Model profiles were at /integrations/slots.
  if (name === 'slots') return <Navigate to="/integrations/profiles" replace />
  if (name && SECTIONS.includes(name)) return <SectionPage which={name} />
  return <IntegrationsBook name={name} area={area} />
}

type Editing = { spec: ConnectorEntry; name: string; config: Record<string, unknown>; icon: string }

function IntegrationsBook({ name, area }: { name?: string; area: Area }) {
  const navigate = useNavigate()
  const list = useGet<IntegrationList>('/integrations')
  const catalogQ = useGet<ConnectorCatalog>('/integrations/catalog', { kind: 'config' })
  const catalog = catalogQ.data?.catalog ?? []
  const [editing, setEditing] = useState<Editing | null>(null)
  // The kinds Connect offers: all of them, or one page's.
  const [picking, setPicking] = useState<string[] | 'all' | null>(null)
  const [docsFor, setDocsFor] = useState<ConnectorEntry | null>(null)
  const refresh = useRefresh('/integrations', '/services', '/secrets')
  const now = useNow(30_000)

  const openFor = (spec: ConnectorEntry, from?: IntegrationEntry, preset?: ConnectorPreset) => {
    const src = from?.config ?? preset?.config ?? {}
    const cfg: Record<string, unknown> = {}
    for (const f of spec.fields ?? []) {
      const v = src[f.key]
      if (v === undefined) continue
      cfg[f.key] = f.type === 'command' && Array.isArray(v) ? v.join(' ')
        : f.type === 'secretRef' || f.type === 'modelList' ? (Array.isArray(v) ? v : [])
          : f.type === 'number' ? asNumber(v)
            : v ?? (f.type === 'bool' ? false : '')
    }
    const icon = from ? iconForBinding(from) : preset?.icon ?? spec.icon ?? spec.capability
    setPicking(null)
    setEditing({ spec, name: from ? (from.managed ? `${from.name}-custom` : from.name) : preset?.suggest ?? '', config: cfg, icon })
  }

  if (editing) {
    return <ConnectorForm spec={editing.spec} icon={editing.icon} initialName={editing.name} initialConfig={editing.config}
      onDone={(saved) => { setEditing(null); if (saved) { refresh(); navigate(`/${area}/${saved}`) } }} />
  }

  // Cloud accounts are added from Infrastructure, so the catalogue offered
  // here leaves them out.
  const creatable = catalog.filter((c) => c.capability !== 'cloud-account')
  const modals = <>
    {picking && <CatalogPicker catalog={picking === 'all' ? creatable : creatable.filter((c) => picking.includes(c.capability))}
      onPick={(spec, preset) => openFor(spec, undefined, preset)} onClose={() => setPicking(null)}
      onOpen={(spec) => {
        const b = list.data?.integrations?.find((x) => x.capability === spec.capability && x.provider === spec.provider)
        setPicking(null)
        navigate(b ? `/${areaOf(b.capability)}/${b.name}` : kindPath(spec.capability))
      }} />}
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

  const props: PageProps = {
    area, bindings, catalog, offered: creatable, catalogErr: catalogQ.err, reading, refreshing: list.refreshing,
    onNew: (caps) => setPicking(caps ?? 'all'),
  }
  const isPage = (k: string) => PAGE_KEYS.has(k) || MENU_KINDS.has(k)
    || bindings.some((x) => x.capability === k) || catalog.some((c) => c.capability === k)
  if (name && isPage(name)) {
    // A kind opens on the page that lists it; an older link to a kind lands there too.
    const at = PAGE_KEYS.has(name) ? `/${areaOf(name)}/${name}` : kindPath(name)
    if (at !== `/${area}/${name}`) return <Navigate to={at} replace />
    return <>{modals}<KindPage which={name} {...props} /></>
  }

  if (name) {
    const b = bindings.find((x) => x.name === name)
    if (b) {
      return <>
        {modals}
        <Detail key={b.name} b={b} catalog={catalog} reading={reading} current={list.current}
          onEdit={openFor} onDocs={setDocsFor} />
      </>
    }
    return (
        <div>
              <h1>{name}</h1>
          <div className="card">No integration is called “{name}”. It may have been removed or renamed.{' '}
            <Link to={`/${area}`}>See them all</Link>.</div>
        </div>
    )
  }

  return <>{modals}<Overview {...props} /></>
}

// What each menu page is for, behind its "?": the area's help would only
// repeat the list of pages. Each names the shipped guide that says more.
export const KIND_HELP: Record<string, { body: ReactNode; guide: string }> = {
  scm: { guide: 'quickstart', body: <>
    <p>Connect a code hosting service, such as GitLab. Each project's code is mirrored there, as a backup and to work with your other development tools.</p>
    <p>Optional: {PRODUCT} keeps each project's code itself either way.</p>
  </> },
  executor: { guide: 'runtime-environments', body: <>
    <p>Choose where {PRODUCT} does its work: agents writing code, checks, and previews (trial runs of an app). Each runs in its own container, an isolated environment that is thrown away afterwards.</p>
    <p>Required. The built-in executor uses {PRODUCT}'s own server; a worker (a second server set up for this) can take the load instead.</p>
  </> },
  model: { guide: 'architecture', body: <>
    <p>Connect the AI model accounts coding agents use, such as Anthropic, OpenAI or your own model server.</p>
    <p>Required: agents cannot work without one.</p>
  </> },
  profiles: { guide: 'architecture', body: <>
    <p>Choose which AI model does each task, such as planning, writing code or reviewing. Workflows ask for a task, not a model, so a change here applies to every workflow at once.</p>
    <p>Every project uses the standard profile unless you move it to another, for example one with stronger models for demanding projects. A task left unset uses the task it falls back to, and in the end Default's model; another profile's unset task uses standard's.</p>
  </> },
  registry: { guide: 'toolchains', body: <>
    <p>Connect a package mirror: a local copy of the libraries apps are built from, such as npm or PyPI. Builds fetch from it instead of the internet, so they are faster and less affected by outside outages.</p>
    <p>Optional.</p>
  </> },
  scanner: { guide: 'conformance', body: <>
    <p>Ready-made checks that can run on a project's code, such as looking for passwords committed by mistake or libraries with known security holes. Each project's way of working decides which ones run.</p>
  </> },
  secrets: { guide: 'secrets', body: <>
    <p>Choose where passwords, API keys and other credentials are stored, for {PRODUCT} and for apps. Values are never shown once saved.</p>
    <p>Required. The built-in store works straight away.</p>
  </> },
  llm: { guide: 'services-and-data', body: <>
    <p>Connect an AI model account that the apps your team builds can use. Each deployment and preview gets its own key with a spending limit.</p>
    <p>Optional: needed only by apps that use AI.</p>
  </> },
  errors: { guide: 'monitoring', body: <>
    <p>Connect an error tracking service, such as GlitchTip, to see when an app breaks and why. A project turns it on under Settings › Services.</p>
    <p>Optional.</p>
  </> },
  shared: { guide: 'services-and-data', body: <>
    <p>Describe services your organisation already runs, such as a sign-in system, so apps can be connected to them. A project attaches one under Settings › Services.</p>
    <p>Optional.</p>
  </> },
  deploy: { guide: 'deployment-targets', body: <>
    <p>Set up the places apps are deployed to, such as a Docker host or a cloud provider, and choose which groups may use each one.</p>
    <p>Optional: without one, apps run on {PRODUCT}'s own server. What runs on each target is under <Link to="/targets">Deployments › Targets</Link>.</p>
  </> },
  'cloud-account': { guide: 'deployment-targets', body: <>
    <p>Set up the places apps are deployed to, such as a Docker host or a cloud provider, and choose which groups may use each one.</p>
    <p>Optional: without one, apps run on {PRODUCT}'s own server. What runs on each target is under <Link to="/targets">Deployments › Targets</Link>.</p>
  </> },
}


// What the page knows about the health reading it shows.
type Reading = { q: Gated; now: number }


// uncheckedWords is what an unchecked reading means for a kind: one the
// AppTown cannot check at all, or one only Test connection asks.
export const uncheckedWords = (testable: boolean) => testable
  ? `${PRODUCT} does not check this kind of service on its own; Test connection asks it now.`
  : `${PRODUCT} has no live check for this kind of service, so whether it works is unknown. Ask an administrator to verify its configuration.`

function StatusChip({ state, stale, health, plain = false }: { state: HealthState; stale: boolean; health?: IntegrationHealth; plain?: boolean }) {
  const word = stale ? 'unknown' : !health?.checked && !health?.automatic && state !== 'not set up' ? 'no automatic check' : state === 'unknown' && !health?.checked ? 'not checked yet' : state
  // Help only where it says more than the word: why it is unknown, or what
  // a check answered. "not set up" and "no automatic check" say it already.
  const help = stale ? `Refresh failed. Last answer shown. ${health?.freshnessHelp ?? ''}`
    : state === 'unknown' && health?.lastResult ? `Last result: ${health.lastResult}. ${health.detail}`
      : plain || word === 'no automatic check' || word === 'not set up' || word === 'not checked yet' ? undefined : health?.detail || undefined
  // A failure keeps the failure colour when it is old; its age is on the checked line.
  return <Status observation word={word} old={!!health?.stale && word !== 'unknown' && word !== 'failed'} help={help} />
}

const checkedAt = (h: IntegrationHealth): number => h.checkedAt ? Date.parse(h.checkedAt) || 0 : 0

export function lastChecked(h: IntegrationHealth, now: number): string {
  if (!h.checkedAt) return ''
  return h.stale ? lastSeen(h.checkedAt, now) : `checked ${relTime(h.checkedAt, now) || 'at an unknown time'}`
}


type PageProps = {
  area: Area
  bindings: IntegrationEntry[]
  catalog: ConnectorEntry[]
  // offered: the connectors that can be connected from this page.
  offered: ConnectorEntry[]
  catalogErr: string
  reading: Reading
  refreshing: boolean
  onNew: (capabilities?: string[]) => void
}

const kindsOf = (key: string, bindings: IntegrationEntry[], offered: ConnectorEntry[]) =>
  pageKinds(key, [...bindings.map((b) => b.capability), ...offered.map((c) => c.capability)])

// Overview: every page of the menu, each with what is connected there and
// whether any of it is failing, so a problem shows before a page is opened.
// rowState is one overview row: working or a problem, from everything it
// holds that can be judged. A retired target, a connection never configured,
// or one that cannot be checked on its own (it runs inside each app, or has
// no check), says nothing either way; a check that was tried and gave no
// answer is not known, never working.
type RowState = 'problem' | 'not known' | 'working' | 'unjudged' | 'nothing'
function rowState(states: { failed: boolean; notKnown: boolean; passed: boolean }[], configured: number): RowState {
  if (configured === 0) return 'nothing'
  if (states.some((x) => x.failed)) return 'problem'
  if (states.some((x) => x.notKnown)) return 'not known'
  return states.some((x) => x.passed) ? 'working' : 'unjudged'
}
const judged = (b: IntegrationEntry) => {
  const st = healthState(b.health)
  const neutral = st === 'not set up' || (!b.health.checked && !b.health.automatic)
  return { failed: st === 'failed', notKnown: st === 'unknown' && !neutral, passed: st === 'passed', neutral, configured: st !== 'not set up' }
}

function Overview({ area, bindings, offered: all, catalogErr, reading, onNew }: PageProps) {
  const navigate = useNavigate()
  const one = INTEGRATION_MENU.find((g) => g.key === area)!
  // Connect offers only this area's kinds.
  const caps = one.items.flatMap((it) => kindsOf(it.key, bindings, all))
  const offered = all.filter((c) => caps.includes(c.capability))
  return (
    <div>
      <PageHead page={area} actions={<button className="primary" onClick={() => onNew(caps)} disabled={offered.length === 0}
        title={offered.length === 0 ? 'The connector catalogue has not been read' : undefined}>Connect a service</button>} />
      {catalogErr && <div className="error small">The connector catalogue could not be read, so connecting and editing are unavailable: {catalogErr}</div>}
      <StaleNote q={reading.q} />
      {[one].map((g) => (
        <Section key={g.heading} title="" label={g.heading}>
          <div role="list">
            {g.items.map((it) => {
              const caps = kindsOf(it.key, bindings, offered)
              const rows = bindings.filter((b) => caps.includes(b.capability))
              const failing = rows.filter((b) => healthState(b.health) === 'failed').length
              const marks = rows.map(judged)
              const counted = rowState(marks, rows.filter((b) => judged(b).configured).length)
              // A list whose refresh failed is not known now; a working row
              // built on an old reading says how old.
              const state = counted === 'working' && reading.q.refreshFailed ? 'not known' : counted
              const olds = rows.filter((b) => b.health.stale && healthState(b.health) === 'passed')
              const oldest = olds.map((b) => b.health.checkedAt ?? '').filter(Boolean).sort()[0]
              const to = `/${area}/${it.key}`
              const setting = !it.kinds && it.key !== 'shared'
              return (
                <div key={it.key} role="listitem" className="list-row-open int-row" onClick={() => navigate(to)}>
                  <ListRow className="int-list-row"
                    identity={<div style={{ minWidth: 0 }}>
                      <Link to={to} className="int-name" onClick={(e) => e.stopPropagation()}>{it.title}</Link>
                      <div className="muted small">{it.blurb}</div>
                    </div>}
                    // Working or a problem; a number only when something needs attention.
                    state={setting ? undefined
                      : state === 'problem' ? <Status word="failed" reason={`${failing} failing`} />
                        : state === 'not known' ? <Status word="unknown" />
                          : state === 'working' ? <Status observation word="fresh" staleAt={oldest} old={olds.length > 0 && !oldest} />
                            : state === 'unjudged' ? <Status word="no automatic check" />
                              : <Status word="nothing connected" />} />
                </div>
              )
            })}
          </div>
        </Section>
      ))}
    </div>
  )
}

// KindPage: one menu page — the services of one kind (or, for Shared
// services, of every kind defined at run time), with what sits beside them.
function KindPage({ which, area, bindings, catalog, offered, catalogErr, reading, refreshing, onNew }: PageProps & { which: string }) {
  const caps = kindsOf(which, bindings, offered)
  const item = INTEGRATION_MENU.flatMap((g) => g.items).find((i) => i.key === which)
  const meta = item ?? { ...capabilityMeta(which, offered.find((c) => c.capability === which)?.title), key: which }
  const rows = bindings.filter((b) => caps.includes(b.capability))
  const states = rows.map((b) => healthState(b.health))
  const failing = states.filter((s) => s === 'failed').length
  // Only a check that was tried and gave no answer is not tested; one that cannot run on its own says nothing.
  const unchecked = rows.filter((b) => judged(b).notKnown).length
  // Only what is configured is listed; Connect at the top is how to add one.
  const groups = caps.map((cap) => ({
    cap,
    rows: rows.filter((b) => b.capability === cap),
  })).filter((g) => g.rows.length > 0)
  const labelled = groups.filter((g) => g.rows.length > 0).length > 1

  return (
    <div>
      <PageHead page={area} sub={meta.title} help={KIND_HELP[which]} line={meta.blurb || undefined} actions={<button className="primary" onClick={() => onNew(caps)}>Connect</button>} />
      {catalogErr && <div className="error small">The connector catalogue could not be read, so connecting and editing are unavailable: {catalogErr}</div>}
      <StaleNote q={reading.q} />
      {/* A neutral name: each row says whether it is set up and working, so the heading never claims it. */}
      {(groups.length > 0 || which !== 'shared') && (
        <Section title="Connections"
          note={rows.length > 0 && (failing > 0 || unchecked > 0 || refreshing) ? <span style={failing > 0 ? { color: "var(--bad)" } : undefined}>
            {[failing > 0 ? `${failing} need attention` : '', unchecked > 0 ? `${unchecked} not tested` : ''].filter(Boolean).join(' · ')}
            {refreshing && !reading.q.refreshFailed ? <span className="muted"> · checking again…</span> : null}
          </span> : undefined}>
          {groups.length === 0 ? <p className="muted">No connections yet. Use Connect to add one.</p>
            : groups.map((g) => {
              const rowsEl = <KindRows area={area} rows={g.rows} catalog={catalog} reading={reading} />
              // A page holding several kinds names each one, once more than one
              // has something connected; a lone kind would only repeat the page.
              return labelled && g.rows.length > 0
                ? <div key={g.cap} className="int-kind-group"><Group label={capabilityMeta(g.cap, specFor(catalog, g.rows[0])?.title).title}>{rowsEl}</Group></div>
                : <div key={g.cap} className="int-kind-group">{rowsEl}</div>
            })}
        </Section>
      )}
      {which === 'executor' && <ExecutorObservation />}
      {which === 'model' && <p className="muted small">Which workflows use which model is set in <Link to="/integrations/profiles">Model profiles</Link>.</p>}
      {which === 'secrets' && <InstanceSecretsPanel bindings={reading.q.err ? [] : bindings} catalog={catalogErr ? [] : catalog} />}
      {which === 'shared' && <Section title="Definitions"><ServiceKindsPanel /></Section>}
    </div>
  )
}

function KindRows({ area, rows, catalog, reading }: {
  area: Area; rows: IntegrationEntry[]; catalog: ConnectorEntry[]; reading: Reading
}) {
  const navigate = useNavigate()
  // A failure leads, so it is never below the fold of a long list.
  const sorted = [...rows].sort((a, b) => Number(healthState(b.health) === 'failed') - Number(healthState(a.health) === 'failed'))
  return (
    <div role="list">
      {sorted.map((b) => {
        const state = healthState(b.health)
        const to = `/${area}/${b.name}`
        return (
          // The whole row opens the service; Fix is shown only where it differs from looking.
          <div key={b.id} role="listitem" className="list-row-open int-row" onClick={() => navigate(to)}>
            <ListRow className="int-list-row"
              identity={<div className="int-who">
                <ConnIcon name={iconForBinding(b)} size={13} />
                <div style={{ minWidth: 0 }}>
                  <Link to={to} className="int-name" onClick={(e) => e.stopPropagation()}>{b.displayLabel || b.name}</Link>
                  <div className="muted small">{usedFor(b, specFor(catalog, b))}</div>
                </div>
              </div>}
              state={<>
                <StatusChip state={state} stale={reading.q.refreshFailed} health={b.health} />
                <div className="int-when">{lastChecked(b.health, reading.now)}</div>
              </>}
              action={state === 'failed' ? <Link className="btn" to={to} onClick={(e) => e.stopPropagation()}>Fix</Link> : undefined} />
          </div>
        )
      })}
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
  const [technical] = useTechnicalDetail()
  const model = b.capability === 'model'
  const needsModels = model && builtInModel(b.name, b.config) && !(Array.isArray(b.config.models) && b.config.models.length)
  const spec = specFor(catalog, b)
  // Only a connector people can make more of can be edited or cloned here;
  // cloud accounts are looked after under Infrastructure.
  const editSpec = b.capability === 'cloud-account' ? undefined
    : catalog.find((c) => c.capability === b.capability && c.provider === b.provider && c.multi)
  const docs = catalog.find((c) => c.capability === b.capability && c.provider === b.provider && c.hasDocs)
  const state = healthState(b.health)
  // A test's answer, or why AppTown could not be asked.
  const [test, setTest] = useState<{ h?: IntegrationHealth; err?: string; at: number } | null>(null)
  const [testing, setTesting] = useState(false)
  const refreshChecks = useRefresh('/integrations', '/attention')
  const remove = useAction(() => navigate(kindPath(b.capability)), { affects: ['/services', '/secrets'] })

  const runTest = async () => {
    setTesting(true); setTest(null)
    try {
      const h = await post<IntegrationHealth>(`/integrations/${encodeURIComponent(b.name)}/test`, {})
      setTest({ h, at: checkedAt(h) || Date.now() })
      refreshChecks()
    } catch (e) { setTest({ err: (e as Error).message, at: Date.now() }) } finally { setTesting(false) }
  }

  const creds = credentialKeys(b, spec)
  // Every setting, empty ones included: a missing value is often why it is not working.
  const settings = Object.entries(b.config).filter(([k]) => !creds.includes(k))
  const modelFacts: [string, unknown][] = [
    ['Credentials', [b.config.secretEnv, b.config.apiKeyFrom].map(fmtVal).filter(Boolean).join(' · ')],
    ['Enabled models', b.config.models], ['Default model', b.config.model],
    ['Calls at once', b.config.concurrency], ['Base URL', b.config.baseUrl],
    ['API dialect', b.config.api], ['Models', b.config.modelsJson],
  ]
  const testState = test?.h ? healthState(test.h) : null
  const at = checkedAt(b.health)
  // One rule for the test button: it leads when the state asks for it.
  const needsTest = state === 'failed' || state === 'not set up'
  const message = b.health.detail || (at ? '' : uncheckedWords(b.testable))

  return (
    <div className="int-detail">
      <div className="int-detail-head">
        <ConnIcon name={iconForBinding(b)} size={22} />
        <div style={{ minWidth: 0 }} className="grow">
          <h1>{b.displayLabel || b.name}</h1>
          <div className="int-sub">{usedFor(b, spec)}</div>
        </div>
      </div>

      <div className="int-state">
        <div className="row">
          {/* The detail is written out below, so the chip carries no help. */}
          {needsModels ? <Status word="waiting for you" reason="Enable at least one model" /> : <StatusChip plain state={state} stale={reading.q.refreshFailed} health={b.health} />}
          {at > 0 && <span className="muted small">{lastChecked(b.health, reading.now)}</span>}
        </div>
        {message && <p className="int-message">{message}</p>}
        {/* A connector AppTown sets up reads its credentials from its secrets; say where to set them. */}
        {b.managed && state === 'not set up' && <p className="int-message"><Link to="/integrations/secrets">Set its secrets under Secrets →</Link></p>}
        {state === 'failed' && <p className="int-message">Check the connection settings, then test again.</p>}
        {b.health.lastGood && state !== 'passed' && <p className="muted small">Last worked {b.health.lastGood.checkedAt ? fmtWhen(b.health.lastGood.checkedAt) : 'at an unknown time'}{b.health.lastGood.detail ? `: ${b.health.lastGood.detail}` : ''}</p>}
        <StaleNote q={reading.q} />
        {test?.h && testState && (
          <div className={`int-test ${testState === 'failed' ? 'bad' : ''}`}>
            <StatusChip plain state={testState} stale={false} health={test.h} />{' '}
            {testState === 'unknown' ? '' : test.h.detail}
            <span className="muted"> · {fmtStamp(new Date(test.at).toISOString())}</span>
          </div>
        )}
        {test?.err && <div className="int-test bad">The test could not be run: {test.err}</div>}
        <div className="row int-actions">
          <button className={needsTest ? 'primary' : ''} onClick={runTest} disabled={testing}>{testing ? 'Testing…' : 'Test connection'}</button>
          {editSpec && (
            <button className={needsModels ? 'primary' : ''} onClick={() => onEdit(editSpec, b)} disabled={!current}
              title={current ? undefined : 'Waiting for the current settings'}>
              {b.managed ? 'Clone…' : 'Edit'}
            </button>
          )}
          {docs && <button className="subtle" onClick={() => onDocs(docs)}>Docs</button>}
          <span className="grow" />
          {!b.managed && b.capability !== 'cloud-account' && (
            <QuietConfirm label="Remove" ask={`Remove “${b.name}”? Past runs keep the configuration they recorded.`}
              onYes={() => remove.run(() => del(`/integrations/${b.name}`))} />
          )}
        </div>
        {remove.err && <div className="error small">{remove.err}</div>}
        {b.capability === 'cloud-account' && (
          <div className="muted int-note">Its deployment targets are under <Link to="/targets">Deployment targets</Link>.</div>
        )}
        {b.capability === 'model' && (
          <div className="muted int-note">Which workflows use which model is set in <Link to="/integrations/profiles">Model profiles</Link>.</div>
        )}
      </div>

      {/* Each run records the version and configuration hash it used. */}
      <Section title="Connection" note={b.managed ? `${PRODUCT} configures this one and re-syncs it with each release.${editSpec ? ' Clone it to change it.' : ''}` : undefined}>
        <dl className="facts int-facts">
          <dt>Name</dt>
          <dd><DisplayLabelEdit value={b.displayLabel || b.name} machineKey={b.name} path={`/integrations/${b.id}/label`} /></dd>
          {model && !technical && modelFacts.filter(([, v]) => fmtVal(v)).map(([label, v]) => (
            <Fragment key={label}><dt>{label}</dt><dd>{fmtVal(v)}</dd></Fragment>
          ))}
          {(!model || technical) && <>{creds.map((k) => (
            <Fragment key={k}><dt>{k}</dt><dd>{fmtVal(b.config[k]) || <span className="muted">not set</span>}</dd></Fragment>
          ))}
          {settings.map(([k, v]) => (
            <Fragment key={k}><dt>{k}</dt><dd>{fmtVal(v) || <span className="muted">not set</span>}</dd></Fragment>
          ))}
          <dt>Kind</dt><dd>{b.capability} · {b.provider}</dd>
          <dt>Version</dt><dd>v{b.version}{b.updatedAt ? ` · changed ${fmtWhen(b.updatedAt)}` : ''}</dd>
          <dt>Configuration hash</dt><dd><code>{b.configHash}</code></dd></>}
        </dl>
      </Section>

      {b.usedBy && <Section title="Used by"><UsedBy use={b.usedBy} capability={b.capability} name={b.name} /></Section>}
    </div>
  )
}

// Kinds AppTown itself runs on: every project uses them without naming them.
const APPTOWN_WIDE = new Set(['scm', 'executor', 'model', 'secrets', 'registry'])

// What names this integration: the projects that attach it and the standing
// deployments on targets it reaches. An AppTown-wide service is used without
// being named, so none here does not mean it is unused.
function UsedBy({ use, capability, name }: { use: IntegrationUse; capability: string; name: string }) {
  if (use.projects.length === 0 && use.deployments.length === 0) {
    // What AppTown itself runs on serves every project without being named
    // by one; what a project attaches says so plainly when nobody has yet.
    const words = APPTOWN_WIDE.has(capability) ? 'Every project.'
      : capability === 'scanner' ? "Projects whose way of working includes this check."
        : capability === 'deploy' || capability === 'cloud-account' ? 'No deployment runs through it yet.'
          : 'No project uses it yet.'
    return <p className="muted">{words}</p>
  }
  return <>
    <p><ProjectCount to={`/${areaOf(capability)}/${name}/projects`} count={new Set([...use.projects.map(p => p.slug), ...use.deployments.map(d => d.project)]).size} /></p>
  </>
}

function SectionPage({ which }: { which: string }) {
  const s = INTEGRATION_MENU.flatMap((g) => g.items).find((i) => i.key === which)!
  return <ModelProfiles title={s.title} help={KIND_HELP[which]} />
}
