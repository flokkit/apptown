import { describe, expect, it } from 'vitest'
import { resolveProjectTabs } from './tabs'

// Old links land somewhere sensible (design/subsystems/ui-coherence.md): every URL
// the project page ever had still opens the right tab.
describe('resolveProjectTabs', () => {
  it('defaults to the overview, findings and general', () => {
    expect(resolveProjectTabs(undefined, undefined)).toEqual({ tab: 'overview', secTab: 'findings', setTab: 'general' })
    expect(resolveProjectTabs('bogus', 'whatever')).toEqual({ tab: 'overview', secTab: 'findings', setTab: 'general' })
  })

  it('sends the old /usage tab to Settings › Usage', () => {
    expect(resolveProjectTabs('usage', undefined)).toMatchObject({ tab: 'settings', setTab: 'usage' })
  })

  it.each([
    ['workflows', 'working'],
    ['guidance', 'working'],
    ['integrations', 'services'],
    ['adrs', 'decisions'],
  ])('sends the old settings sub-tab %s to %s', (old, now) => {
    expect(resolveProjectTabs('settings', old).setTab).toBe(now)
  })

  it.each(['general', 'working', 'services', 'deployments', 'secrets', 'members', 'decisions', 'usage'])('keeps the settings tab %s', (t) => {
    expect(resolveProjectTabs('settings', t)).toMatchObject({ tab: 'settings', setTab: t })
  })

  it('reads the security sub-tabs', () => {
    expect(resolveProjectTabs('security', 'suppressions')).toMatchObject({ tab: 'security', secTab: 'suppressions' })
    expect(resolveProjectTabs('security', undefined).secTab).toBe('findings')
  })

  it('treats a release number as the releases tab, not a settings tab', () => {
    expect(resolveProjectTabs('releases', '12')).toEqual({ tab: 'releases', secTab: 'findings', setTab: 'general' })
    expect(resolveProjectTabs('release', undefined).tab).toBe('release')
  })
})
