// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../../data'
import { CatalogPicker } from './ChangeButton'
import { NewChangeButton } from './NewChange'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const el = document.createElement('div')
let root: ReturnType<typeof createRoot>
afterEach(() => { act(() => root.unmount()); el.remove(); vi.unstubAllGlobals() })
it('offers catalog entries as focusable buttons and keeps parameter controls outside them', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.includes('/catalog?')
    ? { catalog: [{ name: 'Update dependencies', description: 'Update the libraries this project uses' }] }
    : { definition: { workflow: 'update', params: [{ name: 'Package', required: true }] } }))))
  document.body.append(el); root = createRoot(el)
  const client = newQueryClient()
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><CatalogPicker slug="library" onClose={() => {}} /></MemoryRouter></QueryClientProvider>))
  await vi.waitFor(() => expect([...el.querySelectorAll('button')].some(button => button.textContent?.includes('Update dependencies'))).toBe(true))
  const entry = [...el.querySelectorAll('button')].find(button => button.textContent?.includes('Update dependencies'))!
  act(() => entry.focus())
  expect(document.activeElement).toBe(entry)
  expect(entry.tabIndex).toBe(0)
  act(() => entry.click())
  await vi.waitFor(() => expect(el.querySelector('input[aria-label="Package"]')).not.toBeNull())
  expect(el.querySelector('input')?.closest('button')).toBeNull()
  expect(entry.getAttribute('aria-expanded')).toBe('true')
  client.clear()
})
it('names the detailed change fields independently of placeholders', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response('[]')))
  document.body.append(el); root = createRoot(el)
  const client = newQueryClient()
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><NewChangeButton slug="library" external={{ open: true, onClose: () => {} }} /></MemoryRouter></QueryClientProvider>))
  for (const name of ['Title', 'For', 'Reference']) expect(el.querySelector(`[aria-label="${name}"]`)).not.toBeNull()
  expect(document.activeElement).toBe(el.querySelector('[aria-label="Title"]'))
  client.clear()
})

it('links missing pack workflows to Way of working without pulling from the catalog', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ catalog: [{ name: 'Upgrade', description: 'Upgrade', missingWorkflow: 'upgrade', fromPack: 'team' }] }))))
  document.body.append(el); root = createRoot(el)
  const client = newQueryClient()
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><CatalogPicker slug="library" onClose={() => {}} /></MemoryRouter></QueryClientProvider>))
  await vi.waitFor(() => expect(el.querySelector('a[href="/projects/library/settings/working"]')).not.toBeNull())
  expect(el.textContent).not.toContain('Pull latest pack')
  client.clear()
})
