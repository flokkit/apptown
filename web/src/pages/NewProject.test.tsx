// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { newQueryClient } from '../data'
import NewProject from './NewProject'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

type Call = { method: string; path: string; body: unknown }

function stubForge(admin: boolean) {
  const calls: Call[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const path = url.replace(/^\/api\/v1/, '')
    const method = init?.method ?? 'GET'
    calls.push({ method, path, body: init?.body })
    const json = (v: unknown) => new Response(JSON.stringify(v))
    if (path === '/authz/me') return json({ user: 'u', instance: { 'identity:manage-auth': admin } })
    if (path === '/packs') return json([])
    if (path === '/dependencies') return json({ dependencies: [] })
    if (path === '/scm/namespaces') return json({ configured: false, namespaces: [] })
    if (path === '/secrets/names') return json({ names: ['GITLAB_TOKEN'], stores: { GITLAB_TOKEN: 'secrets.env' } })
    if (method === 'POST' && path === '/projects') return json({ slug: 'moved' })
    if (method === 'POST' && path.startsWith('/projects/moved/repo/from-')) return json({ slug: 'moved', defaultBranch: 'main' })
    if (path === '/projects/moved/secrets') return json({ declared: true, secrets: [{ name: 'STRIPE_KEY' }, { name: 'SESSION_SECRET', generate: true }] })
    if (path === '/projects/moved/manifest') return json({ project: { slug: 'moved' } })
    return new Response(JSON.stringify({ error: 'no route ' + path }), { status: 404 })
  }))
  return calls
}

async function mount() {
  const el = document.createElement('div')
  document.body.appendChild(el)
  const root = createRoot(el)
  await act(async () => {
    root.render(<QueryClientProvider client={newQueryClient()}><MemoryRouter><NewProject /></MemoryRouter></QueryClientProvider>)
  })
  await settle()
  return { el, root }
}
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (el: HTMLElement, text: string) => [...el.querySelectorAll('button')].find((b) => b.textContent?.trim() === text)
async function type(input: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}
const radio = (el: HTMLElement, label: string) => [...el.querySelectorAll('label')].find((l) => l.textContent?.includes(label))!.querySelector('input')!

// Through the steps to "Where does the code live?", as a bare project.
async function toStep3(el: HTMLElement) {
  await type(el.querySelector('input') as HTMLInputElement, 'moved')
  await act(async () => { button(el, 'Next →')!.click() })
  await act(async () => { button(el, 'Choose a way of working myself →')!.click() })
  await act(async () => { ([...el.querySelectorAll('.pack-card')].find((c) => c.textContent?.includes('none (bare)')) as HTMLElement).click() })
  await act(async () => { button(el, 'Next →')!.click() })
  await settle()
}

