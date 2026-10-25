// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { getKey, newQueryClient } from '../data'
import type { Target, TargetPage } from '../wire.gen'
import { OfferStep } from './InfrastructureNew'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const target = (revision = 7, domain = 'old.example'): Target => ({
  id: 'target-id', provider: '', origin: 'attached', secretsDelivery: 'env', status: 'ready',
  name: 'test-host', displayLabel: 'Test host', kind: 'docker-host', ingress: { domain }, address: {},
  settingsRevision: revision, needsMode: 'emulators',
  capabilities: { persistentVolumes: true, previews: false, ownLiveness: false, rollout: [], needs: [] },
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
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
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
        current = { ...target(8, 'their.example'), capabilities: { ...current.capabilities, previews: true } }
        return new Response('{"error":"settings moved"}', { status: 409, headers: { 'X-AppTown-Deny-Code': 'settings-moved' } })
      }
      if (body.expectedRevision !== current.settingsRevision) return json({ error: 'revision required' }, 422)
      current = { ...current, settingsRevision: current.settingsRevision + 1, ingress: { domain: String(body.domain).replace(/^\*\./, '') } }
      return json({ ok: true, target: current })
    }
    if (failOffer) return json({ error: 'offer unavailable' }, 503)
    return json({ ok: true })
  }))
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
