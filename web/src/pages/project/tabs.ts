// Project pages and the pages within each sidebar group.
export const PROJECT_TABS = ['overview', 'changes', 'deployments', 'security', 'settings', 'memory', 'release', 'releases'] as const
export const SECURITY_TABS = ['findings', 'suppressions'] as const
export const SETTINGS_TABS = ['general', 'working', 'services', 'deployments', 'secrets', 'members', 'usage'] as const
export const SETTINGS_LABEL: Record<string, string> = {
  general: 'General', working: 'Way of working', services: 'Services', deployments: 'Deployments', secrets: 'Secrets',
  members: 'Members', usage: 'Usage',
}
// Old settings sub-tabs → where they live now.
export const OLD_SETTINGS: Record<string, string> = {
  workflows: 'working', guidance: 'working', integrations: 'services',
}

export const MEMORY_TABS = ['notes', 'decisions'] as const
export type MemoryTab = typeof MEMORY_TABS[number]

export type ProjectTab = typeof PROJECT_TABS[number]
export type SecurityTab = typeof SECURITY_TABS[number]
export type SettingsTab = typeof SETTINGS_TABS[number]

// From the URL's /projects/:slug/:tab/:stab to the tabs actually shown:
// unknown tabs land on the overview, the old /usage tab lands on Settings ›
// Usage, and old settings sub-tabs land where they moved.
export function resolveProjectTabs(rawTab: string | undefined, rawStab: string | undefined): {
  tab: ProjectTab; secTab: SecurityTab; setTab: SettingsTab; memTab: MemoryTab
} {
  const tab = (PROJECT_TABS as readonly string[]).includes(rawTab ?? '') ? rawTab as ProjectTab
    : rawTab === 'usage' ? 'settings' : rawTab === 'board' ? 'changes' : 'overview'
  const secTab = (SECURITY_TABS as readonly string[]).includes(rawStab ?? '') ? rawStab as SecurityTab : 'findings'
  const setTab = rawTab === 'usage' ? 'usage'
    : (SETTINGS_TABS as readonly string[]).includes(rawStab ?? '') ? rawStab as SettingsTab
    : (OLD_SETTINGS[rawStab ?? ''] as SettingsTab | undefined) ?? 'general'
  const memTab = (MEMORY_TABS as readonly string[]).includes(rawStab ?? '') ? rawStab as MemoryTab : 'notes'
  return { tab, secTab, setTab, memTab }
}
