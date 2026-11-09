// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { getKey, newQueryClient } from '../data'
import { JobsPage } from './Jobs'

it('shows background job facts inline without a disclosure', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/jobs?limit=500'), { jobs: [{ id: 1, kind: 'inventory', key: 'host-inventory', state: 'completed', statusLabel: 'finished', createdAt: new Date().toISOString(), receipt: { resources: 1 } }] })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><JobsPage /></QueryClientProvider></MemoryRouter>)
  expect(html).toContain('<h1>Background jobs</h1>')
  expect(html).toContain('host-inventory')
  expect(html).not.toContain('<details')
  expect(html).not.toContain('Technical details')
})

it.each(['available', 'pending', 'running', 'retryable', 'scheduled', 'completed', 'cancelled', 'discarded'])('shows recorded %s and its error beside the job label', state => {
  const client = newQueryClient()
  client.setQueryData(getKey('/jobs?limit=500'), { jobs: [{ id: 1, kind: 'inventory', key: 'host-inventory', state, statusLabel: 'waiting', error: 'Host did not answer', attempts: 2, createdAt: new Date().toISOString() }] })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><JobsPage /></QueryClientProvider></MemoryRouter>)
  const page = new DOMParser().parseFromString(html, 'text/html')
  const stateCell = page.querySelector('tbody tr td:nth-child(2)')!
  expect(stateCell.textContent).toContain('waiting')
  expect(stateCell.textContent).toContain(state)
  expect(stateCell.textContent).toContain('Host did not answer')
  expect(stateCell.textContent).toContain('attempt 2')
  expect(page.querySelector('details, [aria-expanded]')).toBeNull()
})

// A job's key is for administrators: it stays on the row, but on its own
// technical line, never in the outcome's words.
it('keeps the job key off the outcome text, on its own technical line', () => {
  const key = 'protection-observation/01a1c0f7-f732-717b-bf23-3bc2cfec567f/0bfd216d8671931e214963dad76672e5b1978f80b6dae4cf07a49bab12b518bb'
  const client = newQueryClient()
  client.setQueryData(getKey('/jobs?limit=500'), { jobs: [{ id: 1, kind: 'protection-observation', key, state: 'available', statusLabel: 'waiting', createdAt: new Date().toISOString() }] })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><JobsPage /></QueryClientProvider></MemoryRouter>)
  const page = new DOMParser().parseFromString(html, 'text/html')
  const outcome = page.querySelector('tbody tr td:nth-child(4)')!
  const tech = outcome.querySelector('.tech-line')
  expect(tech?.textContent).toContain(key)
  tech?.remove()
  expect(outcome.textContent).not.toContain(key)
})
