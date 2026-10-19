// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient, retryDelay } from '../../data'
import { InstanceSecretsPanel, SecretNameStatus } from './InstanceSecrets'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function stubForge(stores: Record<string, string>, unreadable?: { on: boolean }) {
  const calls: { method: string; path: string; body: unknown }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const path = url.replace(/^\/api\/v1/, '')
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    calls.push({ method, path, body })
    if (method === 'GET' && path === '/secrets/names' && unreadable?.on) {
      return new Response(JSON.stringify({ error: "the forge's secrets could not be read: Infisical could not be read" }), { status: 503 })
    }
    if (method === 'GET' && path === '/secrets/names') {
      return new Response(JSON.stringify({ names: Object.keys(stores).sort(), stores }))
    }
    const m = path.match(/^\/secrets\/([A-Z0-9_]+)$/)
    if (m && method === 'PUT') { stores[m[1]] = 'secrets.env'; return new Response(JSON.stringify({ name: m[1], held: true })) }
    if (m && method === 'DELETE') { delete stores[m[1]]; return new Response(JSON.stringify({ name: m[1], held: false })) }
    return new Response(JSON.stringify({ error: 'no route' }), { status: 404 })
  }))
  return calls
}

async function mount(node: React.ReactNode) {
  const el = document.createElement('div')
  document.body.appendChild(el)
  const root = createRoot(el)
  const client = newQueryClient()
  await act(async () => { root.render(<QueryClientProvider client={client}>{node}</QueryClientProvider>) })
  await advance(0)
  return { el, root, client }
}
// The clock is fake in this file: advance runs what is due by then, and
// advance(0) lets answers already on their way reach the page.
const advance = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })
// A server error is retried twice, on the client's schedule, before it fails.
const retries = retryDelay(0) + retryDelay(1)
const button = (el: HTMLElement, text: string) => [...el.querySelectorAll('button')].find((b) => b.textContent === text)!
async function type(input: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

describe('instance secrets', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); document.body.innerHTML = '' })

  // A field naming a secret says whether the forge holds it; one it does not
  // hold is set from a password field, the value sent once in the body and
  // never shown on the page; afterwards the name reads as held.
  it('sets a named secret the forge does not hold, write-only', async () => {
    const calls = stubForge({})
    const { el, root } = await mount(<SecretNameStatus name="DIGITALOCEAN_ACCESS_TOKEN" />)
    expect(el.textContent).toContain('AppTown does not hold this yet')
    await act(async () => { button(el, 'set this secret now').click() })
    const input = el.querySelector('input[type=password]') as HTMLInputElement
    expect(input).toBeTruthy()
    await type(input, 'dop_v1_secret')
    await act(async () => { button(el, 'Set').click() })
    await advance(0)
    expect(calls.filter((c) => c.method === 'PUT')).toEqual([{ method: 'PUT', path: '/secrets/DIGITALOCEAN_ACCESS_TOKEN', body: { value: 'dop_v1_secret' } }])
    expect(el.innerHTML).not.toContain('dop_v1_secret')
    expect(el.textContent).toContain("held in AppTown's secrets file")
    act(() => root.unmount())
  })

  // A name Infisical holds is not offered for setting here: the forge
  // would refuse to shadow it, so the page says where to change it.
  it('sends a name Infisical holds to Infisical', async () => {
    stubForge({ DIGITALOCEAN_ACCESS_TOKEN: 'infisical' })
    const { el, root } = await mount(<SecretNameStatus name="DIGITALOCEAN_ACCESS_TOKEN" />)
    expect(el.textContent).toContain('held in Infisical')
    expect(el.textContent).toContain('change it in Infisical')
    expect(button(el, 'set this secret now')).toBeUndefined()
    act(() => root.unmount())
  })

  // The list shows each name and where it is held; one in the forge's file
  // can be removed after a quiet confirmation.
  it('lists the secrets and removes one from the file', async () => {
    const calls = stubForge({ DEMO_KEY: 'secrets.env', OPENROUTER_API_KEY: 'infisical' })
    const { el, root } = await mount(<InstanceSecretsPanel />)
    expect(el.textContent).toContain('DEMO_KEY')
    expect(el.textContent).toContain('OPENROUTER_API_KEY')
    await act(async () => { button(el, 'remove').click() })
    await act(async () => { button(el, 'Yes').click() })
    await advance(0)
    expect(calls.some((c) => c.method === 'DELETE' && c.path === '/secrets/DEMO_KEY')).toBe(true)
    act(() => root.unmount())
  })

  // Secrets the forge cannot read are unknown, never "none" or "not held":
  // neither the page nor a wizard field offers to set a value then.
  it('shows an unreadable store as unknown, not empty', async () => {
    stubForge({}, { on: true })
    const panel = await mount(<InstanceSecretsPanel />)
    await advance(retries - 1)
    expect(panel.el.textContent).not.toContain('Unknown')
    await advance(1)
    await vi.waitFor(() => expect(panel.el.textContent).toContain('Unknown'))
    expect(panel.el.textContent).not.toContain('holds no secrets')
    expect(panel.el.querySelector('input[aria-label="secret name"]')).toBeNull()
    act(() => panel.root.unmount())
    const field = await mount(<SecretNameStatus name="DIGITALOCEAN_ACCESS_TOKEN" />)
    await advance(retries)
    await vi.waitFor(() => expect(field.el.textContent).toContain('unknown'))
    expect(field.el.textContent).not.toContain('does not hold')
    expect(button(field.el, 'set this secret now')).toBeUndefined()
    act(() => field.root.unmount())
  })

  // A read that fails after one that worked is unknown too: the names kept
  // from before are not offered as current.
  it('does not offer a set on names kept from before a failed read', async () => {
    const unreadable = { on: false }
    stubForge({}, unreadable)
    const field = await mount(<SecretNameStatus name="DIGITALOCEAN_ACCESS_TOKEN" />)
    expect(field.el.textContent).toContain('AppTown does not hold this yet')
    unreadable.on = true
    await act(async () => { void field.client.invalidateQueries() })
    await advance(retries)
    await vi.waitFor(() => expect(field.el.textContent).toContain('unknown'))
    expect(button(field.el, 'set this secret now')).toBeUndefined()
    act(() => field.root.unmount())
  })
})
