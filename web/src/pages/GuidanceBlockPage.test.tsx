// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { settled } from '../settled'
import GuidanceBlockPage from './GuidanceBlockPage'
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
afterEach(() => vi.unstubAllGlobals())

it('gives an invalid name link a readable error and a route back to the ID-based library', async () => {
  const paths: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string) => { paths.push(url); return new Response(JSON.stringify({ error: 'bad id' }), { status: 400 }) }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/guidance/lang%2Fselection%2Fdjango-fullstack']}><Routes><Route path="/guidance/:id" element={<GuidanceBlockPage />} /></Routes></MemoryRouter></QueryClientProvider>))
    await settled(client)
    expect(paths).toEqual(['/api/v1/guidance/blocks/lang%2Fselection%2Fdjango-fullstack'])
    expect(el.querySelector('h1')?.textContent).toBe('Guidance unavailable')
    expect(el.querySelector('a')?.getAttribute('href')).toBe('/guidance')
    expect(el.textContent).toContain('Choose it from the library')
  } finally { await act(async () => root.unmount()); client.clear() }
})
