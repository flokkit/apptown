// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { newQueryClient } from '../data'
import { AgentsCard, TokensCard } from './AccountAccess'

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

// Revoking a token or disconnecting an agent asks in a dialog, not a box over
// the row: No leaves the credential alone, Yes removes it.
it.each(['revoke', 'disconnect'])('%s asks in a dialog before it removes the credential', async action => {
  const deletes: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'DELETE') { deletes.push(url); return new Response('{"ok":true}') }
    if (url.includes('/delegations')) return new Response(JSON.stringify({ delegations: [{ id: 'd1', clientName: 'ChatGPT', user: 'sam', scopes: ['apptown:user:read'], createdAt: '2026-11-05T01:00:00Z' }] }))
    return new Response(JSON.stringify({ tokens: [{ Name: 'tom-laptop', User: 'sam', Role: 'admin', Purpose: 'cli' }] }))
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  document.body.append(el)
  const button = (text: string) => [...document.querySelectorAll('button')].find(b => b.textContent === text)!
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter>{action === 'revoke' ? <TokensCard /> : <AgentsCard canEdit />}</MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(button(action)).toBeTruthy())
    await act(async () => button(action).click())
    const dialog = document.querySelector('[role="dialog"]')!
    expect(dialog.textContent).toMatch(action === 'revoke' ? /Revoke token tom-laptop\?/ : /Disconnect ChatGPT \(as sam, connected [^)]+\)\?/)
    await act(async () => button('No').click())
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(deletes).toEqual([])
    await act(async () => button(action).click())
    await act(async () => button('Yes').click())
    await vi.waitFor(() => expect(deletes).toEqual([action === 'revoke' ? '/api/v1/tokens/tom-laptop' : '/api/v1/auth/delegations/d1']))
    expect(document.querySelector('[role="dialog"]')).toBeNull()
  } finally { act(() => root.unmount()); el.remove(); client.clear() }
})

// The count heads the tokens listed: revoked tokens, which the server also
// returns for the record, are not among them.
it('counts the live tokens it lists, not revoked ones', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ tokens: [
    { Name: 'laptop', User: 'kim', Purpose: 'cli' },
    { Name: 'old-laptop', User: 'kim', Purpose: 'cli', RevokedAt: '2026-11-01T00:00:00Z' },
  ] }))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><TokensCard /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.textContent).toContain('laptop'))
    expect(el.querySelector('.section-count')?.textContent).toBe('1')
  } finally { act(() => root.unmount()); client.clear() }
})

// Two connections of one agent by one person (a reconnect) differ by when
// they were made: the phone row shows that, and the disconnect question
// names it, so the right one ends.
it('tells two connections of the same agent apart, on a phone and when disconnecting', async () => {
  const base = { clientName: 'ChatGPT', user: 'sam', scopes: ['apptown:user:read'], lastUsed: '' }
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ delegations: [
    { ...base, id: 'one', createdAt: '2026-11-09T09:40:00Z' },
    { ...base, id: 'two', createdAt: '2026-11-09T09:41:00Z' },
  ] }))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><AgentsCard canEdit /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelectorAll('tbody tr')).toHaveLength(2))
    // What a phone shows: the row without the desktop-only parts.
    const phoneText = (row: Element) => { const copy = row.cloneNode(true) as Element; copy.querySelectorAll('.record-wide').forEach(n => n.remove()); return copy.textContent }
    const [a, b] = [...el.querySelectorAll('tbody tr')]
    expect(phoneText(a)).not.toBe(phoneText(b))
    await act(async () => { [...a.querySelectorAll('button')].find(x => x.textContent === 'disconnect')!.click() })
    expect(document.body.textContent).toMatch(/Disconnect ChatGPT \(as sam, connected [^)]+\)/)
  } finally { act(() => root.unmount()); client.clear() }
})
