// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../data'
import { settled } from '../../settled'
import { BigChangeBox } from './Overview'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

async function doors(runtimeForm: string, intranet = false): Promise<string> {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.endsWith('/projects/p')) return new Response(JSON.stringify({ id: '00000000-0001-7000-8000-000000000000', slug: 'p', name: 'P', blurb: '', runtimeForm, intranetReady: true }))
    if (url.endsWith('/projects/p/release/plan')) return new Response(JSON.stringify({ configured: intranet, deployment: 'lan', published: true, changesSinceLast: [] }))
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

// Release is offered only when a current destination is configured.
it('offers Release only where a destination is configured', async () => {
  expect(await doors('static site', false)).not.toContain('Release')
  expect(await doors('static site', true)).toContain('Release')
})

const selectedID = '00000000-0001-7000-8000-000000000002'
const legacyID = '00000000-0001-7000-8000-000000000003'
const planPath = '/projects/p/release/plan'
async function releaseDoors(options: { hostOnly?: boolean; url?: string; configured?: boolean; cached?: boolean } = {}) {
  let plan = { configured: options.configured ?? true, deploymentId: selectedID, deployment: 'customer', deploymentLabel: 'Customer office', defaultRevision: 2, changesSinceLast: [] }
  let failed = false
  let releaseRead: (() => void) | undefined
  const held = options.cached ? new Promise<void>((resolve) => { releaseRead = resolve }) : Promise.resolve()
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.endsWith(planPath)) {
      await held
      return failed ? new Response(JSON.stringify({ error: 'plan unavailable' }), { status: 403 }) : new Response(JSON.stringify(plan))
    }
    if (url.endsWith('/deployments')) return new Response(JSON.stringify([
      { id: legacyID, name: 'lan', displayLabel: 'Old lab', url: 'https://old.example', running: true },
      { id: selectedID, name: 'customer', displayLabel: 'Customer office', url: options.url ?? 'https://customer.example', hostOnly: options.hostOnly, running: true },
    ]))
    return new Response(JSON.stringify({ slug: 'p', runtimeForm: 'static site' }))
  }))
  const client = newQueryClient()
  if (options.cached) client.setQueryData(getKey(planPath), plan)
  const el = document.createElement('div')
  const root = createRoot(el)
  const opened = vi.spyOn(window, 'open').mockReturnValue(null)
  await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><BigChangeBox slug="p" /></MemoryRouter></QueryClientProvider>) })
  if (!options.cached) await settled(client)
  const clickOpen = () => act(() => { (Array.from(el.querySelectorAll('button')).find(b => b.textContent?.startsWith('▶ Open')) as HTMLButtonElement).click() })
  return { el, opened, clickOpen,
    async refresh(fail = false) {
      failed = fail
      plan = { ...plan, deploymentId: legacyID, deploymentLabel: 'Old lab', deployment: 'lan' }
      await act(async () => { await client.invalidateQueries({ queryKey: getKey(planPath) }) })
      await settled(client)
    },
    async resolve() { releaseRead?.(); await settled(client) },
    close() { act(() => root.unmount()); client.clear() },
  }
}

it('opens the selected UUID and shows customer labels instead of LAN assumptions', async () => {
  const h = await releaseDoors()
  try {
    h.clickOpen()
    expect(h.opened).toHaveBeenLastCalledWith('https://customer.example', '_blank')
    expect(h.el.textContent).toContain('Customer office')
    expect(h.el.textContent).not.toContain('intranet')
    act(() => { (h.el.querySelector('[aria-label="Open: more options"]') as HTMLButtonElement).click() })
    expect(h.el.textContent).toContain('Old lab')
    await h.refresh()
    h.clickOpen()
    expect(h.opened).toHaveBeenLastCalledWith('https://old.example', '_blank')
  } finally { h.close() }
})

it.each([{ hostOnly: true }, { url: '' }])('opens an addressable alternative when the selected default cannot be opened: %j', async options => {
  const h = await releaseDoors(options)
  try { h.clickOpen(); expect(h.opened).toHaveBeenCalledWith('https://old.example', '_blank'); expect(h.el.textContent).toContain('Old lab') }
  finally { h.close() }
})

it('withdraws Release and selected-default preference after a failed plan refresh', async () => {
  const h = await releaseDoors()
  try {
    await h.refresh(true)
    expect(h.el.textContent).not.toContain('publish the latest')
    expect(h.el.textContent).toContain('unknown')
    h.clickOpen()
    expect(h.opened).toHaveBeenCalledWith('https://old.example', '_blank')
  } finally { h.close() }
})

it('does not treat a held cached plan as a current release destination', async () => {
  const h = await releaseDoors({ cached: true })
  try {
    expect(h.el.textContent).not.toContain('publish the latest')
    await h.resolve()
    h.clickOpen()
    expect(h.opened).toHaveBeenCalledWith('https://customer.example', '_blank')
  } finally { await h.resolve(); h.close() }
})

it.each([
  { blurb: 'Truck assignments', hasRequest: true },
  { blurb: '', hasRequest: true },
  { blurb: 'A manually written description', hasRequest: false },
])('keeps the first-build brief for %j', async ({ blurb, hasRequest }) => {
  const originalRequest = hasRequest ? 'Track each truck, driver assignments, servicing dates and overdue inspections. Notify the dispatcher each morning.' : ''
  let body = ''
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (url.endsWith('/projects/p')) return new Response(JSON.stringify({ slug: 'p', blurb, originalRequest, runtimeForm: 'no app yet' }))
    if (url.endsWith('/changes') && init?.method === 'POST') {
      body = JSON.parse(init.body as string).body
      return new Response(JSON.stringify({ change: { id: 'first' } }))
    }
    if (url.endsWith('/release/plan')) return new Response(JSON.stringify({ configured: false }))
    return new Response('[]')
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><BigChangeBox slug="p" /></MemoryRouter></QueryClientProvider>))
    await settled(client)
    await act(async () => { (Array.from(el.querySelectorAll('button')).find(b => b.textContent?.includes('Build the first version')) as HTMLButtonElement).click() })
    expect(body).toBe(originalRequest || 'No description was written for this project yet. Ask what the first version should do before building anything.')
  } finally { act(() => root.unmount()); client.clear() }
})
