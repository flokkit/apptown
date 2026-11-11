// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { act } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { getKey, newQueryClient } from '../data'
import { JobsPage } from './Jobs'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('shows background job facts inline without a disclosure', () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/jobs?limit=500'), { jobs: [{ id: 1, kind: 'inventory', key: 'host-inventory', state: 'completed', statusLabel: 'finished', createdAt: new Date().toISOString(), receipt: { resources: 1 } }] })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><JobsPage /></QueryClientProvider></MemoryRouter>)
  expect(html).toContain('<h1>Background jobs</h1>')
  expect(html).not.toContain('<details')
  expect(html).not.toContain('Technical details')
})

// The job's word is the owner's; the queue's own state name beside it
// ("waiting available") would be a second word for one state.
it.each(['available', 'pending', 'running', 'retryable', 'scheduled', 'completed', 'cancelled', 'discarded'])('shows the label for recorded %s, and its error, without the queue state name', state => {
  const client = newQueryClient()
  client.setQueryData(getKey('/jobs?limit=500'), { jobs: [{ id: 1, kind: 'inventory', key: 'host-inventory', state, statusLabel: 'waiting', error: 'Host did not answer', attempts: 2, createdAt: new Date().toISOString() }] })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><JobsPage /></QueryClientProvider></MemoryRouter>)
  const page = new DOMParser().parseFromString(html, 'text/html')
  const stateCell = page.querySelector('tbody tr td:nth-child(2)')!
  expect(stateCell.textContent).toContain('waiting')
  expect(stateCell.textContent).not.toContain(state)
  expect(stateCell.textContent).toContain('Host did not answer')
  expect(stateCell.textContent).toContain('attempt 2')
  expect(page.querySelector('details, [aria-expanded]')).toBeNull()
})

// A job's kind and key are for administrators: the Job column already says
// the kind in words, so the raw line shows only with the technical switch on.
it.each([false, true])('shows the job key on its own technical line only with the technical switch (%s)', async technical => {
  const key = 'protection-observation/01a1c0f7-f732-717b-bf23-3bc2cfec567f/0bfd216d8671931e214963dad76672e5b1978f80b6dae4cf07a49bab12b518bb'
  const user = `jobs-viewer-${technical}`
  if (technical) localStorage.setItem(`apptown.technical-detail.${user}`, '1')
  const client = newQueryClient()
  client.setQueryData(getKey('/auth/me'), { mode: 'internal', user })
  client.setQueryData(getKey('/jobs?limit=500'), { jobs: [{ id: 1, kind: 'protection-observation', key, state: 'available', statusLabel: 'waiting', createdAt: new Date().toISOString() }] })
  const el = document.createElement('div'), root = createRoot(el)
  await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><JobsPage /></QueryClientProvider></MemoryRouter>))
  const outcome = el.querySelector('tbody tr td:nth-child(4)')!
  const tech = outcome.querySelector('.tech-line')
  if (technical) expect(tech?.textContent).toBe(`protection-observation · ${key}`)
  else expect(el.textContent).not.toContain(key)
  tech?.remove()
  expect(outcome.textContent).not.toContain(key)
  await act(async () => root.unmount()); localStorage.clear()
})
