// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, expect, it, vi } from 'vitest'
import App from './App'
import { getKey, newQueryClient } from './data'
import { settled } from './settled'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
afterEach(() => { vi.unstubAllGlobals(); window.history.replaceState(null, '', '/') })

it.each(['/', '/invite', '/invite/private-token'])('a page with no menu has one bar: the wordmark and its heading, at %s', async path => {
  const me = {mode:'internal',user:'',needsBootstrap:false}
  vi.stubGlobal('fetch', vi.fn(async url => new Response(JSON.stringify(String(url).endsWith('/auth/invite')
    ? {username:'sam',role:'member',purpose:'invite',invitedBy:'priya',expiresAt:'2030-01-01T00:00:00Z',state:'pending'} : me))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  client.setQueryData(getKey('/auth/me'), me)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><App /></MemoryRouter></QueryClientProvider>))
    await settled(client)
    expect(el.querySelector('.topbar')).toBeNull()
    expect(el.querySelectorAll('header.page-bar')).toHaveLength(1)
    const bar = el.querySelector('header.page-bar')!
    expect(bar.querySelector('.page-bar-lead a.wordmark')?.getAttribute('href')).toBe('/')
    expect(bar.textContent).not.toContain('private-token')
    expect(el.querySelectorAll('h1')).toHaveLength(1)
    expect(bar.querySelector('h1')?.textContent).toBe(path === '/' ? 'Sign in' : 'Join AppTown')
    // Nobody is signed in: no menu, search or attention to offer.
    expect(bar.querySelector('button')).toBeNull()
  } finally { act(() => root.unmount()); client.clear() }
})
