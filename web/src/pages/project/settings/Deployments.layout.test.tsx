import { enabledFeatures } from '../../../features.fixture'
import { DeploymentStatus } from '../DeploymentStatus'
import { ProtectionCard } from '../DeploymentBackups'
import { DeployAction } from './Deployments'
import { useGet } from '../../../data'
import type { ReleaseListed } from '../../../wire.gen'
// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../../data'
import type { DeploymentView } from '../../../wire.gen'
import { DeploymentTitle } from '../DeploymentDeploys'

// A status's details ride in its title, not in the page text.
const details = (root: Element) => [...root.querySelectorAll('[data-help], [title]')].map(e => e.getAttribute('data-help') ?? e.getAttribute('title')).join(' | ')

// The deployment page's head (title, status, facts) above its details card.
function PageCard({ slug, env }: { slug: string; env: DeploymentView }) {
  const releases = useGet<ReleaseListed[]>(`/projects/${slug}/releases`)
  return <><DeploymentTitle name={env.displayLabel || env.name} env={env} actions={<DeployAction slug={slug} env={env} />} /><DeploymentStatus slug={slug} env={env} refreshFailed={false} releases={releases} current reload={async () => {}} onChoose={() => {}} /></>
}


// The Status tab, and the deployment's backups page one tap from it.
function BackupsAndCard({ env }: { env: DeploymentView }) {
  return <><PageCard slug="lunch" env={env} /><div className="card deploy-entry"><div className="deploy-lines"><ProtectionCard slug="lunch" name={env.name} /></div></div></>
}

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
  el.innerHTML = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter><BackupsAndCard env={{
    name: 'main', displayLabel: 'Lunch Orders', generation: 1, kind: 'standing', backups: true, feed: 'main', audience: 'team', data: 'production',
    status: { word: 'unknown', reason: 'web did not answer in time · old reading', checkedAt: at, stale: true }, observation: 'running', observedAt: at, readiness: 'reconciled', url: 'https://lunch.example', target: { kind: 'docker-host', name: 'local' },
  } as DeploymentView} /></MemoryRouter></QueryClientProvider>)
  client.clear()
  return el
}

it('shows deployment facts without fold-downs', () => {
  const el = card()
  expect(el.querySelector('details')).toBeNull()
  expect(el.querySelector('[aria-hidden="true"]')?.textContent).toBeDefined()
  expect(el.textContent).toContain('what the app declares')
  expect(el.querySelector('a[href="https://lunch.example"]')).not.toBeNull()
  // The missing backup is a row on the Status tab that opens the backups page.
  expect(el.querySelector('a[href="/projects/lunch/deployments/main/backups"]')?.textContent).toBe('Backups')
})

it('shows one deployment check timestamp', () => {
  // Said once across the page and the statuses' details.
  const el = card()
  expect(`${el.textContent} | ${details(el)}`.match(/checked /gi)).toHaveLength(1)
})

it('says the missing backup once, with a disabled restore test and its reason', () => {
  const el = card()
  expect(el.textContent?.match(/No backup yet|no copy|There is no good backup/g)).toHaveLength(1)
  expect(el.querySelector('.error')?.textContent).toBe('✗ No backup yet')
  const button = [...el.querySelectorAll('button')].find(b => b.textContent === 'Test a restore')!
  expect(button.disabled).toBe(true)
  expect(button.title).toBe('There is no good backup of its data yet.')
  expect(el.querySelector(`[id="${button.getAttribute('aria-describedby')}"]`)?.textContent).toContain('No backup yet')
})

it('shows unknown in the head when the host reports unknown parts', () => {
  const head = card().querySelector('.page-bar-title')!
  expect(head.textContent).toContain('unknown')
  expect(head.textContent).not.toContain('working')
})

it('states backup limits once and keeps provider and coverage facts behind Techno Goggles', () => {
  const el = card()
  expect(el.textContent?.match(/AppTown's disk/g)).toHaveLength(1)
  expect(el.textContent).not.toContain('this host')
  for (const text of ['on built-in-docker', 'covers: database']) {
    const fact = [...el.querySelectorAll('.ch-technical')].find(e => e.textContent === text)
    expect(fact?.getAttribute('aria-hidden')).toBe('true')
  }
  expect(el.querySelector('a[href="/docs/guides/deployment-backups"]')?.textContent).toBe('how backups work →')
})
