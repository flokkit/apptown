import { expect, it } from 'vitest'
import source from './App.tsx?raw'
import { SETTINGS_TABS } from './pages/project/tabs'

it('has one literal route for each deployment page and one target page', () => {
  const paths = [...source.matchAll(/<Route path="([^"]+)"/g)].map(m => m[1])
  for (const path of ['/releases', '/deployments', '/targets', '/targets/new', '/targets/:name', '/backups']) expect(paths.filter(p => p === path)).toHaveLength(1)
  for (const old of ['/deployments/:gtab', '/infrastructure', '/infrastructure/jobs', '/infrastructure/new', '/infrastructure/:name', '/integrations/targets/new', '/integrations/targets/:name']) expect(paths).not.toContain(old)
  expect(SETTINGS_TABS).not.toContain('deployments')
})
