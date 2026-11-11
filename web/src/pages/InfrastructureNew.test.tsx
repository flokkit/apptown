import { withFeatures } from '../features.fixture'
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { getKey, newQueryClient } from '../data'
import type { Target, TargetPage } from '../wire.gen'
import { InfrastructureNew, OfferStep } from './InfrastructureNew'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const target = (revision = 7, domain = 'old.example'): Target => ({
  id: 'target-id', provider: '', origin: 'attached', status: 'ready',
  name: 'test-host', displayLabel: 'Test host', kind: 'docker-host', ingress: { domain }, address: {},
  settingsRevision: revision, needsMode: 'emulators',
  capabilities: { keepsDataDir: true, ownLiveness: false, rollout: [], needs: [] },
})
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
const disposals: (() => void)[] = []
afterEach(() => { disposals.splice(0).forEach((f) => f()); vi.unstubAllGlobals() })
async function setup(options: { conflict?: boolean; failOffer?: boolean; failRead?: boolean } = {}) {
  const writes: { method: string; body: Record<string, unknown> }[] = []
  let current = target(), conflict = options.conflict, failOffer = options.failOffer, failRead = options.failRead
  const client = newQueryClient()
  client.setDefaultOptions({ queries: { retry: false } })
  client.setQueryData(getKey('/targets/test-host'), { target: { ...current, kindInfo: { environment: false } }, history: [], builtIn: false })
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string, init?: RequestInit) => {
    const method = init?.method || 'GET'
    if (method === 'GET') {
      if (url === '/api/v1/targets') return json({ targets: [], groups: ['team'] })
      if (failRead) return json({ error: 'unavailable' }, 503)
      return json({ target: current, history: [], builtIn: false })
    }
    const body = JSON.parse(String(init?.body))
    writes.push({ method, body })
    if (method === 'PATCH') {
      if (conflict) {
        current = { ...target(8, 'their.example'), capabilities: { ...current.capabilities, keepsDataDir: false } }
        return new Response('{"error":"settings moved"}', { status: 409, headers: { 'X-AppTown-Deny-Code': 'settings-moved' } })
      }
      if (body.expectedRevision !== current.settingsRevision) return json({ error: 'revision required' }, 422)
      current = { ...current, settingsRevision: current.settingsRevision + 1, ingress: { domain: String(body.domain).replace(/^\*\./, '') } }
      return json({ ok: true, target: current })
    }
    if (failOffer) return json({ error: 'offer unavailable' }, 503)
    return json({ ok: true })
  })))
  const el = document.createElement('div'); document.body.append(el)
  const root = createRoot(el), done = vi.fn()
  disposals.push(() => { act(() => root.unmount()); client.clear(); el.remove() })
  await act(async () => { root.render(<MemoryRouter><QueryClientProvider client={client}><OfferStep target={target()} onDone={done} /></QueryClientProvider></MemoryRouter>) })
  const button = (name: string) => [...el.querySelectorAll('button')].find((b) => b.textContent === name)!
  const click = async (name: string) => { await act(async () => { button(name).click() }); await act(async () => { await new Promise((r) => setTimeout(r, 20)) }) }
  const domain = () => el.querySelector<HTMLInputElement>('input[placeholder="apps.example.com"]')!
  const edit = async (value: string) => { await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(domain(), value)
    domain().dispatchEvent(new Event('input', { bubbles: true }))
  }) }
  return { el, client, writes, done, button, click, domain, edit,
    allow: () => { conflict = false; failOffer = false; failRead = false },
    failRead: () => { failRead = true } }
}

