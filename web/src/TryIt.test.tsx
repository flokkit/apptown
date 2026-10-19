// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from './data'
import { TryIt } from './TryIt'

// Record ids are UUIDv7 strings.
const CHANGE = '00000000-0007-7000-8000-000000000000'

async function render(state: object) {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(state))))
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => {
    root.render(<QueryClientProvider client={newQueryClient()}><TryIt changeId={CHANGE} /></QueryClientProvider>)
  })
  await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
  const text = el.textContent ?? ''
  act(() => root.unmount())
  vi.unstubAllGlobals()
  return text
}

describe('the try-it button', () => {
  // Previews on a worker the forge could not ask: not known, never "cold"
  // or "didn't start", with the reason, and the person may try again.
  it('says when the preview state is not known, and why', async () => {
    const text = await render({ state: 'unavailable', reason: 'the worker could not be asked: connection refused' })
    expect(text).toContain('preview state not known')
    expect(text).toContain('connection refused')
    expect(text).toContain('Try again')
    expect(text).not.toContain("didn't start")
  })
  // A live preview the forge says answers on the forge host only is not a
  // link that fails elsewhere: the page says where it works.
  it('says where a host-only preview answers instead of opening it', async () => {
    const text = await render({ state: 'live', url: 'http://forge.example:40001', hostOnly: true })
    expect(text).toContain("on AppTown's host only, at localhost:40001")
    expect(text).not.toContain('Open')
  })
  it('opens a preview that answers at its address', async () => {
    const text = await render({ state: 'live', url: 'https://change-7.preview.example' })
    expect(text).toContain('Open')
    expect(text).not.toContain("AppTown's host only")
  })
  it('offers the preview when it is cold', async () => {
    const text = await render({ state: 'cold' })
    expect(text).toContain('Try it')
    expect(text).not.toContain('not known')
  })
})
