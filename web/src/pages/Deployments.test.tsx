// @vitest-environment jsdom
import { expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import Deployments from './Deployments'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// One row per app: its placements are counted, and only a current
// running observation counts as working; the app's page lists each one.
it('counts an app\'s placements and only the current running ones as working', async () => {
  const apps = [
    { slug: 'ledger', name: 'Ledger', deploymentId: 'east', deploymentLabel: 'East office', running: true, observation: 'running', targetName:'East host', observationStale:false, url: 'https://east.example' },
    { slug: 'ledger', name: 'Ledger', deploymentId: 'west', deploymentLabel: 'West office', running: false, observation:'unknown', targetName:'West host', observationStale:true, url: 'https://west.example' },
  ]
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    url.endsWith('/tiers') ? { tiers: [{ name: 'release', title: 'Released environments', apps }] }
      : url.endsWith('/projects') ? [{ slug: 'ledger', name: 'Ledger' }] : url.endsWith('/deployments') ? [{ id: 'east', name: 'east', generation: 1, observation: 'running', observationStale: false }] : { parts: [] },
  ))))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/deployments/table']}><Routes><Route path="/deployments/:gtab" element={<Deployments />} /></Routes></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.textContent).toContain('2 deployments · 1 working'))
    expect(el.querySelectorAll('.row-status')).toHaveLength(1)
    await vi.waitFor(() => expect(el.querySelector('.row-status .chip.pass')?.textContent).toContain('working'))
    expect(el.querySelector('.row-action a')?.getAttribute('href')).toBe('/projects/ledger')
    expect(el.textContent).not.toContain('Not deployed2')
  } finally {
    act(() => root.unmount())
    client.clear()
    vi.unstubAllGlobals()
  }
})

it('uses the app observation even when the first tier has no deployments', async () => {
  vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    url.endsWith('/tiers') ? { tiers: [{ name: 'preview', title: 'Preview', apps: [] }, { name: 'main', title: 'Production', apps: [{ slug: 'ledger', name: 'Ledger', deploymentLabel: 'Production', running: true, observation: 'running', targetName: 'Kitchen', observationStale: false }] }] }
      : url.endsWith('/projects') ? [{ slug: 'ledger', name: 'Ledger', deployed: true, observation: 'running', targetName: 'Kitchen', observationStale: false }] : url.endsWith('/deployments') ? [{ name: 'main', generation: 1, observation: 'running', observationStale: false }] : { parts: [] },
  ))))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/deployments/table']}><Routes><Route path="/deployments/:gtab" element={<Deployments />} /></Routes></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelector('.row-status .chip.pass')?.textContent).toContain('working'))
    expect(el.querySelector('.card.row strong')?.textContent).toContain('Ledger')
    expect(el.querySelector('.row-action')?.textContent).toContain('View app')
    expect(el.textContent).toContain('1 deployment · 1 working')
  } finally {
    act(() => root.unmount()); client.clear(); vi.unstubAllGlobals()
  }
})

// An app last seen running some time ago says when, never plain working.
it('shows a stale running app as last seen', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    url.endsWith('/tiers') ? { tiers: [{ name: 'main', title: 'Production', apps: [{ slug: 'ledger', name: 'Ledger', deploymentLabel: 'Production', running: true, observation: 'running', targetName: 'Kitchen', observationStale: true, observedAt: '2026-01-01T00:00:00Z' }] }] }
      : url.endsWith('/projects') ? [{ slug: 'ledger', name: 'Ledger', deployed: true, observation: 'running', targetName: 'Kitchen', observationStale: true, observedAt: '2026-01-01T00:00:00Z' }] : url.endsWith('/deployments') ? [{ name: 'main', generation: 1, observation: 'running', observationStale: true, observedAt: '2026-01-01T00:00:00Z' }] : { parts: [] },
  ))))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/deployments/table']}><Routes><Route path="/deployments/:gtab" element={<Deployments />} /></Routes></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelector('.row-status .chip')?.textContent).toContain('last seen'))
    expect(el.querySelector('.row-status .chip.pass')).toBeNull()
  } finally {
    act(() => root.unmount()); client.clear(); vi.unstubAllGlobals()
  }
})

// Departures carry their own date and time on each row; there are no
// day headings repeating them.
it('lists departures without day headings, each with its own date', async () => {
  const departures = [
    { releaseNumber: 7, at: '2026-11-04T07:41:00Z', slug: 'fieldnotes', name: 'Fieldnotes', tier: 'main deployment', label: 'release', who: 'bls', state: 'deployed' },
    { releaseNumber: 2, at: '2026-11-03T08:22:00Z', slug: 'feet', name: 'Feet catalog', tier: 'haha deployment', label: 'release', who: 'bls', state: 'deployed' },
  ]
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    url.includes('/tiers/departures') ? { departures } : url.endsWith('/tiers') ? { tiers: [] } : [],
  ))))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/deployments/departures']}><Routes><Route path="/deployments/:gtab" element={<Deployments />} /></Routes></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelectorAll('.departure-row')).toHaveLength(2))
    expect(el.textContent).not.toMatch(/NOV 2026/i)
    expect(el.querySelectorAll('.departure-row')[0].textContent).toMatch(/4 Nov/)
    expect(el.querySelectorAll('.departure-row')[1].textContent).toMatch(/3 Nov/)
  } finally {
    act(() => root.unmount())
    client.clear()
    vi.unstubAllGlobals()
  }
})
