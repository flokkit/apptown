// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../data'
import { TechnicalDetailSwitch } from '../change/TechnicalDetail'
import { DeploymentContainers } from './DeploymentContainers'
import type { DeploymentDiagnostics } from '../../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const PATH = '/projects/game/deployments/main/diagnostics'
const now = new Date().toISOString()
const diagnostics: DeploymentDiagnostics = {
 reading: { observedAt: now, attemptedAt: now, waiting: false, stale: false, unknown: false },
  deployment: 'main', generation: 3, desiredGeneration: 3, commit: 'abcdef0123456789', releaseNumber: 7,
  runtime: { state: 'running', observedAt: now, attemptedAt: now, stale: false },
  containers: true,
  parts: [
    { part: 'web', public: true, presence: 'running', facts: { id: 'w3b', status: 'running', running: true, oomKilled: false, restarts: 0, health: 'not configured' }, observedAt: now, attemptedAt: now, stale: false },
    { part: 'worker', presence: 'stopped', facts: { id: 'w0rk', status: 'exited', running: false, exitCode: 137, oomKilled: true, restarts: 3, health: 'not configured', finishedAt: now }, observedAt: now, attemptedAt: now, stale: false },
  ],
  events: [{ part: 'worker', kind: 'out-of-memory', container: 'forge-svc-env-game-main-worker', at: now, generation: 2 }],
  eventsKnown: true, eventsSource: "recorded from the Docker host's events",
  logMaxLines: 200, logMaxBytes: 65536,
}

// What each part uses, as the resources read answers it beside diagnostics.
const resources = { reading: { stale: false }, limits: [], limitsKnown: true, limitsSource: 'the Docker daemon',
  parts: ['web', 'worker'].map((part) => ({ part, observed: { part, usedBytes: 1 << 20, limitBytes: 0, cpuCores: 0, cpuLimit: 0, known: true } })) }

type Reply = { status: number; body: unknown }
const serve = (replies: (url: string) => Reply) => {
  const calls: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    calls.push(String(url))
    if (String(url).endsWith('/resources')) return new Response(JSON.stringify(resources))
    const r = replies(String(url))
    return new Response(JSON.stringify(r.body), { status: r.status })
  }))
  return calls
}

const mount = async () => {
  const client = newQueryClient()
  const el = document.createElement('div')
  document.body.appendChild(el)
  const root = createRoot(el)
  await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><TechnicalDetailSwitch /><DeploymentContainers slug="game" name="main" /></MemoryRouter></QueryClientProvider>) })
  await vi.waitFor(() => expect(el.textContent).toContain('worker'))
  await vi.waitFor(() => expect(el.textContent).not.toContain('Reading'))
  return { el, client, done: () => { act(() => root.unmount()); client.clear(); el.remove() } }
}

// A part's row in the containers table, and the words in its status cell.
const row = (el: HTMLElement, part: string) => Array.from(el.querySelectorAll('tbody tr')).find((r) => r.querySelector('td')?.textContent?.startsWith(part))!
const status = (el: HTMLElement, part: string) => row(el, part).querySelector('.phone-state')!.textContent
// Logs is in the row's ⋯ menu, which opens in a popover.
const readLog = async (el: HTMLElement, part: string) => {
  await act(async () => { row(el, part).querySelector<HTMLButtonElement>('.phone-action button')!.click() })
  await act(async () => { [...document.body.querySelectorAll<HTMLButtonElement>('.more-menu-list button')].find(b => b.firstChild?.textContent === 'Logs')!.click() })
}

describe('a deployment’s diagnostics', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('lead with the failing part and what to do, and mark an earlier generation’s event', async () => {
    serve(() => ({ status: 200, body: diagnostics }))
    const { el, done } = await mount()
    expect(status(el, 'worker')).toContain('failed')
    expect(el.textContent).toContain('worker: raise its memory')
    expect(status(el, 'web')).toContain('working')
    expect(el.textContent).not.toContain('generation 2, earlier')
    await act(async () => { el.querySelector<HTMLButtonElement>('[aria-label="Techno Goggles"]')!.click() })
    expect(el.textContent).toContain('generation 2, earlier')
    expect(el.textContent).toContain('not configured')
    await act(async () => { el.querySelector<HTMLButtonElement>('[aria-label="Techno Goggles"]')!.click() })
    done()
  })

  it('read a part’s log only when asked, and show its bounds', async () => {
    const calls = serve((url) => url.includes('/logs')
      ? { status: 200, body: { part: 'worker', container: 'c0ffee', generation: 3, text: '2026-10-16T01:00:00Z worker: killed', truncated: true, maxLines: 200, maxBytes: 65536, readAt: now } }
      : { status: 200, body: diagnostics })
    const { el, client, done } = await mount()
    expect(calls.some((c) => c.includes('/logs'))).toBe(false)
    await readLog(el, 'worker')
    await vi.waitFor(() => expect(el.textContent).toContain('worker: killed'))
    expect(calls.filter((c) => c.includes('/logs'))).toEqual(['/api/v1/projects/game/deployments/main/logs?part=worker'])
    expect(el.textContent).toContain('newest 200 lines')
    expect(el.textContent).toContain('older output not shown')
    // The log's text is held by the view alone: no cache keeps it, even
    // once closed.
    const held = JSON.stringify([...client.getQueryCache().getAll().map((q) => q.state.data), ...client.getMutationCache().getAll().map((m) => m.state.data)])
    expect(held).not.toContain('worker: killed')
    done()
  })

  it('say a refused log read is refused, not empty', async () => {
    serve((url) => url.includes('/logs') ? { status: 403, body: { error: 'needs deployment:operate' } } : { status: 200, body: diagnostics })
    const { el, done } = await mount()
    await readLog(el, 'worker')
    await vi.waitFor(() => expect(el.querySelector('.error')?.textContent).toContain('deployment:operate'))
    expect(el.querySelector('pre')).toBeNull()
    done()
  })

  it('keep the last answer marked out of date when a refresh fails, never as current', async () => {
    serve(() => ({ status: 200, body: diagnostics }))
    const { el, client, done } = await mount()
    serve(() => ({ status: 502, body: { error: 'AppTown is restarting' } }))
    await act(async () => { await client.refetchQueries({ queryKey: getKey(PATH) }).catch(() => {}) })
    await vi.waitFor(() => expect(status(el, 'worker')).toContain('unknown'))
    expect(el.textContent).toContain('worker: raise its memory')
    done()
  })

  it('show a part the host did not answer for as unknown, with the facts last read', async () => {
    const unknown = { ...diagnostics, parts: [diagnostics.parts[0], { ...diagnostics.parts[1], presence: 'unknown', stale: true, reason: 'its container could not be read: it did not answer in time' }] }
    serve(() => ({ status: 200, body: unknown }))
    const { el, done } = await mount()
    expect(status(el, 'worker')).toContain('unknown')
    expect(el.textContent).not.toContain('worker: raise its memory')
    await act(async () => { el.querySelector<HTMLButtonElement>('[aria-label="Techno Goggles"]')!.click() })
    expect(el.textContent).toContain('exited, exit 137, out of memory')
    await act(async () => { el.querySelector<HTMLButtonElement>('[aria-label="Techno Goggles"]')!.click() })
    done()
  })
})
