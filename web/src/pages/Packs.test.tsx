// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { settled } from '../settled'
import Packs from './Packs'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

afterEach(() => { vi.unstubAllGlobals() })

const pack = (name: string, bound: boolean) => ({
  name, description: name + ' pack', guidance: [], workflows: [], policies: [], origin: 'stock', enabled: true,
  requires: [{ capability: 'scanner', bound }], declaration: { guidance: [], workflows: [], policies: [] },
})

async function page(): Promise<string> {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify([pack('ready', true), pack('short', false)]))))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => {
    root.render(<QueryClientProvider client={client}><MemoryRouter><Packs /></MemoryRouter></QueryClientProvider>)
  })
  await settled(client)
  const text = el.textContent ?? ''
  act(() => root.unmount())
  client.clear()
  return text
}

// A pack card speaks up only when something it needs is missing; a pack
// with everything in place says nothing about integrations.
it('names missing integrations and is quiet when none are missing', async () => {
  const text = await page()
  expect(text).toContain('Missing: scanner')
  expect(text).not.toContain('Required integrations configured')
  expect(text).not.toContain('No integration requirements')
})

it('keeps a long library short until Show all is requested', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(Array.from({ length: 18 }, (_, i) => pack(`way-${i}`, true))))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><Packs /></MemoryRouter></QueryClientProvider>))
    await settled(client)
    expect(el.querySelectorAll('.pack-grid a')).toHaveLength(8)
    await act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Show all packs (18)')!.click())
    expect(el.querySelectorAll('.pack-grid a')).toHaveLength(18)
  } finally { await act(async () => root.unmount()); client.clear() }
})
