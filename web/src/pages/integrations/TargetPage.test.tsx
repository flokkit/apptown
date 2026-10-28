// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import type { RegistryReading, TargetView } from '../../wire.gen'
import { newQueryClient } from '../../data'
import { MemoryRouter } from 'react-router-dom'
import { CopyRunnerLine, EditTarget, ProviderAssociationSection, RegistryLine, Unaccounted } from './TargetPage'
import type { InfrastructureView } from '../../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const target = (domain: string, disks: boolean, settingsRevision = 0) => ({
  name: 'lan', ingress: { domain }, needsMode: 'emulators', settingsRevision, kindInfo: { environment: false },
  capabilities: { persistentVolumes: disks, previews: false },
} as unknown as TargetView)

describe('the target editor', () => {
  // Save sends all four fields, so the editor must open on the target as it
  // is now, not as it was when the page first rendered.
  it('opens on the target as it is now', async () => {
    const el = document.createElement('div')
    const root = createRoot(el)
    const client = newQueryClient()
    const render = (t: TargetView) => act(async () => {
      root.render(<QueryClientProvider client={client}><EditTarget t={t} onSaved={() => {}} /></QueryClientProvider>)
    })
    await render(target('old.example', false))
    await render(target('new.example', true))
    await act(async () => { [...el.querySelectorAll('button')].find((b) => b.textContent === 'Edit target')!.click() })
    const [domain, disks] = [...el.querySelectorAll('input')] as HTMLInputElement[]
    expect(domain.value).toBe('new.example')
    expect(disks.checked).toBe(true)
    act(() => root.unmount())
  })

  it('cannot be opened on an answer the page has not read itself', async () => {
    const el = document.createElement('div')
    const root = createRoot(el)
    const client = newQueryClient()
    const render = (fresh: boolean) => act(async () => {
      root.render(<QueryClientProvider client={client}><EditTarget t={target('held.example', false)} current={fresh} onSaved={() => {}} /></QueryClientProvider>)
    })
    const edit = () => [...el.querySelectorAll('button')].find((b) => b.textContent === 'Edit target')!
    await render(false)
    expect(edit().disabled).toBe(true)
    await render(true)
    expect(edit().disabled).toBe(false)
    act(() => root.unmount())
  })

  afterEach(() => { vi.unstubAllGlobals() })

  // Only what the person changed is sent, with the revision they started
  // from; a conflict keeps their edits and shows what moved.
  it('saves only the changed settings and keeps them through a conflict', async () => {
    const sent: Record<string, unknown>[] = []
    let conflict = true
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
      sent.push(JSON.parse(String(init?.body)))
      return conflict
        ? new Response(JSON.stringify({ error: 'moved' }), { status: 409, headers: { 'X-AppTown-Deny-Code': 'settings-moved' } })
        : new Response('{"ok":true}')
    }))
    const el = document.createElement('div')
    const root = createRoot(el)
    const client = newQueryClient()
    const render = (t: TargetView) => act(async () => {
      root.render(<QueryClientProvider client={client}><EditTarget t={t} onSaved={() => {}} /></QueryClientProvider>)
    })
    const button = (text: string) => [...el.querySelectorAll('button')].find((b) => b.textContent === text)!
    await render(target('apps.example', false, 2))
    await act(async () => { button('Edit target').click() })
    await act(async () => { (el.querySelectorAll('input')[1] as HTMLInputElement).click() })
    await act(async () => { button('Save').click() })
    expect(sent[0]).toEqual({ expectedRevision: 2, persistentVolumes: true })
    await vi.waitFor(() => expect(el.textContent).toContain("Someone changed this target's settings"))
    await render(target('other.example', false, 3))
    expect(el.textContent).toContain('domain is now “other.example”')
    await act(async () => { button('Keep my edits on top of theirs').click() })
    expect((el.querySelectorAll('input')[0] as HTMLInputElement).value).toBe('other.example')
    expect((el.querySelectorAll('input')[1] as HTMLInputElement).checked).toBe(true)
    conflict = false
    await act(async () => { button('Save').click() })
    expect(sent[1]).toEqual({ expectedRevision: 3, persistentVolumes: true })
    act(() => root.unmount())
  })

  // A Portainer target's environment is set here; a Docker host has none.
  it('edits the Portainer environment of a Portainer target only', async () => {
    const sent: Record<string, unknown>[] = []
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
      sent.push(JSON.parse(String(init?.body)))
      return new Response('{"ok":true}')
    }))
    const el = document.createElement('div')
    const root = createRoot(el)
    const client = newQueryClient()
    const render = (t: TargetView) => act(async () => {
      root.render(<QueryClientProvider client={client}><EditTarget t={t} onSaved={() => {}} /></QueryClientProvider>)
    })
    const button = (text: string) => [...el.querySelectorAll('button')].find((b) => b.textContent === text)!
    const field = () => [...el.querySelectorAll('label')].find((l) => l.textContent?.includes('Portainer environment'))?.querySelector('input') as HTMLInputElement | undefined
    await render({ ...target('apps.example', false, 4), kind: 'docker-host', address: {} } as unknown as TargetView)
    await act(async () => { button('Edit target').click() })
    expect(field()).toBeUndefined()
    act(() => root.unmount())

    const root2 = createRoot(el)
    await act(async () => {
      root2.render(<QueryClientProvider client={client}><EditTarget t={{ ...target('apps.example', false, 4), kind: 'portainer', kindInfo: { environment: true }, address: { environment: 'prd-forge' } } as unknown as TargetView} onSaved={() => {}} /></QueryClientProvider>)
    })
    await act(async () => { button('Edit target').click() })
    expect(field()!.value).toBe('prd-forge')
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    // Typed a key at a time: a space inside the name stays.
    await act(async () => { setValue.call(field()!, ''); field()!.dispatchEvent(new Event('input', { bubbles: true })) })
    for (const key of 'Production Docker') {
      await act(async () => { setValue.call(field()!, field()!.value + key); field()!.dispatchEvent(new Event('input', { bubbles: true })) })
    }
    await act(async () => { button('Save').click() })
    expect(sent[0]).toEqual({ expectedRevision: 4, environment: 'Production Docker' })
    act(() => root2.unmount())
  })
})

