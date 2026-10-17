// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../../data'
import { settled } from '../../settled'
import { BigChangeBox } from './Overview'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

afterEach(() => { vi.unstubAllGlobals() })

async function doors(runtimeForm: string): Promise<string> {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.endsWith('/projects/p')) return new Response(JSON.stringify({ id: '00000000-0001-7000-8000-000000000000', slug: 'p', name: 'P', blurb: '', runtimeForm, intranetReady: true }))
    return new Response('[]') // no deployments
  }))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => {
    root.render(<QueryClientProvider client={client}><MemoryRouter><BigChangeBox slug="p" /></MemoryRouter></QueryClientProvider>)
  })
  await settled(client)
  const text = el.textContent ?? ''
  act(() => root.unmount())
  client.clear()
  return text
}

// "Build the first version" is for a project with no app. A project whose
// pack seeded a working starter has an app before its first deploy, and is
// changed, not built.
it('offers to build the first version only when the project has no app yet', async () => {
  expect(await doors('no app yet')).toContain('Build the first version')
  expect(await doors('static site')).not.toContain('Build the first version')
  expect(await doors('runs from source')).not.toContain('Build the first version')
})
