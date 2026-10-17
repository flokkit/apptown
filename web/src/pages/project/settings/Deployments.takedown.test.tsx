// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../../../data'
import type { DeploymentView } from '../../../wire.gen'
import { EnvCard } from './Deployments'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const refused = 'A take-down now could take no final copy of its data, so it is refused unless you choose to take it down without one.'
const withCopy = 'Taking it down deletes the app, takes a final copy of its data to the backup store and checks it comes back, then deletes its database cluster.'
const env = { name: 'prod', kind: 'standing', url: '', feed: 'promotion', audience: 'team', data: 'production', protected: true, readiness: 'reconciled',
  target: { name: 'do-syd', kind: 'do-app', title: 'App Platform' }, needs: [{ need: 'postgres', name: 'forge-shop-prod', status: 'ready' }] } as unknown as DeploymentView
const protection = (takeDown: string, finalCopy: boolean) => ({
  deployment: 'prod', dataClass: 'production', offHost: finalCopy, target: 'do-syd', recent: [], running: null, restoredCopies: [], items: [], coverage: { assessed: [], notAssessed: [] },
  operations: Object.fromEntries(['copy', 'check', 'restore', 'rehearsal', 'startFromCopy', 'finalCopy'].map((k) => [k, finalCopy ? { available: true } : { available: false, reason: 'no-copy-runner', why: 'Backups and restore tests need a copy runner.' }])), takeDown,
})

// A cloud deployment's take-down says, from the forge's answer, what it
// does to the data before anyone confirms; when no final copy can be taken,
// the confirmation is the explicit choice to go without one, and only then
// does the request carry that choice, naming the deployment.
describe('taking down a deployment that holds data', () => {
  afterEach(() => vi.unstubAllGlobals())
  const mount = async (prot: unknown) => {
    const deletes: (string | undefined)[] = []
    const reads = { protection: prot as unknown, failing: false }
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'DELETE') { deletes.push(init.body as string | undefined); return new Response(JSON.stringify({ ok: true, deleted: [], orphans: [] })) }
      if (String(url).includes('/protection') && reads.failing) return new Response(JSON.stringify({ error: 'the forge is restarting' }), { status: 503 })
      return new Response(JSON.stringify(String(url).includes('/protection') ? reads.protection : { user: 'bls', instance: {} }))
    }))
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><EnvCard slug="shop" env={env} onChanged={() => {}} /></MemoryRouter></QueryClientProvider>) })
    await vi.waitFor(() => expect(el.textContent).toContain('Copy now'))
    const button = (label: string) => Array.from(el.querySelectorAll('button')).find((b) => b.textContent === label)
    return { el, deletes, button, reads, client, done: () => { act(() => root.unmount()); client.clear() } }
  }

  it('asks for the choice to go without a final copy, and sends it, when none can be taken', async () => {
    const { el, deletes, button, done } = await mount(protection(refused, false))
    await act(async () => { button('Tear down')!.click() })
    expect(el.textContent).toContain(refused)
    await act(async () => { button('Take it down without a final copy')!.click() })
    await vi.waitFor(() => expect(deletes).toHaveLength(1))
    expect(JSON.parse(deletes[0]!)).toEqual({ withoutFinalCopy: 'prod' })
    done()
  })
  it('re-reads on open, and asks what confirming means now', async () => {
    const { el, deletes, button, reads, done } = await mount(protection(refused, false))
    reads.protection = protection(withCopy, true) // a copy runner was named meanwhile
    await act(async () => { button('Tear down')!.click() })
    await vi.waitFor(() => expect(el.textContent).toContain(withCopy))
    expect(button('Take it down without a final copy')).toBeUndefined()
    await act(async () => { button('Take it down')!.click() })
    await vi.waitFor(() => expect(deletes).toHaveLength(1))
    expect(deletes[0]).toBeUndefined()
    done()
  })
  it('offers no choice to keep the data on an answer whose refresh failed', async () => {
    const { button, reads, client, done } = await mount(protection(refused, false))
    await act(async () => { button('Tear down')!.click() })
    expect(button('Take it down without a final copy')).toBeDefined()
    reads.failing = true
    await act(async () => { await client.refetchQueries().catch(() => {}) })
    await vi.waitFor(() => expect(button('Take it down without a final copy')).toBeUndefined())
    expect(button('Tear down')?.disabled).toBe(true)
    done()
  })
  it('says the final copy comes first, and sends no choice, when one can be taken', async () => {
    const { el, deletes, button, done } = await mount(protection(withCopy, true))
    await act(async () => { button('Tear down')!.click() })
    expect(el.textContent).toContain(withCopy)
    expect(button('Take it down without a final copy')).toBeUndefined()
    await act(async () => { button('Take it down')!.click() })
    await vi.waitFor(() => expect(deletes).toHaveLength(1))
    expect(deletes[0]).toBeUndefined()
    done()
  })
})