// The registry line says the plan and how many repositories are used, and
// that the plan is full when it is; an unreadable registry is unknown with
// why, never numbers; each with when it was read.
describe('the registry line', () => {
  const show = (r: RegistryReading) => {
    const el = document.createElement('div')
    const root = createRoot(el)
    act(() => root.render(<RegistryLine r={r} />))
    const text = el.textContent ?? ''
    act(() => root.unmount())
    return text
  }
  const readAt = new Date(Date.now() - 3 * 3600 * 1000).toISOString()
  it('says a full plan is full', () => {
    const text = show({ registry: 'flokkit', tier: 'basic', included: 5, used: 5, full: true, readAt })
    expect(text).toContain('Container registry flokkit: basic plan, 5 of 5 repositories')
    expect(text).toContain('plan full')
    expect(text).toContain('read 3')
  })
  it('says room without calling it full', () => {
    const text = show({ registry: 'flokkit', tier: 'basic', included: 5, used: 2, full: false, readAt })
    expect(text).toContain('2 of 5 repositories')
    expect(text).not.toContain('plan full')
  })
  it('says when the gate image takes one of the repositories', () => {
    const text = show({ registry: 'flokkit', tier: 'basic', included: 5, used: 2, full: false, gate: true, readAt })
    expect(text).toContain('2 of 5 repositories')
    expect(text).toContain("one holds AppTown's gate image")
    expect(show({ registry: 'flokkit', tier: 'basic', included: 5, used: 2, full: false, readAt })).not.toContain('gate image')
  })
  it('says an unreadable registry is unknown, with why', () => {
    const text = show({ registry: 'flokkit', included: 0, used: 0, full: false, unknown: 'DigitalOcean answered 500', readAt })
    expect(text).toContain('unknown — DigitalOcean answered 500')
    expect(text).not.toContain('repositories')
  })
})

