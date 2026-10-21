// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../../../data'
import { settled } from '../../../settled'
import { AdoptPanel } from './Working'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
afterEach(() => vi.unstubAllGlobals())

// The server deliberately keeps the Change when starting fails. Settings
// must preserve the failure explanation rather than consume it as success.
it.each([
  { notStarted: 'the opener lacks run authority' },
  { workflow: 'quick-change', runId: '00000000-0042-7000-8000-000000000000', runError: 'clone refused' },
])('Settings reports the adoption start outcome: %j', async (outcome) => {
  const candidate = {
    expectedRevision: 1, packs: [{ pack: 'team', version: 2 }], additions: [],
    problems: [], weakens: [], moves: [], kept: [], added: [], changed: [],
    removed: [], older: [], lanesAdded: [], lanesRemoved: [],
    newChecks: [{ block: 'docs', check: 'readme', title: 'Readme' }],
  }
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const value = String(url).endsWith('/prepare') ? candidate
      : init?.method === 'POST' ? { changeId: '00000000-0041-7000-8000-000000000000', candidate, ...outcome }
      : { packs: [] }
    return new Response(JSON.stringify(value))
  }))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  const click = async (label: string) => {
    const b = [...el.querySelectorAll('button')].find((b) => b.textContent === label)
    expect(b, label).toBeDefined()
    await act(async () => b!.click())
    await settled(client)
  }
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><AdoptPanel slug="demo" packs={[{ pack: 'team', version: 1, recommended: 2, reason: '' }]} onChanged={() => {}} /></MemoryRouter></QueryClientProvider>))
    await settled(client)
    await click('Review team v1 → v2')
    await click('Adopt through a change')
    await vi.waitFor(() => expect(client.isMutating()).toBe(0))
    expect(el.textContent).toContain(outcome.notStarted ?? outcome.runError)
  } finally {
    act(() => root.unmount())
    client.clear()
  }
})
