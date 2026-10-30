import { PRODUCT } from '../../brand'
import type { ConnectorEntry, IntegrationEntry, IntegrationHealth, IntegrationUse } from '../../wire.gen'

// What each kind of integration is for, in the words the list groups by.
// A kind AppTown learns at run time (a service kind) takes its title from
// the connector catalogue instead.
const CAP_META: Record<string, { title: string; blurb: string }> = {
  scm: { title: 'Source control', blurb: 'Repositories, branches and merge requests' },
  secrets: { title: 'Secrets', blurb: `Where ${PRODUCT} reads credentials from` },
  executor: { title: 'Executors', blurb: 'Where workflow steps run' },
  model: { title: 'Model providers', blurb: 'The models agents call' },
  deploy: { title: 'Deploy connectors', blurb: `How ${PRODUCT} reaches the machines it deploys to` },
  'cloud-account': { title: 'Cloud accounts', blurb: 'Accounts whose deployment targets are managed under Infrastructure' },
  scanner: { title: 'Scanners', blurb: 'Checks run against a project’s code' },
  registry: { title: 'Package registries', blurb: 'Local mirrors of packages and toolchains' },
  errors: { title: 'Error reporting', blurb: 'Error reports from apps, Sentry-compatible' },
  llm: { title: 'AI models', blurb: 'Model keys for the apps people build, one per deployment and preview' },
}

// The order groups appear in; kinds not named here follow, then cloud accounts.
export const CAP_ORDER = ['scm', 'secrets', 'executor', 'model', 'deploy', 'scanner', 'registry', 'errors', 'llm']

export function capabilityMeta(capability: string, catalogTitle?: string): { title: string; blurb: string } {
  return CAP_META[capability] ?? { title: catalogTitle ?? capability, blurb: '' }
}

// The state of one integration's health reading.
//   working    AppTown checked it and it answered
//   failing    AppTown checked it and it did not
//   unchecked  AppTown has no check for this kind (checked is false), so
//              the reading says nothing either way
export type HealthState = import('../../wire.gen').HealthStatus
export function healthState(h: IntegrationHealth): HealthState { return h.stateLabel }
export { STATUS_LIGHT as HEALTH_CHIP } from '../../Status'

// What names an integration, counted for its row: "2 projects · 1
// deployment", or '' when nothing names it directly.
export function usedByCounts(u: IntegrationUse | undefined): string {
  if (!u) return ''
  const n = (k: number, one: string) => `${k} ${one}${k === 1 ? '' : 's'}`
  return [
    u.projects.length > 0 ? n(u.projects.length, 'project') : '',
    u.deployments.length > 0 ? n(u.deployments.length, 'deployment') : '',
  ].filter(Boolean).join(' · ')
}

// The catalogue entry a binding was made from, if the catalogue has one.
export const specFor = (catalog: ConnectorEntry[], b: { capability: string; provider: string }) =>
  catalog.find((c) => c.capability === b.capability && c.provider === b.provider)

// What one integration is used for: its own description when it carries one
// (a scanner), else the first sentence of its connector's description, else
// what its kind is for.
export function usedFor(b: Pick<IntegrationEntry, 'capability' | 'config'>, spec?: ConnectorEntry): string {
  const own = typeof b.config.description === 'string' ? b.config.description.trim() : ''
  if (own) return own
  const d = (spec?.description ?? '').trim()
  if (d) return (d.match(/^.*?\.(?=\s|$)/)?.[0] ?? d).trim()
  return capabilityMeta(b.capability).blurb
}

// Configuration keys that name where a value is read from rather than the
// value: a secretRef field in the connector's declaration, or the *From /
// secretEnv convention every connector's stored config uses. Only the names
// reach the browser; values never do.
export function credentialKeys(b: IntegrationEntry, spec?: ConnectorEntry): string[] {
  const declared = new Set((spec?.fields ?? []).filter((f) => f.type === 'secretRef').map((f) => f.key))
  return Object.keys(b.config).filter((k) => declared.has(k) || /From$/.test(k) || k === 'secretEnv')
}

// A config value as text: lists joined, nested objects (imported provider
// definitions) as compact JSON rather than "[object Object]".
export const fmtVal = (v: unknown): string =>
  v == null ? ''
    : Array.isArray(v) ? (v as unknown[]).join(' ')
    : typeof v === 'object' && v !== null ? JSON.stringify(v)
      : String(v)
