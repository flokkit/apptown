// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../data'
import Projects from './Projects'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

afterEach(() => { vi.unstubAllGlobals() })

async function after(state: string | null): Promise<string> {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.includes('/project-cleanups')) {
      return new Response(JSON.stringify({ cleanups: state ? [{ projectId: '00000000-0007-7000-8000-000000000000', slug: 'gone', state, detail: 'docker unreachable', attempts: 1, round: 1, requestedBy: 'admin', requestedAt: '', plan: {} }] : [] }))
    }
    return new Response(JSON.stringify({ projects: [], total: 0 }))
  }))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => {
    root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/projects?removed=gone']}><Projects /></MemoryRouter></QueryClientProvider>)
  })
  // The page has the cleanup's answer, nothing else is still being read, and
  // the zero-delay tick on which the client hands answers to the page has run.
  await vi.waitFor(() => {
    expect(client.getQueryState(getKey('/project-cleanups'))?.status).toBe('success')
    expect(client.isFetching()).toBe(0)
  })
  await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
  const text = el.textContent ?? ''
  act(() => root.unmount())
  client.clear()
  return text
}

// Deleting answers before the cleanup is done; the projects page it lands on
// says the project is still being removed, then that it is gone, and says so
// plainly when the cleanup needs a person.
it('follows a deleted project\'s cleanup to the end', async () => {
  expect(await after(null)).toContain('gone is being removed')
  expect(await after('pending')).toContain('gone is being removed')
  expect(await after('done')).toContain('gone has been removed')
  expect(await after('needs-attention')).toContain('cleaning up after it stopped: docker unreachable')
})
