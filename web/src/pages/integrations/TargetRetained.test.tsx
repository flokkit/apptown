// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { newQueryClient } from '../../data'
import { TargetRetained } from './TargetRetained'

it('records removal only from the selected target and keeps the door in its menu', async () => {
  let retained = [{ id: 'here', project: 'lunch', target: 'office', need: 'postgres', handle: { name: 'old-lunch' }, disposition: 'retained' }, { id: 'elsewhere', project: 'other', target: 'cloud', need: 'bucket', handle: { name: 'other-data' }, disposition: 'retained' }]
  const writes: unknown[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (init?.method === 'POST') {
      writes.push([path, JSON.parse(String(init.body))]); retained = retained.filter(r => r.id !== 'here')
      return new Response('{"ok":true}')
    }
    return new Response(JSON.stringify({ retained }))
  }))
  const el = document.createElement('div'), root = createRoot(el), client = newQueryClient()
  document.body.append(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><TargetRetained name="office" /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.textContent).toContain('old-lunch'))
    expect(el.textContent).not.toContain('other-data')
    expect(el.textContent).not.toContain('Record removal')
    await act(async () => el.querySelector<HTMLButtonElement>('button[aria-label="More actions for old-lunch"]')!.click())
    await act(async () => [...document.querySelectorAll('button')].find(b => b.textContent === 'Record removal…')!.click())
    expect(el.textContent).toContain('it deletes nothing at the provider')
    await act(async () => el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    expect(writes).toEqual([['/api/v1/projects/lunch/retained/here', { disposition: 'released', note: '' }]])
    await vi.waitFor(() => expect(el.textContent).not.toContain('old-lunch'))
  } finally { act(() => root.unmount()); client.clear(); el.remove(); vi.unstubAllGlobals() }
})