// An App Platform target says, from AppTown's own answer, whether
// backups and restore tests of its deployments can run: with no copy
// runner, that they cannot and why; with one, which host it is.
describe('the copy runner line', () => {
  const why = "Backups and restore tests need a copy runner: a Docker host that runs the copy commands. An administrator can choose one in the target's settings."
  const render = async (t: TargetView) => {
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => { root.render(<MemoryRouter><CopyRunnerLine t={t} /></MemoryRouter>) })
    return { el, done: () => act(() => root.unmount()) }
  }
  it('says backups and restore tests are unavailable, and why, with none chosen', async () => {
    const { el, done } = await render({ name: 'do-syd', kind: 'do-app', address: {}, copies: { available: false, reason: 'no-copy-runner', why } } as unknown as TargetView)
    expect(el.textContent).toContain('unavailable')
    expect(el.textContent).toContain(why)
    done()
  })
  it('names the copy runner when one is chosen', async () => {
    const { el, done } = await render({ name: 'do-syd', kind: 'do-app', address: { copyRunner: 'lan' }, copies: { available: true } } as unknown as TargetView)
    expect(el.textContent).toBe('Copy runner: lan')
    expect(el.querySelector('a[href="/infrastructure/lan"]')).not.toBeNull()
    done()
  })
})

// "Everything at the target is accounted for" is the provider's answer, never
// the absence of one: a failed first read shows the error and claims nothing;
// a failed refresh shows the error and how old the list is.
describe('what is unaccounted for at a target', () => {
  const show = (v: Partial<InfrastructureView>) => {
    const el = document.createElement('div')
    const root = createRoot(el)
    const here = { target: 'lan', kind: 'docker-host', family: 'docker', origin: 'attached', status: 'ready', facts: '', place: '', vendor: '', live: false, refreshing: false, resources: [], ...v } as InfrastructureView
    act(() => root.render(<QueryClientProvider client={newQueryClient()}><MemoryRouter><Unaccounted here={here} onChanged={() => {}} /></MemoryRouter></QueryClientProvider>))
    const text = el.textContent ?? ''
    act(() => root.unmount())
    return text
  }
  const claim = { kind: 'container', name: 'forge-env-shop-main', record: 'recorded' } as InfrastructureView['resources'][number]
  const allClear = 'Everything at the target is accounted for'
  it('claims nothing when the first read failed', () => {
    for (const resources of [[claim], []]) {
      const text = show({ live: false, error: 'ssh: connection refused', resources })
      expect(text).toContain('ssh: connection refused')
      expect(text).not.toContain(allClear)
    }
  })
  it('says how old the list is when a refresh failed', () => {
    const asOf = new Date(Date.now() - 2 * 3600 * 1000).toISOString()
    const text = show({ live: false, error: 'ssh: connection refused', asOf, resources: [claim] })
    expect(text).toContain('ssh: connection refused')
    expect(text).toContain('showing the inventory read 2')
    expect(text).not.toContain(allClear)
  })
  it('says all accounted for only on an answer', () => {
    expect(show({ live: true, asOf: new Date().toISOString(), resources: [claim] })).toContain(allClear)
    expect(show({ live: false, resources: [] })).toContain('checking')
  })
})

