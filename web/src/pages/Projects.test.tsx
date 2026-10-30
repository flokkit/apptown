// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../data'
import Projects from './Projects'
// @ts-expect-error -- this package has no Node types; only this read is used.
import { readFileSync } from 'node:fs'
const css = readFileSync('src/styles.css', 'utf8')

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

async function listing(): Promise<HTMLElement> {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.includes('/project-cleanups')) return new Response(JSON.stringify({ cleanups: [] }))
    return new Response(JSON.stringify({ projects: [
      { id: '00000000-0001-7000-8000-000000000000', slug: 'plain', name: 'Plain', blurb: '', icon: '', deployed: false, observation: 'unknown', observationStale: true },
      { id: '00000000-0002-7000-8000-000000000000', slug: 'boat', name: 'Boat', blurb: 'A long description that must stay on one line in both the desktop list and phone cards', icon: '⛵', deployed: true, observation: 'unknown', observationStale: true },
    ], total: 2 }))
  }))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => {
    root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/projects']}><Projects /></MemoryRouter></QueryClientProvider>)
  })
  await vi.waitFor(() => expect(el.textContent).toContain('Plain'))
  return el
}

// A project shows its own icon or none: no stand-in icon repeated on every
// card. The page's box filters the list; finding stays in the top bar, so
// the page box does not take the find box's id.
it('shows only icons projects chose, and leaves finding to the top bar', async () => {
  const el = await listing()
  expect(el.textContent).not.toContain('🛠️')
  expect(el.textContent).toContain('⛵')
  expect(el.querySelector('#find-projects')).toBeNull()
})

// A project with nothing deployed has no runtime stamp: there is nothing to
// say is running, stopped or unknown. A deployed one nobody has observed
// still says it has not been checked, in the unknown style.
it('stamps only the projects that have a deployment', async () => {
  const el = await listing()
  const card = (slug: string) => el.querySelector(`a[href="/projects/${slug}"]`)!.closest('tr')!
  expect(card('plain').querySelector('.chip')).toBeNull()
  expect(card('plain').querySelector('.chip')).toBeNull()
  const stamp = card('boat').querySelector('.chip.unknown')
  expect(stamp?.textContent).toBe('● unknown')
  expect(stamp?.getAttribute('title')).toBe('Not checked yet.')
})


it('keeps list descriptions on one line with an ellipsis', async () => {
  const el = await listing()
  expect(el.querySelector('.project-description')?.textContent).toContain('A long description')
  const rule = css.match(/\.project-description\s*\{([^}]+)\}/)?.[1] ?? ''
  expect(rule).toMatch(/white-space:\s*nowrap/)
  expect(rule).toMatch(/overflow:\s*hidden/)
  expect(rule).toMatch(/text-overflow:\s*ellipsis/)
  expect(el.querySelector('table')?.classList.contains('stack')).toBe(true)
})
