// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import { getKey, newQueryClient } from '../../data'
import { TargetResourcesPage } from './TargetResources'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('shows a failed reread of the target under its head on every tab', async () => {
  const client = newQueryClient()
  client.setDefaultOptions({ queries: { retry: false } })
  const target = { target: { name: 'kitchen', kind: 'docker-host', status: 'ready', statusLabel: 'working' } }
  const inventory = { targets: [{ target: 'kitchen', live: true, unknown: false, resources: [] }] }
  client.setQueryData(getKey('/targets/kitchen'), target)
  client.setQueryData(getKey('/infrastructure'), inventory)
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    if (String(input).endsWith('/targets/kitchen')) return new Response(JSON.stringify({ error: 'target read unavailable' }), { status: 503 })
    return new Response(JSON.stringify(String(input).includes('/infrastructure') ? inventory : { sources: [], items: [] }))
  }))
  const el = document.body.appendChild(document.createElement('div')), root = createRoot(el)
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/targets/kitchen/resources']}><Routes><Route path="/targets/:name/resources" element={<TargetResourcesPage />} /></Routes></MemoryRouter></QueryClientProvider>) })
    await vi.waitFor(() => expect(client.getQueryState(getKey('/targets/kitchen'))?.status).toBe('error'))
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 20)) })
    // The last good target answer may stay, but its failed reread must be visible.
    expect(el.textContent).toContain('unknown')
  } finally { act(() => root.unmount()); el.remove(); client.clear(); vi.unstubAllGlobals() }
})
