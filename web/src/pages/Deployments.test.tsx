// @vitest-environment jsdom
import { expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import Deployments from './Deployments'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('shows both customer placements with their own label, address and observation', async () => {
  const apps = [
    { slug: 'ledger', name: 'Ledger', deploymentId: 'east', deploymentLabel: 'East office', running: true, url: 'https://east.example' },
    { slug: 'ledger', name: 'Ledger', deploymentId: 'west', deploymentLabel: 'West office', running: false, observed: 'unknown', url: 'https://west.example' },
  ]
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    url.endsWith('/tiers') ? { tiers: [{ name: 'release', title: 'Released environments', apps }] }
      : url.endsWith('/projects') ? [{ slug: 'ledger', name: 'Ledger' }] : {},
  ))))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/deployments/table']}><Routes><Route path="/deployments/:gtab" element={<Deployments />} /></Routes></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.textContent).toContain('West office'))
    expect(el.textContent).toContain('East office')
    expect(el.querySelector('a[href="https://east.example"]')).not.toBeNull()
    expect(el.querySelector('a[href="https://west.example"]')).not.toBeNull()
    expect(el.querySelectorAll('.chip.pass')).toHaveLength(1)
    expect(el.querySelector('.chip.unknown')?.getAttribute('title')).toBe('unknown')
  } finally {
    act(() => root.unmount())
    client.clear()
    vi.unstubAllGlobals()
  }
})
