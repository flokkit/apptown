// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../../data'
import { ResourceLines } from './Deployments'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

afterEach(() => vi.unstubAllGlobals())

it('keeps a supplied service visible but offers overrides only for app parts and contract needs', async () => {
  const observed = (part: string) => ({ part, usedBytes: 16 * 2 ** 20, limitBytes: 512 * 2 ** 20, known: true })
  const resources = { recorded: true, profile: 'default', how: 'default', parts: [
    { part: 'sidecar', suppliedService: true, public: false, memory: '512Mi', cpu: 'no limit', overrideApplied: false, observed: observed('sidecar') },
    { part: 'web', public: true, memory: '512Mi', cpu: 'no limit', overrideApplied: false, observed: observed('web') },
    { part: 'postgres', need: true, public: false, memory: '256Mi', cpu: 'no limit', overrideApplied: false, observed: observed('postgres') },
  ], limits: [], limitsKnown: true, limitsSource: 'Docker events' }
  const secrets = { declared: false, spec: 'secretspec.toml', stores: [], deployments: [], choosable: ['default'] }
  const writes: { part: string }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'PUT') { writes.push(JSON.parse(String(init.body))); return new Response(JSON.stringify({ notYet: 'at the next deploy' })) }
    return new Response(JSON.stringify(String(url).includes('/resources') ? resources : secrets))
  }))
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/game/deployments/cloud/resources'), resources)
  client.setQueryData(getKey('/projects/game/secrets'), secrets)
  const el = document.createElement('div')
  const root = createRoot(el)
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><ResourceLines slug="game" name="cloud" /></MemoryRouter></QueryClientProvider>) })
    expect(el.textContent).toContain('sidecar (workflow service)')
    expect(el.textContent).toContain('Workflow service limits are set in the workflow service declaration.')
    const sidecar = Array.from(el.querySelectorAll('tbody tr')).find((r) => r.textContent?.includes('sidecar'))!
    expect(sidecar.textContent).toContain('512Mi')
    expect(sidecar.textContent).toContain('16')
    const button = (label: string) => Array.from(el.querySelectorAll('button')).find((b) => b.textContent === label)!
    await act(async () => { button('Override memory…').click() })
    expect(Array.from(el.querySelectorAll('select option')).map((o) => o.getAttribute('value'))).toEqual(['web', 'postgres'])
    const inputs = Array.from(el.querySelectorAll('input'))
    await act(async () => {
      for (const [input, value] of [[inputs[0], '128Mi'], [inputs[1], 'measured app cap']] as const) {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
        input.dispatchEvent(new Event('input', { bubbles: true }))
      }
    })
    await act(async () => { button('Override and redeploy').click() })
    expect(writes).toHaveLength(1)
    expect(writes[0].part).toBe('web')
  } finally { act(() => root.unmount()); client.clear() }
})
