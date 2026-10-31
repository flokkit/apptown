// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient, getKey } from '../data'
import { InfrastructureView } from './Infrastructure'
import { BackupsView } from './Backups'
import { groupJobs } from './InfrastructureHome'
import { Evidence } from './change/Evidence'
import { ProjectIntegrationsTab } from './project/settings/Services'
import { NewChangeButton } from './project/NewChange'
import { AdrsTab } from './project/settings/Decisions'
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

it('shows only the latest backup until history is opened, and caps that history at ten', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/backups'), { keep: 7, where: '/backups', backups: Array.from({ length: 30 }, (_, n) => ({
    id: String(n), project: 'Family photos', deployment: 'main', takenAt: '2026-10-30T00:00:00Z', status: 'ok', statusLabel: 'finished', bytes: 128, path: '/backups/club/main',
    items: [{ kind: 'need', name: 'postgres', caveat: 'Technical copy limits' }],
  })) })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><BackupsView /></QueryClientProvider></MemoryRouter>)
  expect(firstView(html).match(/Family photos/g)).toHaveLength(1)
  expect(firstView(html)).not.toContain('Technical copy limits')
  const el = document.createElement('div'); el.innerHTML = html
  expect(el.querySelectorAll('table')[1].querySelectorAll('tbody tr')).toHaveLength(10)
  expect(html).toContain('Show 10 more')
  client.clear()
})

it('collapses completed repeats without merging another target, failures or waiting work', () => {
  const jobs = [
    { id: 5, kind: 'inventory', key: 'Kitchen', state: 'completed' },
    { id: 4, kind: 'inventory', key: 'Kitchen', state: 'completed' },
    { id: 3, kind: 'inventory', key: 'Office', state: 'completed' },
    { id: 2, kind: 'inventory', key: 'Kitchen', state: 'discarded' },
    { id: 1, kind: 'inventory', key: 'Kitchen', state: 'available' },
  ] as Job[]
  expect(groupJobs(jobs).map(g => [g.job.id, g.count])).toEqual([[5, 2], [3, 1], [2, 1], [1, 1]])
})

it('keeps commands, approval credentials, comments and step identifiers behind Details', () => {
  const steps = [
    { id: 'test-id', type: 'tests', name: 'scan-source:osv', status: 'pass', statusLabel: 'passed', overrideBy: '', evidence: { command: 'mvn -q -Dmaven.repo.local=/apptown-cache/m2 test' } },
    { id: 'approval-id', type: 'approval', name: 'accept-landing', status: 'pass', statusLabel: 'passed', overrideBy: '', evidence: { approver: 'Sam', comment: 'accepted run raw-uuid', decisions: [{ approver: 'Sam', via: 'token-cli:secret-token' }] } },
  ] as unknown as Step[]
  const run = { id: 'run-id', status: 'done', statusLabel: 'finished', workflowName: 'internal-workflow', workflowVersion: 12 } as Run
  const view = { change: { state: 'open' }, detail: { run, steps } } as unknown as ChangeView
  const html = renderToStaticMarkup(<MemoryRouter><Evidence view={view} runs={[{ run, steps }]} workTab={() => '/work'} /></MemoryRouter>)
  const visible = firstView(html)
  expect(visible).toContain('Approved by Sam')
  for (const word of ['mvn', '/apptown-cache', 'secret-token', 'raw-uuid', 'scan-source:osv', 'internal-workflow']) expect(visible).not.toContain(word)
  expect(html).toContain('secret-token')
})

it('replaces an empty decisions table with a clear empty state', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/club/adrs'), [])
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><AdrsTab slug="club" /></QueryClientProvider></MemoryRouter>)
  expect(html).toContain('No decisions recorded yet.')
  expect(html).not.toContain('<table')
  client.clear()
})

it('explains the image store without exposing repository paths or registry jargon', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/club/integrations'), { attachments: [{ capability: 'container-registry', title: 'Container registry', resource: { repo: 'registry.example/club/app' } }] })
  client.setQueryData(getKey('/attachables'), { attachables: [{ capability: 'container-registry', description: 'OCI images are the unit of release' }] })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><ProjectIntegrationsTab slug="club" /></QueryClientProvider></MemoryRouter>)
  expect(firstView(html)).toContain('Stores your app’s built images')
  expect(firstView(html)).not.toContain('registry.example')
  expect(firstView(html)).not.toContain('OCI')
  expect(html).toContain('registry.example')
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

it('keeps infrastructure inventory and connection facts behind the target row on desktop too', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/infrastructure'), { targets: [{ target: 'Kitchen', statusLabel: 'working', live: true, facts: 'ssh root@internal-host TOKEN_NAME', resources: [{ kind: 'container', name: 'apptown-dep-raw-id', id: 'raw-provider-id', status: 'running', statusLabel: 'running', record: 'recorded', hosts: [{ project: 'Diary', deployment: 'main' }] }] }] })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><InfrastructureView /></QueryClientProvider></MemoryRouter>)
  expect(firstView(html)).toContain('Kitchen')
  expect(firstView(html)).toContain('working')
  expect(firstView(html)).not.toContain('TOKEN_NAME')
  expect(firstView(html)).not.toContain('raw-provider-id')
  expect(firstView(html)).not.toContain('Diary')
  expect(html).toContain('Diary')
  client.clear()
})

it('keeps a withdrawn approval distinct from an approved person in the first view', () => {
  const step = { id: 'approval', type: 'approval', name: 'accept', config: {}, status: 'pending', statusLabel: 'waiting for checks', overrideBy: '', evidence: { approver: 'Sam', withdrawal: { by: 'Mei', reason: 'The code changed' } } } as unknown as Step
  const run = { id: 'run', status: 'running' } as Run
  const view = { change: { state: 'open' }, detail: { run, steps: [step] } } as unknown as ChangeView
  const html = renderToStaticMarkup(<MemoryRouter><Evidence view={view} runs={[{ run, steps: [step] }]} workTab={() => '/work'} /></MemoryRouter>)
  expect(firstView(html)).toContain('Approval withdrawn by Mei')
  expect(firstView(html)).not.toContain('Approved by Sam')
})