describe('target wizard completion', () => {
  it('refuses an invalid offer before saving edited settings and retains both drafts', async () => {
    const h = await setup(); await h.edit('mine.example')
    const cap = h.el.querySelector<HTMLInputElement>('input[placeholder="no limit"]')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(cap, 'abc')
      cap.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await h.click('Finish')
    expect(h.writes).toEqual([]); expect(h.done).not.toHaveBeenCalled()
    expect(h.domain().value).toBe('mine.example'); expect(cap.value).toBe('abc')
    expect(h.el.textContent).toContain('whole number')
  })
  it('ignores unused invalid cap text when offering is disabled', async () => {
    const h = await setup(); await h.edit('mine.example')
    await act(async () => {
      const cap = h.el.querySelector<HTMLInputElement>('input[placeholder="no limit"]')!
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(cap, 'abc')
      cap.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => {
      const offer = h.el.querySelector('select')!
      offer.value = ''; offer.dispatchEvent(new Event('change', { bubbles: true }))
    })
    await h.click('Finish')
    expect(h.writes).toEqual([{ method: 'PATCH', body: { domain: 'mine.example', expectedRevision: 7 } }])
    expect(h.done).toHaveBeenCalledOnce()
  })
  it('offers unchanged facts without an unnecessary settings write', async () => {
    const h = await setup(); await h.click('Finish')
    expect(h.writes).toEqual([{ method: 'POST', body: { group: '*' } }]); expect(h.done).toHaveBeenCalledOnce()
  })
  it('sends only edited facts with the observed revision before offering', async () => {
    const h = await setup(); await h.edit('new.example'); await h.click('Finish')
    expect(h.writes).toEqual([{ method: 'PATCH', body: { domain: 'new.example', expectedRevision: 7 } }, { method: 'POST', body: { group: '*' } }])
    expect(h.done).toHaveBeenCalledOnce()
  })
  it('retains normalized saved facts and cache after an offer fails, then retries only the offer', async () => {
    const h = await setup({ failOffer: true }); await h.edit('*.new.example'); await h.click('Finish')
    expect(h.done).not.toHaveBeenCalled(); expect(h.domain().value).toBe('new.example')
    const cached = h.client.getQueryData<TargetPage>(getKey('/targets/test-host'))!
    expect(cached.target?.settingsRevision).toBe(8); expect(cached.target?.ingress?.domain).toBe('new.example'); expect(cached.history).toEqual([])
    h.allow(); await h.click('Finish')
    expect(h.writes.map((x) => x.method)).toEqual(['PATCH', 'POST', 'POST']); expect(h.done).toHaveBeenCalledOnce()
  })
  it('keeps a conflicting draft and requires an explicit fresh baseline before offering', async () => {
    const h = await setup({ conflict: true }); await h.edit('mine.example'); await h.click('Finish')
    expect(h.done).not.toHaveBeenCalled(); expect(h.writes).toHaveLength(1); expect(h.domain().value).toBe('mine.example')
    expect(h.button('Finish').disabled).toBe(true); expect(h.el.textContent).toContain('their.example')
    await h.click('Keep my edits on current settings'); h.allow(); await h.click('Finish')
    expect(h.writes[1]).toEqual({ method: 'PATCH', body: { domain: 'mine.example', expectedRevision: 8 } })
    expect(h.done).toHaveBeenCalledOnce()
  })
  it('does not accept cached settings after an unavailable conflict read, and can explicitly discard', async () => {
    const h = await setup({ conflict: true, failRead: true }); await h.edit('mine.example'); await h.click('Finish')
    expect(h.button('Keep my edits on current settings').disabled).toBe(true)
    expect(h.button('Discard my fact edits').disabled).toBe(true); expect(h.domain().value).toBe('mine.example'); expect(h.writes).toHaveLength(1)
    h.allow(); await h.click('Read current settings'); await h.click('Discard my fact edits')
    expect(h.domain().value).toBe('their.example'); await h.click('Finish')
    expect(h.writes.map((x) => x.method)).toEqual(['PATCH', 'POST']); expect(h.done).toHaveBeenCalledOnce()
  })
  it('refuses a previously readable replacement after its refresh fails', async () => {
    const h = await setup({ conflict: true }); await h.edit('mine.example'); await h.click('Finish')
    expect(h.button('Keep my edits on current settings').disabled).toBe(false)
    h.failRead(); await h.click('Read current settings')
    expect(h.button('Keep my edits on current settings').disabled).toBe(true); expect(h.domain().value).toBe('mine.example'); expect(h.writes).toHaveLength(1)
  })
})


it('leaves the target parent trail to the top bar', () => {
  const client = newQueryClient(), el = document.createElement('div')
  try {
    el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><InfrastructureNew /></QueryClientProvider></MemoryRouter>)
    expect(el.querySelector('h1')?.textContent).toBe('Add a deployment target')
    expect(el.querySelector('a[href="/targets"]')).toBeNull()
  } finally {client.clear()}
})

it('heads the kind list like the other wizard lists', () => {
  const client = newQueryClient(), el = document.createElement('div')
  try {
    el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><InfrastructureNew /></QueryClientProvider></MemoryRouter>)
    expect([...el.querySelectorAll('h2.wiz-section')].map(e => e.textContent)).toContain('Target kind')
  } finally {client.clear()}
})

// A disabled Check names the fields still empty, and the line goes once they are filled.
it('names the missing connection fields while Check is disabled', async () => {
  vi.stubGlobal('fetch', vi.fn(withFeatures(async () => json({}))))
  const client = newQueryClient(), el = document.createElement('div'); document.body.append(el)
  const root = createRoot(el)
  disposals.push(() => { act(() => root.unmount()); client.clear(); el.remove() })
  await act(async () => { root.render(<MemoryRouter><QueryClientProvider client={client}><InfrastructureNew /></QueryClientProvider></MemoryRouter>) })
  await vi.waitFor(() => expect(el.querySelectorAll('.wiz-kind').length).toBeGreaterThan(0))
  const docker = [...el.querySelectorAll<HTMLButtonElement>('.wiz-kind')].find((b) => b.textContent?.includes('ssh') || b.textContent?.toLowerCase().includes('docker host'))!
  await act(async () => { docker.click() })
  const check = () => [...el.querySelectorAll('button')].find((b) => b.textContent === 'Check')!
  expect(check().disabled).toBe(true)
  expect(el.querySelector('.wiz-missing')?.textContent).toBe('Fill in the name and SSH key secret')
  // Nothing is below to set until a secret name is typed, so the step does not say so.
  expect(el.textContent).not.toContain('below')
  const type = async (placeholder: string, value: string) => {
    const input = el.querySelector<HTMLInputElement>(`input[placeholder="${placeholder}"]`)!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
  }
  await type('droplet-syd', 'box')
  expect(el.querySelector('.wiz-missing')?.textContent).toBe('Fill in the SSH key secret')
  await type('DROPLET_SSH_KEY', 'BOX_KEY')
  expect(check().disabled).toBe(false)
  expect(el.querySelector('.wiz-missing')).toBeNull()
})

