// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient, getKey } from '../data'
import { ResourceTables } from './Infrastructure'
import { BackupsView } from './Backups'
import { groupJobs } from './Jobs'
import { Evidence } from './change/Evidence'
import { ProjectIntegrationsTab } from './project/settings/Services'
import { NewChangeButton } from './project/NewChange'
import { AdrsTab } from './project/Decisions'
import type { ChangeView, Run, Step } from '../api'
import type { Job } from '../wire.gen'

// A closed disclosure's summary is part of the first view; its body is not.
function firstView(html: string) {
  const el = document.createElement('div')
  el.innerHTML = html
  el.querySelectorAll('details:not([open])').forEach(d => {
    const summary = d.querySelector(':scope > summary')
    Array.from(d.childNodes).filter(c => c !== summary).forEach(c => c.remove())
  })
  return el.textContent || ''
}

it('shows the latest backup once and caps the history at ten', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/backups'), { keep: 7, where: '/backups', backups: Array.from({ length: 30 }, (_, n) => ({
    id: String(n), project: 'Family photos', deployment: 'main', takenAt: '2026-10-30T00:00:00Z', status: 'ok', statusLabel: 'finished', bytes: 128, path: '/backups/club/main',
    items: [{ kind: 'need', name: 'postgres', caveat: 'Technical copy limits' }],
  })) })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><BackupsView /></QueryClientProvider></MemoryRouter>)
  expect(firstView(html)).toContain('Technical copy limits')
  const el = document.createElement('div'); el.innerHTML = html
  expect(el.querySelectorAll('table')[0].querySelectorAll('tbody tr')).toHaveLength(1)
  expect(el.querySelectorAll('table')[1].querySelectorAll('tbody tr')).toHaveLength(10)
  expect(html).toContain('Show 10 more')
  client.clear()
})

it('groups successful work by kind across targets while preserving failures and waiting work', () => {
  const jobs = [
    { id: 5, kind: 'inventory', key: 'Kitchen', state: 'completed' },
    { id: 4, kind: 'inventory', key: 'Kitchen', state: 'completed' },
    { id: 3, kind: 'inventory', key: 'Office', state: 'completed' },
    { id: 2, kind: 'inventory', key: 'Kitchen', state: 'discarded' },
    { id: 1, kind: 'inventory', key: 'Kitchen', state: 'available' },
  ] as Job[]
  expect(groupJobs(jobs).map(g => [g.job.id, g.count])).toEqual([[5, 3], [2, 1], [1, 1]])
})

it('keeps commands, approval credentials, comments and step identifiers behind Details', () => {
  const steps = [
    { id: 'test-id', type: 'tests', name: 'scan-source:osv', status: 'pass', statusLabel: 'passed', overrideBy: '', evidence: { command: 'mvn -q -Dmaven.repo.local=/apptown-cache/m2 test' } },
    { id: 'approval-id', type: 'approval', name: 'accept-landing', status: 'pass', statusLabel: 'passed', overrideBy: '', evidence: { approver: 'Sam', comment: 'accepted run raw-uuid', decisions: [{ approver: 'Sam', via: 'token-cli:secret-token' }] } },
  ] as unknown as Step[]
  const run = { id: 'run-id', status: 'done', statusLabel: 'finished', workflowName: 'internal-workflow', workflowVersion: 12 } as Run
  const view = { checks: { tone: 'neutral', text: 'Not checked yet.', started: false }, change: { state: 'open' }, detail: { run, steps } } as unknown as ChangeView
  const html = renderToStaticMarkup(<MemoryRouter><Evidence view={view} runs={[{ run, steps }]} workTab={() => '/work'} /></MemoryRouter>)
  const visible = firstView(html)
  expect(visible).toContain('Approved by Sam')
  for (const word of ['mvn', '/apptown-cache', 'secret-token', 'raw-uuid', 'scan-source:osv', 'internal-workflow']) expect(visible).not.toContain(word)
  expect(html).not.toContain('secret-token')
  expect(html).toContain('href="/work"')
})

