// The project's shape (design/subsystems/ui-coherence.md): Overview · Chat · Security ·
// Settings. Board retired (Overview tells the same story), the tier map
// retired (the Open button and header chips carry it), Usage moved into
// Settings. Old links still land somewhere sensible.
export const PROJECT_TABS = ['overview', 'security', 'settings', 'release', 'releases'] as const
export const SECURITY_TABS = ['findings', 'suppressions'] as const
export const SETTINGS_TABS = ['general', 'working', 'services', 'deployments', 'secrets', 'members', 'decisions', 'usage'] as const
export const SETTINGS_LABEL: Record<string, string> = {
  general: 'General', working: 'Way of working', services: 'Services', deployments: 'Deployments', secrets: 'Secrets',
  members: 'Members', decisions: 'Decisions', usage: 'Usage',
}
// Old settings sub-tabs → where they live now.
export const OLD_SETTINGS: Record<string, string> = {
  workflows: 'working', guidance: 'working', integrations: 'services', adrs: 'decisions',
}

export type ProjectTab = typeof PROJECT_TABS[number]
export type SecurityTab = typeof SECURITY_TABS[number]
export type SettingsTab = typeof SETTINGS_TABS[number]

// From the URL's /projects/:slug/:tab/:stab to the tabs actually shown:
// unknown tabs land on the overview, the old /usage tab lands on Settings ›
// Usage, and old settings sub-tabs land where they moved.
export function resolveProjectTabs(rawTab: string | undefined, rawStab: string | undefined): {
  tab: ProjectTab; secTab: SecurityTab; setTab: SettingsTab
} {
  const tab = (PROJECT_TABS as readonly string[]).includes(rawTab ?? '') ? rawTab as ProjectTab
    : rawTab === 'usage' ? 'settings' : 'overview'
  const secTab = (SECURITY_TABS as readonly string[]).includes(rawStab ?? '') ? rawStab as SecurityTab : 'findings'
  const setTab = rawTab === 'usage' ? 'usage'
    : (SETTINGS_TABS as readonly string[]).includes(rawStab ?? '') ? rawStab as SettingsTab
    : (OLD_SETTINGS[rawStab ?? ''] as SettingsTab | undefined) ?? 'general'
  return { tab, secTab, setTab }
}
