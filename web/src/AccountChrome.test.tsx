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

it.each(['/', '/invite', '/invite/private-token'])('keeps one trail and a current-page heading at %s', async path => {
  const me = {mode:'internal',user:'',needsBootstrap:false}
  vi.stubGlobal('fetch', vi.fn(async url => new Response(JSON.stringify(String(url).endsWith('/auth/invite')
    ? {username:'sam',role:'member',purpose:'invite',invitedBy:'priya',expiresAt:'2030-01-01T00:00:00Z',state:'pending'} : me))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  client.setQueryData(getKey('/auth/me'), me)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><App /></MemoryRouter></QueryClientProvider>))
    await settled(client)
    expect(el.querySelectorAll('nav[aria-label="Breadcrumb"]')).toHaveLength(1)
    expect(el.querySelector('nav')?.textContent).not.toContain('private-token')
    expect(el.querySelectorAll('h1')).toHaveLength(1)
    expect(el.querySelector('h1')?.textContent).toBe(path === '/' ? 'Sign in' : 'Join AppTown')
    expect(el.querySelector('.topbar button')).toBeNull()
  } finally { act(() => root.unmount()); client.clear() }
})