describe('new project from existing code', () => {
  afterEach(() => { vi.unstubAllGlobals(); document.body.innerHTML = '' })

  // A chosen bundle is sent as the whole body of the bundle door, the empty
  // repository is not made, and the secrets the code declares are named.
  // Create waits until a file is chosen.
  it('uploads the chosen bundle instead of starting empty', async () => {
    const calls = stubForge(false)
    const { el, root } = await mount()
    await toStep3(el)
    await act(async () => { radio(el, 'Existing code').click() })
    expect(button(el, 'Create project')!.disabled).toBe(true)
    const file = new File([new Uint8Array([35, 32, 118, 50])], 'code.bundle')
    const input = el.querySelector('input[type=file]') as HTMLInputElement
    Object.defineProperty(input, 'files', { value: [file] })
    await act(async () => { input.dispatchEvent(new Event('change', { bubbles: true })) })
    await act(async () => { button(el, 'Create project')!.click() })
    for (let i = 0; i < 20 && !el.textContent?.includes('STRIPE_KEY'); i++) await settle()
    const writes = calls.filter((c) => c.method === 'POST').map((c) => c.path)
    expect(writes).toEqual(['/projects', '/projects/moved/repo/from-bundle?branch='])
    expect(calls.find((c) => c.path.startsWith('/projects/moved/repo/from-bundle'))!.body).toBe(file)
    expect(el.textContent).toContain('STRIPE_KEY')
    expect(el.textContent).not.toContain('SESSION_SECRET')
    act(() => root.unmount())
  })

  // Only an administrator is offered a apptown secret as the git credential;
  // the URL door gets its name, never a value.
  it('offers the git credential to administrators only', async () => {
    stubForge(false)
    let m = await mount()
    await toStep3(m.el)
    await act(async () => { radio(m.el, 'Existing code').click() })
    await act(async () => { radio(m.el, 'an https Git URL').click() })
    expect(m.el.querySelector('select[aria-label="git credential"]')).toBeNull()
    act(() => m.root.unmount()); document.body.innerHTML = ''; vi.unstubAllGlobals()

    const calls = stubForge(true)
    m = await mount()
    await toStep3(m.el)
    await act(async () => { radio(m.el, 'Existing code').click() })
    await act(async () => { radio(m.el, 'an https Git URL').click() })
    await settle()
    const select = m.el.querySelector('select[aria-label="git credential"]') as HTMLSelectElement
    expect(select).toBeTruthy()
    await act(async () => {
      select.value = 'GITLAB_TOKEN'
      select.dispatchEvent(new Event('change', { bubbles: true }))
    })
    await type(m.el.querySelector('input[aria-label="https Git URL"]') as HTMLInputElement, 'https://gitlab.example/team/app.git')
    await act(async () => { button(m.el, 'Create project')!.click() })
    for (let i = 0; i < 20 && !calls.some((c) => c.path === '/projects/moved/repo/from-url'); i++) await settle()
    const sent = calls.find((c) => c.path === '/projects/moved/repo/from-url')!
    expect(JSON.parse(String(sent.body))).toEqual({ url: 'https://gitlab.example/team/app.git', branch: '', credentialSecret: 'GITLAB_TOKEN' })
    act(() => m.root.unmount())
  })
})

// A pack picked before the service catalog has answered still starts with
// the services it proposes once the catalog arrives, and leaving the step
// waits for the catalog (review web-readgate-2026-10-08, W-02: the defaults
// were dropped for good).
describe('a pack picked before the service catalog arrives', () => {
  afterEach(() => vi.unstubAllGlobals())
  it('takes the pack\'s services when the catalog answers', async () => {
    const server = { name: 'web-server', description: 'a server', guidance: [], workflows: [], policies: [], services: [{ name: 'postgres', image: 'postgres:16-alpine' }], origin: 'stock', enabled: true, requires: [], guidanceBytes: 0, generatedBytes: 0, declaration: {} }
    const postgres = { capability: 'postgres', title: 'PostgreSQL', local: { image: 'postgres:16-alpine' }, defaults: { version: '16' } }
    let deliver: (r: Response) => void = () => {}
    const catalog = new Promise<Response>((r) => { deliver = r })
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      const path = url.replace(/^\/api\/v1/, '')
      if (path === '/authz/me') return new Response(JSON.stringify({ user: 'u', instance: {} }))
      if (path === '/packs') return new Response(JSON.stringify([server]))
      if (path === '/dependencies') return catalog
      return new Response(JSON.stringify({ error: 'no route ' + path }), { status: 404 })
    }))
    const { el, root } = await mount()
    try {
      await type(el.querySelector('input') as HTMLInputElement, 'shop')
      await act(async () => { button(el, 'Next →')!.click() })
      await act(async () => { button(el, 'Choose a way of working myself →')!.click() })
      await settle()
      await act(async () => { ([...el.querySelectorAll('.pack-card')].find((c) => c.textContent?.includes('web-server')) as HTMLElement).click() })
      expect(button(el, 'Next →')!.disabled).toBe(true)
      await act(async () => { deliver(new Response(JSON.stringify({ dependencies: [postgres] }))) })
      await settle()
      const tile = [...el.querySelectorAll('[aria-pressed]')].find((t) => t.textContent?.includes('PostgreSQL'))!
      expect(tile.getAttribute('aria-pressed')).toBe('true')
      expect(button(el, 'Next →')!.disabled).toBe(false)
    } finally {
      act(() => root.unmount())
    }
  })
})

