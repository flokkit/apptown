// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import TopBar from './TopBar'
import { SidebarNav } from './App'
import { newQueryClient } from './data'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it.each(['notes', 'decisions'])('Memory has the shared group controls and active %s page', async page => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ slug: 'a', name: 'Alpha' }))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/projects/a/memory/${page}`]}><SidebarNav /><TopBar me={null} /></MemoryRouter></QueryClientProvider>))
    expect([...el.querySelectorAll('.nav-drawer-head > a')].map(a => a.textContent)).toEqual(['Security', 'Settings', 'Memory'])
    const toggle = el.querySelector<HTMLButtonElement>('button[aria-label="Close Memory"]')!
    expect(el.querySelector('.topbar-here')?.textContent).toBe('Memory')
    expect(toggle).not.toBeNull()
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    expect(el.querySelector('a[href="/projects/a/memory"]')?.classList.contains('active-area')).toBe(true)
    expect(el.querySelector(`a[href="/projects/a/memory/${page}"]`)?.classList.contains('active')).toBe(true)
    expect([...el.querySelectorAll('a[href^="/projects/a/memory/"]')].map(a => a.textContent)).toEqual(['Notes', 'Decisions'])
    act(() => toggle.click())
    expect(el.querySelector('a[href="/projects/a/memory/notes"]')).toBeNull()
    act(() => el.querySelector<HTMLButtonElement>('button[aria-label="Open Memory"]')!.click())
    expect(el.querySelector('a[href="/projects/a/memory/notes"]')).not.toBeNull()
    act(() => el.querySelector<HTMLButtonElement>('button[aria-label="Open Settings"]')!.click())
    expect(el.querySelector('a[href="/projects/a/settings/decisions"]')).toBeNull()
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
