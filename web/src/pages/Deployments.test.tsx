// @vitest-environment jsdom
import { expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import Deployments from './Deployments'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('lists every deployment separately by display name, including stopped copies', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    url.endsWith('/projects') ? [{ slug: 'ledger', name: 'Ledger' }]
      : url.endsWith('/deployments') ? [
        { id: 'internal-east', name: 'east', displayLabel: 'East office', generation: 1, observation: 'stopped' },
        { id: 'internal-west', name: 'west', displayLabel: 'West office', generation: 1, observation: 'running' },
      ] : url.endsWith('/releases') ? [] : { parts: [] },
  ))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><Deployments /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelectorAll('[role=listitem]')).toHaveLength(2))
    expect(el.textContent).toContain('East office')
    expect(el.textContent).toContain('West office')
    expect(el.textContent).not.toContain('internal-east')
    expect(el.querySelector('a[href="/projects/ledger/deployments/east"]')).not.toBeNull()
    expect(el.textContent).toContain('stopped')
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

it('lists releases waiting for approval with their own dates and deployment names', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ departures: [
    { releaseNumber: 7, at: '2026-11-04T07:41:00Z', slug: 'ledger', name: 'Ledger', tier: 'East office deployment', state: 'waiting for approval' },
  ] }))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><Deployments page="releases" /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.textContent).toContain('release #7 to East office deployment'))
    expect(el.textContent).toContain('waiting for approval')
    expect(el.textContent).toContain('4 Nov')
    expect(el.querySelector('a[href="/projects/ledger/releases/7"]')).not.toBeNull()
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
