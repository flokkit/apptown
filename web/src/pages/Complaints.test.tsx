// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { settled } from '../settled'
import Complaints from './Complaints'
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
afterEach(() => vi.unstubAllGlobals())

it('shows the latest ten answered reports and reveals older reports on request', async () => {
  const complaints = Array.from({ length: 21 }, (_, i) => ({ id: String(i), text: `Report ${i}`, source: 'console', at: '2026-10-30T01:00:00Z', state: 'answered', seen: true, answer: 'Fixed', answeredBy: 'sam' }))
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.includes('/authz/me') ? { instance: {} } : { complaints }))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><Complaints tab="answered" /></MemoryRouter></QueryClientProvider>))
    await settled(client)
    expect(el.textContent).toContain('Report 9')
    expect(el.textContent).not.toContain('Report 10')
    await act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Show more (11)')!.click())
    expect(el.textContent).toContain('Report 19')
    expect(el.textContent).not.toContain('Report 20')
  } finally { await act(async () => root.unmount()); client.clear() }
})
