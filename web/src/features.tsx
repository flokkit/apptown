import { resolveProjectTabs, type ProjectTab, type SettingsTab, type SecurityTab, type MemoryTab } from './pages/project/tabs'
import type { ReactNode } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useGet } from './data'
import { useHolds } from './instancePages'
import { ReadStatus, StaleNote } from './ReadGate'
import type { Feature } from './wire.gen'

export function useFeatures() {
  return useGet<Feature[]>('/features', { poll: 5000 })
}
export function useFeature() {
  const q = useFeatures()
  return (area: string) => q.data?.find(f => f.area === area)?.enabled === true && (!['conformance-estate', 'what-ifs'].includes(area) || q.data?.find(f => f.area === 'conformance')?.enabled === true)
}
export function FeatureGate({ area, children }: { area: string; children: ReactNode }) {
  const q = useFeatures()
  const mayManage = useHolds('identity:manage-auth')
  if (!q.data) return <ReadStatus q={q} what="Features" />
  if (!q.current) return <><StaleNote q={q} /><p role="status">{q.refreshFailed ? 'Features could not be refreshed. Reload before opening this area.' : 'Checking Features…'}</p></>
  const f = q.data.find(f => f.area === (['conformance-estate', 'what-ifs'].includes(area) && !q.data?.find(f => f.area === 'conformance')?.enabled ? 'conformance' : area))
  if (!f) return <p className="error">This page has no feature area.</p>
  if (!f.enabled) return <section><h1>{f.name}</h1><p role="status">{f.name} is turned off; an administrator can turn it on in {mayManage ? <Link to="/features">Features</Link> : 'Features'}</p></section>
  return <>{f.tier === 'beta' && <span className="badge">beta</span>}{children}</>
}

// These records make adding a project tab without an area a type error.
const projectAreas: Record<ProjectTab, string> = { overview: 'projects', changes: 'changes', deployments: 'deployments', security: 'record', settings: 'projects', memory: 'memory', releases: 'releases' }
export const settingsAreas: Record<SettingsTab, string> = { general: 'projects', working: 'ways', models: 'models', services: 'projects', secrets: 'projects', members: 'people', usage: 'usage' }
const securityAreas: Record<SecurityTab, string> = { findings: 'record', suppressions: 'record' }
const memoryAreas: Record<MemoryTab, string> = { notes: 'memory', decisions: 'record' }

export function ProjectFeatureGate({ children }: { children: ReactNode }) {
  const { tab: rawTab, stab: rawStab } = useParams()
  const { tab, setTab, secTab, memTab } = resolveProjectTabs(rawTab, rawStab)
  const area = tab === 'settings' ? settingsAreas[setTab] : tab === 'memory' ? memoryAreas[memTab] : tab === 'security' ? securityAreas[secTab] : projectAreas[tab]
  return ['projects', 'changes', 'deployments', 'releases', 'record', 'people'].includes(area) ? <CoreArea area={area}>{children}</CoreArea> : <FeatureGate area={area}>{children}</FeatureGate>
}

export function FeatureOnly({ area, children }: { area: string; children: ReactNode }) {
  const enabled = useFeature()
  return enabled(area) ? <>{children}</> : null
}
export const targetFeatureArea = (kind: string) => ({ 'do-app': 'app-platform', portainer: 'portainer', 's3-bucket': 'bucket-site' } as Record<string, string>)[kind] ?? 'deployments'

// Core route declarations name their area for completeness; they never wait
// for a switch read. The server refuses any attempt to switch these areas off.
export function CoreArea({ children }: { area: string; children: ReactNode }) {
 return <>{children}</>
}
