// @vitest-environment jsdom
import { expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes, useNavigate, type NavigateFunction } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../data'
import Project from './Project'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const project = (id: number, slug: string, name: string) =>
  ({ id: `00000000-${String(id).padStart(4, '0')}-7000-8000-000000000000`, slug, name, namespace: '', icon: '', blurb: '', createdAt: '', requireIndependentApproval: false, outOfBandEdits: [] })

// The project page stays mounted when the sidebar moves to another project.
// Save sends every field, so a draft started on one project must never be
// what the next project's form shows or saves. Today the page shows loading
// while it reads the next project, which unmounts the settings tabs and drops
// the draft; this holds that behaviour in place.
it('moving to another project shows that project, not the last one\'s draft', async () => {
  const projects: Record<string, ReturnType<typeof project>> = { a: project(1, 'a', 'Alpha'), b: project(2, 'b', 'Bravo') }
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    const m = url.match(/\/projects\/([a-z]+)$/)
    if (m) return new Response(JSON.stringify(projects[m[1]]))
    return new Response(url.endsWith('/projects') || url.endsWith('/deployments') ? '[]' : '{}')
  }))
  let navigate: NavigateFunction = () => {}
  function Nav() { navigate = useNavigate(); return null }
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  const nameInput = () => el.querySelector('fieldset input') as HTMLInputElement
  try {
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <MemoryRouter initialEntries={['/projects/a/settings/general']}>
            <Nav />
            <Routes><Route path="/projects/:slug/:tab/:stab" element={<Project />} /></Routes>
          </MemoryRouter>
        </QueryClientProvider>)
    })
    await vi.waitFor(() => expect(nameInput()?.value).toBe('Alpha'))
    await vi.waitFor(() => expect(el.querySelector('fieldset')!.disabled).toBe(false))
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(nameInput(), 'Alpha renamed')
      nameInput().dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => { navigate('/projects/b/settings/general') })
    await vi.waitFor(() => expect(nameInput()?.value).toBe('Bravo'))
  } finally {
    act(() => root.unmount())
    client.clear()
    vi.unstubAllGlobals()
  }
})

// A failed refresh of the project keeps the page mounted under a stale note,
// so the settings form and what someone typed into it survive a network
// blip (web review B14, finding 1: the error used to replace the page).
it('a draft in the settings form survives a failed refresh of the project', async () => {
  const alpha = project(1, 'a', 'Alpha')
  let down = false
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (down) return new Response(JSON.stringify({ error: 'AppTown is restarting' }), { status: 502 })
    if (/\/projects\/a$/.test(url)) return new Response(JSON.stringify(alpha))
    return new Response(url.endsWith('/projects') || url.endsWith('/deployments') ? '[]' : '{}')
  }))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  const nameInput = () => el.querySelector('fieldset input') as HTMLInputElement
  try {
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <MemoryRouter initialEntries={['/projects/a/settings/general']}>
            <Routes><Route path="/projects/:slug/:tab/:stab" element={<Project />} /></Routes>
          </MemoryRouter>
        </QueryClientProvider>)
    })
    await vi.waitFor(() => expect(nameInput()?.value).toBe('Alpha'))
    await vi.waitFor(() => expect(el.querySelector('fieldset')!.disabled).toBe(false))
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(nameInput(), 'Alpha renamed')
      nameInput().dispatchEvent(new Event('input', { bubbles: true }))
    })
    down = true
    await act(async () => { await client.refetchQueries({ queryKey: getKey('/projects/a') }).catch(() => {}) })
    await vi.waitFor(() => expect(el.textContent).toContain('unknown'))
    expect(nameInput()?.value).toBe('Alpha renamed')
  } finally {
    act(() => root.unmount())
    client.clear()
    vi.unstubAllGlobals()
  }
})

// Open chats have one continuation. Completed chats do not become a second
// history section underneath the project's activity record.
it.each([true, false])('shows open chats without a Past chats section (chat=%s)', async chat => {
  const alpha = project(1, 'a', 'Alpha')
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    const body = url.endsWith('/projects/a') ? alpha
      : url.endsWith('/overview') ? { active: [], history: [], open: chat ? [{ changeId: 'chat', changeNumber: 3, title: 'Add lunch orders', state: 'in_progress', statusLabel: 'waiting' }] : [] }
      : url.endsWith('/schedules') ? { schedules: [] }
      : url.endsWith('/transcript') ? { sessions: [{ id: 'past', title: 'A completed chat' }] }
      : url.endsWith('/release/plan') ? { configured: false }
      : []
    return new Response(JSON.stringify(body))
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/projects/a']}><Routes><Route path="/projects/:slug" element={<Project />} /></Routes></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.textContent).toContain(chat ? 'Add lunch orders' : 'No open changes.'))
    if (chat) {
      expect(el.querySelector('.row-status .chip')?.textContent).toContain('chat open')
      expect(el.querySelector('.row-action a')?.textContent).toBe('Continue chat')
      expect(el.querySelector('.row-action a')?.getAttribute('href')).toBe('/projects/a/console?change=chat')
    }
    expect(el.textContent).not.toContain('Past chats')
    expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).endsWith('/transcript'))).toBe(false)
  } finally {
    act(() => root.unmount()); client.clear(); vi.unstubAllGlobals()
  }
})