it('replaces an empty decisions table with a clear empty state', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/club/adrs'), [])
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><AdrsTab slug="club" /></QueryClientProvider></MemoryRouter>)
  expect(html).toContain('No decisions recorded yet.')
  expect(html).not.toContain('<table')
  client.clear()
})

// Registry setup belongs to the instance; project connections offer no registry.
it('has no project registry setup', () => {
 const client=newQueryClient()
 client.setQueryData(getKey('/projects/club/integrations'),{attachments:[]})
 client.setQueryData(getKey('/attachables'),{attachables:[]})
 const html=renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><ProjectIntegrationsTab slug="club" /></QueryClientProvider></MemoryRouter>)
 expect(firstView(html)).not.toContain('Container registry')
 expect(firstView(html)).not.toContain('Images kept at')
 client.clear()
})

it('starts a secret request as a prepared change without asking for its value', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/club/workflows'), [])
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><NewChangeButton slug="club" external={{ open: true, onClose() {} }} request={{ title: 'Add a secret', body: 'Ask for its name and purpose. Keep the value out of code.' }} /></QueryClientProvider></MemoryRouter>)
  const el = document.createElement('div'); el.innerHTML = html
  expect(el.querySelector('input')?.value).toBe('Add a secret')
  expect(el.querySelector('textarea')?.textContent).toContain('Keep the value out of code.')
  client.clear()
})

it('shows target facts beside the inventory while resource ids stay in detail', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/infrastructure'), { targets: [{ target: 'Kitchen', statusLabel: 'working', live: true, facts: 'ssh root@internal-host TOKEN_NAME', resources: [{ kind: 'container', name: 'apptown-dep-raw-id', id: 'raw-provider-id', status: 'running', statusLabel: 'running', record: 'recorded', hosts: [{ project: 'Diary', deployment: 'main' }] }] }] })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><ResourceTables t={client.getQueryData<{ targets: import('../wire.gen').InfrastructureView[] }>(getKey('/infrastructure'))!.targets[0]} onChanged={() => {}} /></QueryClientProvider></MemoryRouter>)
  expect(firstView(html)).toContain('working')
  expect(firstView(html)).toContain('raw-provider-id')
  expect(firstView(html)).toContain('Diary')
  expect(html).toContain('Diary')
  client.clear()
})

it('keeps a withdrawn approval distinct from an approved person in the first view', () => {
  const step = { id: 'approval', type: 'approval', name: 'accept', config: {}, status: 'pending', statusLabel: 'waiting for checks', overrideBy: '', evidence: { approver: 'Sam', withdrawal: { by: 'Mei', reason: 'The code changed' } } } as unknown as Step
  const run = { id: 'run', status: 'running' } as Run
  const view = { checks: { tone: 'neutral', text: 'Not checked yet.', started: false }, change: { state: 'open' }, detail: { run, steps: [step] } } as unknown as ChangeView
  const html = renderToStaticMarkup(<MemoryRouter><Evidence view={view} runs={[{ run, steps: [step] }]} workTab={() => '/work'} /></MemoryRouter>)
  expect(firstView(html)).toContain('Approval withdrawn by Mei')
  expect(firstView(html)).not.toContain('Approved by Sam')
})

// AppTown runs its own workflows; New change offers only what a person picks.
it('offers no system workflow in New change', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/club/workflows'), [
    { name: 'quick-change', tier: 'core', description: 'small' }, { name: 'adopt-integration', tier: 'system', description: 'wire' }, { name: 'land', tier: 'system', description: 'land' },
  ])
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><NewChangeButton slug="club" external={{ open: true, onClose() {} }} /></QueryClientProvider></MemoryRouter>)
  expect(html).toContain('Quick change')
  expect(html).not.toContain('adopt-integration')
  expect(html).not.toContain('Adopt an integration')
  client.clear()
})