describe('provider association confirmation', () => {
  const t = { ...target('apps.example', true, 4), id: 'target-id', name: 'office' } as TargetView
  const scope = { version: 1, apiBase: 'https://portainer.example', endpointId: 23, daemonId: 'daemon-id' }
  const preview = {
    targetId: t.id, connectionId: 'connection-id', revision: 4, scope, associated: false,
    resources: [{ deploymentId: 'deployment-id', scope, stackId: 90, stackName: 'shop-office' }],
  }
  afterEach(() => { vi.unstubAllGlobals() })
  const mount = async () => {
    const el = document.createElement('div'), root = createRoot(el), client = newQueryClient(), saved = vi.fn()
    client.setDefaultOptions({ queries: { retry: false } })
    const render = async (target = t, current = true) => act(async () => {
      root.render(<QueryClientProvider client={client}><ProviderAssociationSection t={target} current={current} onSaved={saved} /></QueryClientProvider>)
    })
    const button = (label: string) => [...el.querySelectorAll('button')].find((b) => b.textContent === label)!
    const click = async (label: string) => act(async () => { button(label).click() })
    await render()
    return { el, render, button, click, saved, close: () => act(() => { root.unmount(); client.clear() }) }
  }

  it('reads on inspection and confirms exactly the displayed associations', async () => {
    const posts: unknown[] = []
    const fetch = vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.method === 'POST') { posts.push(JSON.parse(String(init.body))); return new Response(JSON.stringify({ ...preview, associated: true })) }
      return new Response(JSON.stringify(preview))
    })
    vi.stubGlobal('fetch', fetch)
    const h = await mount()
    expect(fetch).not.toHaveBeenCalled()
    await h.click('Inspect provider association')
    await vi.waitFor(() => expect(h.el.textContent).toContain('shop-office'))
    for (const value of [scope.apiBase, scope.daemonId, 'deployment-id', '90']) expect(h.el.textContent).toContain(value)
    expect(posts).toEqual([])
    await vi.waitFor(() => expect(h.button('Confirm provider association').disabled).toBe(false))
    await h.click('Confirm provider association')
    expect(posts).toEqual([{ connectionId: preview.connectionId, endpointId: 23, expectedRevision: 4, expected: scope, resources: preview.resources }])
    expect(h.saved).toHaveBeenCalledOnce()
    expect(h.el.textContent).toContain('Provider association confirmed')
    h.close()
  })

  it('blocks stale observations and discards inspection on settings or target changes', async () => {
    let fail = false
    vi.stubGlobal('fetch', vi.fn(async () => fail
      ? new Response('{"error":"provider unavailable"}', { status: 409 })
      : new Response(JSON.stringify(preview))))
    const h = await mount()
    await h.click('Inspect provider association')
    await vi.waitFor(() => expect(h.button('Confirm provider association')?.disabled).toBe(false))
    await h.render(t, false)
    expect(h.button('Confirm provider association').disabled).toBe(true)
    await h.render()
    fail = true
    await h.click('Inspect again')
    await vi.waitFor(() => expect(h.el.textContent).toContain('out of date'))
    expect(h.el.textContent).toContain('shop-office')
    expect(h.button('Confirm provider association').disabled).toBe(true)
    await h.render({ ...t, settingsRevision: 5 })
    expect(h.el.textContent).not.toContain('shop-office')
    expect(h.button('Confirm provider association')).toBeUndefined()
    expect(h.button('Inspect provider association')).toBeDefined()
    await h.render({ ...t, id: 'other-id', name: 'other' })
    expect(h.button('Confirm provider association')).toBeUndefined()
    h.close()
  })

  it.each([403, 409])('shows initial %s without claiming an empty association', async (status) => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{"error":"inspection refused"}', { status })))
    const h = await mount()
    await h.click('Inspect provider association')
    await vi.waitFor(() => expect(h.el.textContent).toContain(status === 403 ? 'Not available to you' : 'inspection refused'))
    expect(h.button('Confirm provider association')).toBeUndefined()
    expect(h.el.textContent).not.toContain('No existing stack associations')
    h.close()
  })

  it('keeps a failed confirmation visible and requires another inspection', async () => {
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => init?.method === 'POST'
      ? new Response('{"error":"provider resource observations changed"}', { status: 409 })
      : new Response(JSON.stringify(preview))))
    const h = await mount()
    await h.click('Inspect provider association')
    await vi.waitFor(() => expect(h.button('Confirm provider association')?.disabled).toBe(false))
    await h.click('Confirm provider association')
    expect(h.saved).not.toHaveBeenCalled()
    await vi.waitFor(() => expect(h.el.textContent).toContain('provider resource observations changed'))
    expect(h.button('Confirm provider association').disabled).toBe(true)
    await h.click('Inspect again')
    await vi.waitFor(() => expect(h.button('Confirm provider association').disabled).toBe(false))
    h.close()
  })
})
