import { settled } from './settled'
import { withFeatures } from './features.fixture'
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
  vi.stubGlobal('fetch', vi.fn(withFeatures(async () => new Response(JSON.stringify({ slug: 'a', name: 'Alpha' })))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/projects/a/memory/${page}`]}><SidebarNav /><TopBar me={null} /></MemoryRouter></QueryClientProvider>))
    await settled(client)
    expect([...el.querySelectorAll('.nav-drawer-head > a')].map(a => a.textContent)).toEqual(['Security', 'Settings', 'Memory'])
    const toggle = el.querySelector<HTMLButtonElement>('button[aria-label="Close Memory"]')!
    expect(el.querySelector('.topbar-crumb:last-child a')?.textContent).toBe('Memory')
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

it.each([[true, true], [true, false], [false, true], [false, false]])('offers Maintenance for project read %s and conformance read %s', async (projects, conformance) => {
  vi.stubGlobal('fetch', vi.fn(withFeatures(async url => new Response(JSON.stringify(url.endsWith('/authz/me') ? { instance: { 'project:read': projects, 'conformance:read': conformance, 'instance:updates': true } } : {})))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/maintenance/settings']}><SidebarNav /></MemoryRouter></QueryClientProvider>))
    await settled(client)
    const setup = el.querySelector('.nav-group')!
    const top = [...el.children].slice(0, [...el.children].indexOf(setup)).map(e => e.matches('a') ? e.textContent : e.querySelector('a')?.textContent)
    expect(top).toEqual(['Home', 'Projects', 'Deployments', ...(projects || conformance ? ['Maintenance'] : [])])
    // Maintenance is a drawer whose pages follow the reader's access.
    expect(el.querySelector('.nav-drawer-head a[href="/maintenance"]')?.classList.contains('active-area')).toBe(projects || conformance ? true : undefined)
    expect([...el.querySelectorAll('.nav-drawer-items a[href^="/maintenance"]')].map(a => a.textContent)).toEqual([...(projects ? ['Needs you'] : []), ...(conformance ? ['Standards'] : []), ...(projects ? ['Updates', 'Settings'] : [])])
    expect(el.querySelector('.nav-drawer-items a[href="/maintenance/settings"]')?.classList.contains('active')).toBe(projects ? true : undefined)
    expect(el.querySelector('a[href="/janitor"]')).toBeNull()
    expect(el.querySelector('a[href="/conformance"]')).toBeNull()
    expect(el.querySelector('a[href="/updates"]')?.textContent).toBe('AppTown updates')
    expect(el.querySelector('a[href="/updates"]')?.nextElementSibling?.className).toBe('spacer')
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
