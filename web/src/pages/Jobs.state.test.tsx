// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { getKey, newQueryClient } from '../data'
import { JobsPage } from './Jobs'

it('retains the recorded scheduled state when the display label is waiting', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/jobs?limit=500'), { jobs: [{ id: 1, kind: 'inventory', key: 'host-inventory', state: 'scheduled', statusLabel: 'waiting', createdAt: new Date().toISOString() }] })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><JobsPage /></QueryClientProvider></MemoryRouter>)
  expect(html).toContain('scheduled')
})
