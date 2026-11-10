// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient, getKey } from '../../data'
import { TargetConnections } from './TargetConnections'
import type { TargetView } from '../../wire.gen'

it('has sibling sections with help and shows connection descriptions and recorded uses', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/integrations'), { integrations: [{ id: 'portainer-id', name: 'portainer-deploy', capability: 'deploy', provider: 'portainer', config: { description: 'Deploys stacks through the office Portainer.' }, health: { checked: true, status: 'ok' } }] })
  client.setQueryData(getKey('/integrations/catalog'), { catalog: [] })
  const targets = [{ id: 't', name: 'office', displayLabel: 'Office', connectionId: 'portainer-id', provider: 'portainer-deploy', connector: 'portainer-deploy', ingress: { rehearsals: true }, address: {} }] as unknown as TargetView[]
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><TargetConnections targets={targets} /></QueryClientProvider></MemoryRouter>)
  const el = document.createElement('div'); el.innerHTML = html
  expect([...el.querySelectorAll('section')].map(s => s.getAttribute('aria-label'))).toEqual(['Deploy connectors', 'Cloud accounts'])
  expect(el.querySelectorAll('.page-help-q')).toHaveLength(2)
  expect(el.textContent).toContain('Deploys stacks through the office Portainer.')
  expect(el.textContent).toContain('Used for')
  expect(el.textContent).toContain('rehearsal host')
  expect(el.querySelector('a[href="/targets/office"]')).not.toBeNull()
})

// The built-in host records no provider; it is shown under the connector
// the owner names for it, never as "No target recorded."
it('lists the built-in host under the connector the owner names for it', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/integrations'), { integrations: [{ id: 'docker-id', name: 'docker-deploy', capability: 'deploy', provider: 'docker', config: { description: 'Deploys apps to AppTown’s own Docker host.' }, health: { checked: true, status: 'ok' } }] })
  client.setQueryData(getKey('/integrations/catalog'), { catalog: [] })
  const targets = [{ id: 'b', name: 'built-in-docker', displayLabel: 'Built-in Docker host', kind: 'docker-host', origin: 'built-in', provider: '', connector: 'docker-deploy', ingress: {}, address: { daemon: 'local' } }] as unknown as TargetView[]
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><TargetConnections targets={targets} /></QueryClientProvider></MemoryRouter>)
  expect(el.querySelector('a[href="/targets/built-in-docker"]')).not.toBeNull()
  expect(el.textContent).not.toContain('No target recorded.')
})
