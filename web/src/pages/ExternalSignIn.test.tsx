// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../data'
import { ExternalProviderSettings } from './ExternalSignIn'
import type { ExternalProvider } from '../wire.gen'
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

afterEach(() => { vi.unstubAllGlobals() })
it('retains an edit and refuses saving it after another provider revision replaces its snapshot', async () => {
  const provider: ExternalProvider = { id: '00000000-0001-7000-8000-000000000000', revision: 1, issuer: 'https://issuer.example', displayName: 'Original', clientId: 'client', secretRef: 'OIDC_SECRET', callbackOrigin: 'https://apptown.example', enabled: false, allowSignup: false, initialGroup: '' }
  const fetch = vi.fn(async (url: string) => new Response(JSON.stringify(url.includes('bindings') ? { bindings: [] } : provider)))
  vi.stubGlobal('fetch', fetch)
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  await act(async () => { root.render(<QueryClientProvider client={client}><ExternalProviderSettings /></QueryClientProvider>) })
  await vi.waitFor(() => expect(el.textContent).toContain('Original: disabled'))
  await act(async () => { Array.from(el.querySelectorAll('button')).find(b => b.textContent === 'Configure provider')!.click() })
  const before = Array.from(el.querySelectorAll('input')).map(i => i.value)
  await act(async () => { client.setQueryData(getKey('/auth/external/provider'), { ...provider, revision: 2, displayName: 'Replacement' }) })
  await vi.waitFor(() => expect(el.textContent).toContain('Settings changed while you were editing'))
  expect(Array.from(el.querySelectorAll('input')).map(i => i.value)).toEqual(before)
  expect(Array.from(el.querySelectorAll('button')).find(b => b.textContent === 'Save disabled configuration')?.disabled).toBe(true)
  expect(fetch.mock.calls.every(call => call.length === 1 || !(call as unknown as [string, RequestInit])[1]?.method)).toBe(true)
  await act(async () => { root.unmount() })
})