// A Fly.io target is offered beside the others; its connection asks only
// for the token's secret and a region (the org is read from the token),
// and Check asks the server without saving.
it('connects a Fly.io target by its token secret and region, checking before it adds', async () => {
  const posts: Record<string, unknown>[] = []
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') { posts.push(JSON.parse(String(init.body))); return json({ ok: true, detail: 'Fly answers: org acme, region syd (Sydney)', dryRun: true }) }
    return json({})
  })))
  const client = newQueryClient(), el = document.createElement('div'); document.body.append(el)
  const root = createRoot(el)
  disposals.push(() => { act(() => root.unmount()); client.clear(); el.remove() })
  await act(async () => { root.render(<MemoryRouter><QueryClientProvider client={client}><InfrastructureNew /></QueryClientProvider></MemoryRouter>) })
  await vi.waitFor(() => expect(el.querySelectorAll('.wiz-kind').length).toBeGreaterThan(0))
  const fly = [...el.querySelectorAll<HTMLButtonElement>('.wiz-kind')].find((b) => b.textContent?.includes('Fly.io Machines'))!
  expect(fly.querySelector('svg')?.getAttribute('aria-label')).toBe('Fly.io')
  await act(async () => { fly.click() })
  expect(el.querySelector('.wiz-missing')?.textContent).toBe('Fill in the name')
  const name = el.querySelector<HTMLInputElement>('input[placeholder="fly-syd"]')!
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(name, 'fly-demo')
    name.dispatchEvent(new Event('input', { bubbles: true }))
  })
  const button = (label: string) => [...el.querySelectorAll('button')].find((b) => b.textContent === label)!
  expect(button('Add target').disabled).toBe(true)
  await act(async () => { button('Check').click() })
  await vi.waitFor(() => expect(el.textContent).toContain('Fly answers: org acme'))
  expect(posts).toEqual([{ kind: 'fly-machines', name: 'fly-demo', tokenFrom: 'FLY_API_TOKEN', region: 'syd', org: '', dryRun: true }])
  expect(button('Add target').disabled).toBe(false)
})

it('checks explicit AWS estate fields and invalidates the check after an edit', async () => {
  const posts: Record<string, unknown>[] = []
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (_url, init) => {
    if (init?.method === 'POST') { posts.push(JSON.parse(String(init.body))); return json({ detail: 'account and estate checked' }) }
    return json({})
  })))
  const client = newQueryClient(), el = document.createElement('div'); document.body.append(el)
  const root = createRoot(el)
  disposals.push(() => { act(() => root.unmount()); client.clear(); el.remove() })
  await act(async () => { root.render(<MemoryRouter><QueryClientProvider client={client}><InfrastructureNew /></QueryClientProvider></MemoryRouter>) })
  await vi.waitFor(() => expect([...el.querySelectorAll('.wiz-kind')].some(b => b.textContent?.includes('AWS ECS'))).toBe(true))
  await act(async () => { [...el.querySelectorAll<HTMLButtonElement>('.wiz-kind')].find(b => b.textContent?.includes('AWS ECS'))!.click() })
  const button = (text: string) => [...el.querySelectorAll('button')].find(b => b.textContent === text)!
  const edit = async (label: string, value: string) => {
    const input = [...el.querySelectorAll('label')].find(l => l.firstChild?.textContent === label)!.querySelector('input')!
    await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value); input.dispatchEvent(new Event('input', { bubbles: true })) })
  }
  expect(button('Check').disabled).toBe(true)
  for (const [label, value] of Object.entries({ name: 'aws-prod', 'expected account id': '123456789012', cluster: 'existing', 'task subnets (comma separated)': 'subnet-a, subnet-b', 'HTTPS listener ARN': 'arn:listener', 'execution role ARN': 'arn:role', 'apps domain': 'apps.example.com' })) await edit(label, value)
  await act(async () => { button('Check').click() })
  await vi.waitFor(() => expect(posts).toHaveLength(1))
  expect(posts[0]).toEqual({ kind: 'aws-ecs', name: 'aws-prod', keyIdFrom: 'AWS_ACCESS_KEY_ID', secretKeyFrom: 'AWS_SECRET_ACCESS_KEY', account: '123456789012', cluster: 'existing', taskSubnets: ['subnet-a', 'subnet-b'], databaseSubnets: [], listener: 'arn:listener', executionRole: 'arn:role', domain: 'apps.example.com', region: 'ap-southeast-2', dryRun: true })
  await vi.waitFor(() => expect(button('Add target').disabled).toBe(false))
  await edit('cluster', 'changed')
  expect(button('Add target').disabled).toBe(true)
  expect(el.querySelector('a[href="/docs/guides/aws-target-setup"]')).not.toBeNull()
})
