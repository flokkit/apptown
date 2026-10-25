// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../../data'
import { OfferForm } from './Targets'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const cleanups: (() => void)[] = []
afterEach(() => { cleanups.splice(0).forEach((f) => f()); vi.unstubAllGlobals() })

it.each(['3', 'abc', '-1', '1.5', 'Infinity', '9007199254740992'])('admits only valid standing limits from the offer form: %s', async (value) => {
  const writes: { path: string; body: unknown }[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    writes.push({ path, body: JSON.parse(String(init?.body)) })
    return new Response('{"ok":true}')
  }))
  const client = newQueryClient(), done = vi.fn()
  const el = document.createElement('div'); document.body.append(el)
  const root = createRoot(el)
  cleanups.push(() => { act(() => root.unmount()); client.clear(); el.remove() })
  await act(async () => { root.render(<QueryClientProvider client={client}><OfferForm target="host" groups={['operators']} onDone={done} /></QueryClientProvider>) })
  await act(async () => {
    const group = el.querySelector('select')!
    group.value = 'operators'; group.dispatchEvent(new Event('change', { bubbles: true }))
    const limit = el.querySelector<HTMLInputElement>('input[placeholder="no limit"]')!
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(limit, value)
    limit.dispatchEvent(new Event('input', { bubbles: true }))
    for (const text of ['partners', 'synthetic']) {
      const label = [...el.querySelectorAll('label')].find((l) => l.textContent?.trim() === text)!
      label.querySelector<HTMLInputElement>('input')!.click()
    }
  })
  await act(async () => { [...el.querySelectorAll('button')].find((b) => b.textContent === 'Offer')!.click() })
  for (let n = 0; n < 100 && !done.mock.calls.length && !el.textContent?.includes('whole number'); n++) {
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 10)) })
  }
  if (value !== '3') {
    expect(writes).toEqual([]); expect(done).not.toHaveBeenCalled()
    expect(el.querySelector<HTMLInputElement>('input[placeholder="no limit"]')!.value).toBe(value)
    expect(el.textContent).toContain('whole number')
    return
  }
  expect(writes).toEqual([{ path: '/api/v1/targets/host/offers', body: { group: 'operators', maxStanding: 3, audiences: ['partners'], data: ['synthetic'] } }])
  expect(done).toHaveBeenCalledWith(true)
})
