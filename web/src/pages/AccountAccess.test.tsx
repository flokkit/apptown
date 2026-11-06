// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { TokensCard } from './AccountAccess'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
afterEach(() => vi.unstubAllGlobals())

it.each([false, true])('refreshes Account inventory after writes (existing token: %s)', async existing => {
  const token = { Name: 'laptop', User: 'kim', Purpose: 'cli' }
  let tokens = existing ? [token] : []
  const writes: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') {
      writes.push(url)
      tokens = [{ Name: JSON.parse(String(init.body)).name, User: 'kim', Purpose: 'cli' }]
      return new Response('{"token":"once-only-secret"}')
    }
    if (init?.method === 'DELETE') { writes.push(url); tokens = []; return new Response('{"ok":true}') }
    expect(url).toBe('/api/v1/auth/me')
    return new Response(JSON.stringify({ user: 'kim', ownTokens: tokens }))
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  const button = (text: string) => [...el.querySelectorAll('button')].find(b => b.textContent === text)!
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><TokensCard personal /></QueryClientProvider>))
    if (!existing) {
      await vi.waitFor(() => expect(el.textContent).toContain('No access tokens yet.'))
      await act(async () => {
        const input = el.querySelector('input')!
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'laptop')
        input.dispatchEvent(new Event('input', { bubbles: true }))
        const select = el.querySelector('select')!
        select.value = 'cli'
        select.dispatchEvent(new Event('change', { bubbles: true }))
      })
      await act(async () => button('Mint token').click())
      await vi.waitFor(() => expect(el.querySelector('.badge')?.textContent).toBe('laptop'))
      expect(el.textContent).not.toContain('No access tokens yet.')
      expect(el.textContent).toContain('once-only-secret')
    }
    await vi.waitFor(() => expect(el.querySelector('.badge')?.textContent).toBe('laptop'))
    expect(el.querySelector('.section-count')?.textContent).toBe('1')
    await act(async () => button('revoke').click())
    await act(async () => button('Yes').click())
    await vi.waitFor(() => expect(el.querySelector('.badge')).toBeNull())
    expect(el.textContent).toContain('No access tokens yet.')
    expect(el.querySelector('.section-count')?.textContent).toBe('0')
    expect(writes).toEqual(existing ? ['/api/v1/account/tokens/laptop'] : ['/api/v1/account/tokens', '/api/v1/account/tokens/laptop'])
  } finally { act(() => root.unmount()); client.clear() }
})
