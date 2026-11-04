// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient, retryDelay } from '../../data'
import { InstanceSecretsPanel, SecretNameStatus } from './InstanceSecrets'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function stubAppTown(stores: Record<string, string>, unreadable?: { on: boolean }) {
  const calls: { method: string; path: string; body: unknown }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const path = url.replace(/^\/api\/v1/, '')
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    calls.push({ method, path, body })
    if (method === 'GET' && path === '/secrets/names' && unreadable?.on) {
      return new Response(JSON.stringify({ error: "AppTown's secrets could not be read: Infisical could not be read" }), { status: 503 })
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

  // A field naming a secret says whether AppTown holds it; one it does not
  // hold is set from a password field, the value sent once in the body and
  // never shown on the page; afterwards the name reads as held.
  it('sets a named secret AppTown does not hold, write-only', async () => {
    const calls = stubAppTown({})
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

  // A name Infisical holds is not offered for setting here: AppTown
  // would refuse to shadow it, so the page says where to change it.
  it('sends a name Infisical holds to Infisical', async () => {
    stubAppTown({ DIGITALOCEAN_ACCESS_TOKEN: 'infisical' })
    const { el, root } = await mount(<SecretNameStatus name="DIGITALOCEAN_ACCESS_TOKEN" />)
    expect(el.textContent).toContain('held in Infisical')
    expect(el.textContent).toContain('change it in Infisical')
    expect(button(el, 'set this secret now')).toBeUndefined()
    act(() => root.unmount())
  })

  // The list shows each name and where it is held; one in AppTown's file
  // can be removed after a quiet confirmation.
  it('lists the secrets and removes one from the file', async () => {
    const calls = stubAppTown({ DEMO_KEY: 'secrets.env', OPENROUTER_API_KEY: 'infisical' })
    const { el, root } = await mount(<InstanceSecretsPanel />)
    expect(el.textContent).toContain('DEMO_KEY')
    expect(el.textContent).toContain('OPENROUTER_API_KEY')
    await act(async () => { button(el, 'Remove').click() })
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
    expect(el.textContent).toContain('Anything that names it will no longer find it.')
    await act(async () => { button(el, 'Yes').click() })
    await advance(0)
    expect(calls.some((c) => c.method === 'DELETE' && c.path === '/secrets/DEMO_KEY')).toBe(true)
    act(() => root.unmount())
  })

  it('adds at the top with validated name and value, and cancels cleanly', async () => {
    const calls = stubAppTown({ EXISTING: 'secrets.env' })
    const { el, root } = await mount(<InstanceSecretsPanel />)
    expect(el.querySelector('input')).toBeNull()
    expect(el.textContent).not.toContain('Set a secret')
    await act(async () => { button(el, 'Add a credential').click() })
    const editor = el.querySelector('[role=list]')!.firstElementChild!
    expect(editor.tagName).toBe('FORM')
    const name = editor.querySelector('input[aria-label="secret name"]') as HTMLInputElement
    const value = editor.querySelector('input[type=password]') as HTMLInputElement
    expect(document.activeElement).toBe(name)
    expect(button(el, 'Save').disabled).toBe(true)
    await type(name, '9bad')
    await type(value, 'private-value')
    expect(el.textContent).toContain('starting with a letter')
    expect(button(el, 'Save').disabled).toBe(true)
    await type(name, ' new_key ')
    await act(async () => { button(el, 'Save').click() })
    await advance(0)
    expect(calls.filter((c) => c.method === 'PUT')).toEqual([{ method: 'PUT', path: '/secrets/NEW_KEY', body: { value: 'private-value' } }])
    expect(el.querySelector('form')).toBeNull()
    expect(el.innerHTML).not.toContain('private-value')
    expect([...el.querySelectorAll('code')].map((n) => n.textContent)).toEqual(['EXISTING', 'NEW_KEY'])
    await act(async () => { button(el, 'Add a credential').click() })
    await type(el.querySelector('input[aria-label="secret name"]') as HTMLInputElement, 'CANCELLED')
    await act(async () => { button(el, 'Cancel').click() })
    expect(el.querySelector('form')).toBeNull()
    await act(async () => { button(el, 'Add a credential').click() })
    expect((el.querySelector('input[aria-label="secret name"]') as HTMLInputElement).value).toBe('')
    act(() => root.unmount())
  })

  it('states a common store once with normal Replace and Remove buttons', async () => {
    const calls = stubAppTown({ FIRST: 'secrets.env', SECOND: 'secrets.env' })
    const { el, root } = await mount(<InstanceSecretsPanel />)
    expect(el.textContent!.match(/AppTown's secrets file/g)).toHaveLength(1)
    expect(el.textContent).toContain("All stored in AppTown's secrets file")
    expect(el.textContent).not.toContain('Infisical')
    expect(el.textContent).not.toContain('Used by')
    for (const row of el.querySelectorAll('[role=listitem]')) {
      for (const label of ['Replace', 'Remove']) {
        const action = button(row as HTMLElement, label)
        expect(action).toBeTruthy()
        expect(action.classList.contains('subtle')).toBe(false)
      }
      expect(row.querySelector('summary')).toBeNull()
    }
    await act(async () => { button(el, 'Replace').click() })
    await type(el.querySelector('input[type=password]') as HTMLInputElement, 'replacement')
    await act(async () => { button(el, 'Set').click() })
    expect(calls.some((c) => c.method === 'PUT' && c.path === '/secrets/FIRST')).toBe(true)
    act(() => root.unmount())
  })

  it('shows mixed stores and change advice only on Infisical rows', async () => {
    stubAppTown({ LOCAL: 'secrets.env', REMOTE: 'infisical' })
    const { el, root } = await mount(<InstanceSecretsPanel />)
    const rows = [...el.querySelectorAll('[role=listitem]')]
    expect(el.textContent).not.toContain('All stored')
    expect(rows[0].textContent).toContain("AppTown's secrets file")
    expect(rows[0].textContent).not.toContain('Infisical')
    expect(rows[1].textContent).toContain('change it in Infisical')
    expect(rows[1].querySelector('button')).toBeNull()
    expect(el.textContent!.match(/change it in Infisical/g)).toHaveLength(1)
    await act(async () => { button(el, 'Add a credential').click() })
    await type(el.querySelector('input[aria-label="secret name"]') as HTMLInputElement, 'REMOTE')
    await type(el.querySelector('input[type=password]') as HTMLInputElement, 'value')
    expect(button(el, 'Save').disabled).toBe(true)
    act(() => root.unmount())
  })

  it('hides the list and open editor after a failed refresh', async () => {
    const unreadable = { on: false }
    stubAppTown({ EXISTING: 'secrets.env' }, unreadable)
    const { el, root, client } = await mount(<InstanceSecretsPanel />)
    await act(async () => { button(el, 'Add a credential').click() })
    unreadable.on = true
    await act(async () => { void client.invalidateQueries() })
    await advance(retries)
    expect(el.textContent).toContain('Unknown')
    expect(el.textContent).not.toContain('EXISTING')
    expect(el.querySelector('input')).toBeNull()
    expect(button(el, 'Add a credential')).toBeUndefined()
    act(() => root.unmount())
  })

  // Secrets AppTown cannot read are unknown, never "none" or "not held":
  // neither the page nor a wizard field offers to set a value then.
  it('shows an unreadable store as unknown, not empty', async () => {
    stubAppTown({}, { on: true })
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
    stubAppTown({}, unreadable)
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
