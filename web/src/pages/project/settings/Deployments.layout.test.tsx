import { enabledFeatures } from '../../../features.fixture'
// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../../data'
import type { DeploymentView } from '../../../wire.gen'
import { EnvCard } from './Deployments'

// A running deployment observation must not claim working while the host's
// per-part observation is unknown. Missing backups get one actionable account.
function card() {
  const client = newQueryClient()
 client.setQueryData(getKey('/features'), enabledFeatures)
  const base = '/projects/lunch/deployments/main'
  const at = '2026-11-03T08:00:00Z'
  const reading = { observedAt: at, attemptedAt: at, waiting: false, stale: false, unknown: false }
  client.setQueryData(getKey(`${base}/resources`), { reading, recorded: true, profile: 'default', parts: [], limits: [], limitsKnown: true })
  client.setQueryData(getKey(`${base}/diagnostics`), {
    reading, generation: 1, runtime: { state: 'running', observedAt: at }, containers: true,
    parts: [{ part: 'web', presence: 'unknown' }, { part: 'postgres', presence: 'unknown' }], events: [], eventsKnown: true,
  })
  const elsewhere = { available: false, reason: 'not-on-this-platform' }
  const why = 'There is no good backup of its data yet.'
  client.setQueryData(getKey(`${base}/protection`), {
    reading, keptOn: 'host', items: [{ need: 'postgres', where: 'on built-in-docker', copy: 'none', copyLabel: 'missing' }],
    recent: [], restoredCopies: [], rehearsalNote: 'never rehearsed',
    coverage: { assessed: ['postgres database'], notAssessed: ['Files stored elsewhere'], statusLabel: 'unknown' },
    operations: { copy: { available: false, reason: 'backups-page' }, check: elsewhere, restore: elsewhere, finalCopy: elsewhere, rehearsal: { available: false, reason: 'no-copy', why } },
  })
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter><EnvCard slug="lunch" env={{
    name: 'main', displayLabel: 'Lunch Orders', generation: 1, kind: 'standing', feed: 'main', audience: 'team', data: 'production',
    status: { word: 'unknown', reason: 'web did not answer in time', stale: true }, observation: 'running', observedAt: at, readiness: 'reconciled', url: 'https://lunch.example', target: { kind: 'docker-host', name: 'local' },
  } as DeploymentView} onChanged={() => {}} /></MemoryRouter></QueryClientProvider>)
  client.clear()
  return el
}

it('shows deployment facts without fold-downs', () => {
  const el = card()
  expect(el.querySelector('details')).toBeNull()
  expect(el.querySelector('[aria-hidden="true"]')?.textContent).toBeDefined()
  expect(el.textContent).toContain('Not covered: anything the app stores outside its declared needs')
  expect(el.querySelector('a[href="https://lunch.example"]')).not.toBeNull()
})

it('shows one deployment check timestamp', () => {
  expect(card().textContent?.match(/Last checked|Checked/g)).toHaveLength(1)
})

it('says the missing backup once, with a disabled restore test and its reason', () => {
  const el = card()
  expect(el.textContent?.match(/No backup yet|no copy|There is no good backup/g)).toHaveLength(1)
  expect(el.querySelector('.error')?.textContent).toBe('✗ No backup yet: postgres database')
  const button = [...el.querySelectorAll('button')].find(b => b.textContent === 'Test a restore')!
  expect(button.disabled).toBe(true)
  expect(button.title).toBe('There is no good backup of its data yet.')
  expect(el.querySelector(`[id="${button.getAttribute('aria-describedby')}"]`)?.textContent).toContain('No backup yet')
})

it('shows unknown in the head when the host reports unknown parts', () => {
  const head = card().querySelector('.deploy-head')!
  expect(head.textContent).toContain('unknown')
  expect(head.textContent).not.toContain('working')
})

it('states backup limits once and keeps provider and coverage facts behind Techno Goggles', () => {
  const el = card()
  expect(el.textContent?.match(/Kept on this host only; it does not outlive its disk/g)).toHaveLength(1)
  expect(el.textContent?.match(/Not covered:/g)).toHaveLength(1)
  for (const text of ['on built-in-docker', 'covers: database']) {
    const fact = [...el.querySelectorAll('.ch-technical')].find(e => e.textContent === text)
    expect(fact?.getAttribute('aria-hidden')).toBe('true')
  }
  expect(el.querySelector('a[href="/docs/guides/deployment-backups"]')?.textContent).toBe('how backups work →')
})