// The interview can pick a pack before the pack list has answered (the
// no-model fallback answers at once). Its proposed services come from that
// list, so leaving the step waits for it, and the project is created with
// the services the pack proposes (review web-readgate-r2, W2-01).
describe('an interview that answers before the pack list', () => {
  afterEach(() => vi.unstubAllGlobals())
  it('waits for the pack list, then creates the project with the pack\'s services', async () => {
    const server = { name: 'web-server', description: 'a server', guidance: [], workflows: [], policies: [], services: [{ name: 'postgres', image: 'postgres:16-alpine' }], origin: 'stock', enabled: true, requires: [], guidanceBytes: 0, generatedBytes: 0, declaration: {} }
    const postgres = { capability: 'postgres', title: 'PostgreSQL', local: { image: 'postgres:16-alpine' }, defaults: { version: '16' } }
    let deliver: (r: Response) => void = () => {}
    const packList = new Promise<Response>((r) => { deliver = r })
    const calls: Call[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      const path = url.replace(/^\/api\/v1/, '')
      const method = init?.method ?? 'GET'
      calls.push({ method, path, body: init?.body })
      const json = (v: unknown) => new Response(JSON.stringify(v))
      if (path === '/authz/me') return json({ user: 'u', instance: {} })
      if (path === '/packs') return packList
      if (path === '/dependencies') return json({ dependencies: [postgres] })
      if (path === '/packs/interview') return json({ pack: 'web-server', why: 'it has a server' })
      if (path === '/scm/namespaces') return json({ configured: false, namespaces: [] })
      if (method === 'POST' && path === '/projects') return json({ slug: 'shop' })
      if (method === 'POST' && path === '/projects/shop/repo') return json({ slug: 'shop', defaultBranch: 'main' })
      if (method === 'POST' && path === '/projects/shop/pack') return json({ guidanceImports: 0, workflowsInstalled: [], missing: [] })
      if (method === 'POST' && path === '/projects/shop/services') return json({})
      if (path === '/projects/shop/manifest') return json({ project: { slug: 'shop' } })
      return new Response(JSON.stringify({ error: 'no route ' + path }), { status: 404 })
    }))
    const { el, root } = await mount()
    try {
      await type(el.querySelector('input') as HTMLInputElement, 'shop')
      await act(async () => { button(el, 'Next →')!.click() })
      await act(async () => {
        const area = el.querySelector('textarea') as HTMLTextAreaElement
        Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(area, 'a shop with a database')
        area.dispatchEvent(new Event('input', { bubbles: true }))
      })
      await act(async () => { button(el, 'Next →')!.click() })
      await settle()
      expect(el.textContent).toContain('web-server')
      expect(button(el, 'Next →')!.disabled).toBe(true)
      await act(async () => { deliver(new Response(JSON.stringify([server]))) })
      await settle()
      expect(button(el, 'Next →')!.disabled).toBe(false)
      await act(async () => { button(el, 'Next →')!.click() })
      await act(async () => { button(el, 'Create project')!.click() })
      await settle()
      const services = calls.find((c) => c.method === 'POST' && c.path === '/projects/shop/services')
      expect(services && JSON.parse(String(services.body)).needs).toEqual([{ capability: 'postgres', params: { version: '16' } }])
    } finally {
      act(() => root.unmount())
    }
  })
})

// An AppTown with Infisical set up has two secrets stores: the summary
// shows both, each its own chip (keyed by binding, not capability alone).
describe('the new project summary', () => {
  it('shows every secrets store this AppTown can use', async () => {
    const errors = vi.spyOn(console, 'error').mockImplementation(() => {})
    const { ManifestView } = await import('./NewProject')
    const bind = (binding: string, provider: string) => ({ capability: 'secrets', binding, provider, version: 1, configHash: 'h' })
    const m = {
      project: { slug: 'shop', name: 'Shop' }, repo: { backed: true }, packs: [], workflows: [], guidance: [], guidanceHash: 'x',
      policies: [], bindings: [bind('built-in-secrets', 'internal'), bind('infisical', 'infisical')],
    }
    const el = document.createElement('div')
    document.body.appendChild(el)
    const root = createRoot(el)
    await act(async () => {
      root.render(<MemoryRouter><ManifestView m={m} onBuildV0={() => {}} onConsole={() => {}} /></MemoryRouter>)
    })
    expect(el.textContent).toContain('secrets → built-in-secrets v1')
    expect(el.textContent).toContain('secrets → infisical v1')
    expect(errors.mock.calls.some((c) => String(c[0]).includes('same key'))).toBe(false)
    act(() => root.unmount())
    el.remove()
    errors.mockRestore()
  })
})
