// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../../../data'
import { SecretsTab } from './Secrets'

vi.mock('../../../instancePages', () => ({ useHolds: () => true }))
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

afterEach(() => vi.unstubAllGlobals())

function fixture(shared = false) {
  const secret = { name: 'NEXTUP_PASSWORD', required: true, bound: true, store: 'internal', version: '1', generated: shared, agentEligible: !shared, agentVisible: false }
  const data = { declared: true, spec: 'secretspec.toml', project: 'Demo', secrets: [{ name: secret.name, consumers: ['web'], allParts: true }], profiles: ['preview', 'default'], stores: [{ name: 'internal', title: 'Built-in store', available: true }], deployments: [
    { name: 'preview', chosen: 'preview', profile: 'preview', secrets: [secret], missing: [] },
    { name: 'main', chosen: 'default', profile: 'default', secrets: [{ ...secret, agentEligible: false }], missing: [] },
  ] }
  const sent: { url: string; method?: string; body?: unknown }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method && init.method !== 'GET') {
      sent.push({ url, method: init.method, body: JSON.parse(String(init.body)) })
      if (url.endsWith('/agent-visibility')) secret.agentVisible = (JSON.parse(String(init.body)) as { agentVisible: boolean }).agentVisible
      return new Response(JSON.stringify({ ok: true }))
    }
    return new Response(JSON.stringify(data))
  }))
  const client = newQueryClient()
  const el = document.createElement('div')
  document.body.append(el)
  const root = createRoot(el)
  act(() => root.render(<QueryClientProvider client={client}><MemoryRouter><SecretsTab slug="demo" /></MemoryRouter></QueryClientProvider>))
  return { el, sent, cleanup: () => { act(() => root.unmount()); el.remove(); client.clear() } }
}

it('offers an off-by-default toggle only for preview and warns when on and when changing its value', async () => {
  const f = fixture()
  try {
    await vi.waitFor(() => expect(f.el.querySelectorAll('input[type=checkbox]')).toHaveLength(1))
    const toggle = f.el.querySelector('input[type=checkbox]') as HTMLInputElement
    expect(toggle.checked).toBe(false)
    expect(toggle.parentElement?.textContent).toContain('Agents working on changes can see this')
    await act(async () => toggle.click())
    expect(f.sent[0]).toMatchObject({ method: 'PUT', url: '/api/v1/projects/demo/secrets/preview/NEXTUP_PASSWORD/agent-visibility', body: { agentVisible: true } })
    await vi.waitFor(() => expect(f.el.textContent).toContain('agents can see this'))
    expect(f.el.textContent).toContain('Don’t reuse a password from a real deployment.')
    const change = [...f.el.querySelectorAll('button')].find((b) => b.textContent === 'Change…')!
    await act(async () => change.click())
    expect(document.querySelector('[role=dialog]')?.textContent).toContain('Don’t reuse a password from a real deployment.')
  } finally { f.cleanup() }
})

it('explains shared storage and offers an independent generated preview value without a toggle', async () => {
  const f = fixture(true)
  try {
    await vi.waitFor(() => expect(f.el.textContent).toContain('This value is shared with main. Give previews their own value to let agents see it'))
    expect(f.el.querySelectorAll('input[type=checkbox]')).toHaveLength(0)
    const separate = [...f.el.querySelectorAll('button')].find((b) => b.textContent === 'Give previews their own value')!
    await act(async () => separate.click())
    expect(f.sent[0]).toMatchObject({ method: 'POST', url: '/api/v1/projects/demo/secrets/preview/NEXTUP_PASSWORD/separate-preview' })
  } finally { f.cleanup() }
})
