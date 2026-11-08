// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { settled } from '../settled'
import Packs from './Packs'

// A status's details ride in its title, not in the page text.
const details = (root: Element) => [...root.querySelectorAll('[data-help], [title]')].map(e => e.getAttribute('data-help') ?? e.getAttribute('title')).join(' | ')

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

afterEach(() => { vi.unstubAllGlobals() })

const pack = (name: string, bound: boolean) => ({
  name, description: name + ' pack', guidance: [], workflows: [], policies: [], origin: 'stock', enabled: true,
  requires: [{ capability: 'scanner', bound }], declaration: { guidance: [], workflows: [], policies: [] },
})

async function page(): Promise<{ text: string; details: string }> {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith('/wow') ? { packs: [] } : [pack('ready', true), pack('short', false)]))))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => {
    root.render(<QueryClientProvider client={client}><MemoryRouter><Packs /></MemoryRouter></QueryClientProvider>)
  })
  await settled(client)
  const text = el.textContent ?? ''
  const shown = details(el)
  act(() => root.unmount())
  client.clear()
  return { text, details: shown }
}

// A pack card speaks up only when something it needs is missing; a pack
// with everything in place says nothing about integrations.
it('names missing integrations and is quiet when none are missing', async () => {
  const { text, details } = await page()
  // What is missing is the status's details, open on hover or tap.
  expect(details).toContain('Missing: scanner')
  expect(details.match(/Missing:/g)).toHaveLength(1)
  for (const said of [text, details]) {
    expect(said).not.toContain('Required integrations configured')
    expect(said).not.toContain('No integration requirements')
  }
})

it('shows every pack, counted on the section heading', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith('/wow') ? { packs: [] } : Array.from({ length: 18 }, (_, i) => pack(`way-${i}`, true))))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><Packs /></MemoryRouter></QueryClientProvider>))
    await settled(client)
    expect(el.querySelectorAll('.pack-name')).toHaveLength(18)
    expect(el.querySelector('section[aria-label="Packs"] .section-count')!.textContent).toBe('18')
  } finally { await act(async () => root.unmount()); client.clear() }
})
