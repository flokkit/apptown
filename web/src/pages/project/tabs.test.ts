import { describe, expect, it } from 'vitest'
import { resolveProjectTabs, SETTINGS_TABS } from './tabs'

// Project navigation keeps each page in its owning group.
describe('resolveProjectTabs', () => {
  it('defaults to the overview, findings and general', () => {
    expect(resolveProjectTabs(undefined, undefined)).toEqual({ tab: 'overview', secTab: 'findings', setTab: 'general', memTab: 'notes' })
    expect(resolveProjectTabs('bogus', 'whatever')).toEqual({ tab: 'overview', secTab: 'findings', setTab: 'general', memTab: 'notes' })
  })

  it('sends the old /usage tab to Settings › Usage', () => {
    expect(resolveProjectTabs('usage', undefined)).toMatchObject({ tab: 'settings', setTab: 'usage' })
  })

  it.each([
    ['workflows', 'working'],
    ['guidance', 'working'],
    ['integrations', 'services'],
  ])('sends the old settings sub-tab %s to %s', (old, now) => {
    expect(resolveProjectTabs('settings', old).setTab).toBe(now)
  })

  it.each(['general', 'working', 'models', 'services', 'deployments', 'secrets', 'members', 'usage'])('keeps the settings tab %s', (t) => {
    expect(resolveProjectTabs('settings', t)).toMatchObject({ tab: 'settings', setTab: t })
  })

  it('reads the security sub-tabs', () => {
    expect(resolveProjectTabs('security', 'suppressions')).toMatchObject({ tab: 'security', secTab: 'suppressions' })
    expect(resolveProjectTabs('security', undefined).secTab).toBe('findings')
  })

  it('treats a release number as the releases tab, not a settings tab', () => {
    expect(resolveProjectTabs('releases', '12')).toEqual({ tab: 'releases', secTab: 'findings', setTab: 'general', memTab: 'notes' })
    expect(resolveProjectTabs('release', undefined).tab).toBe('release')
  })
})

it('opens the project change and deployment lists, including old board links', () => {
 expect(resolveProjectTabs('changes', undefined).tab).toBe('changes')
 expect(resolveProjectTabs('deployments', undefined).tab).toBe('deployments')
 expect(resolveProjectTabs('board', undefined).tab).toBe('changes')
})

it('places notes and decisions only in Memory', () => {
  expect(resolveProjectTabs('memory', 'notes')).toMatchObject({ tab: 'memory', memTab: 'notes' })
  expect(resolveProjectTabs('memory', 'decisions')).toMatchObject({ tab: 'memory', memTab: 'decisions' })
  expect(SETTINGS_TABS).not.toContain('decisions')
  expect(resolveProjectTabs('settings', 'decisions').setTab).not.toBe('decisions')
  expect(resolveProjectTabs('settings', 'adrs').setTab).not.toBe('decisions')
})
