// @vitest-environment jsdom
import { expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from './data'
import { CloseIt } from './CloseIt'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('shows the server refusal when closing a change fails', async () => {
 vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({error:'already on main; fix forward'}), {status:409, headers:{'Content-Type':'application/json'}})))
 const el = document.createElement('div')
 document.body.appendChild(el)
 const root = createRoot(el)
 try {
  await act(async () => root.render(<QueryClientProvider client={newQueryClient()}><CloseIt changeId="00000000-0021-7000-8000-000000000000" /></QueryClientProvider>))
  await act(async () => (el.querySelector('button') as HTMLElement).click())
  await act(async () => {
   const input = el.querySelector('input')!
   Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'obsolete')
   input.dispatchEvent(new Event('input', {bubbles:true}))
  })
  await act(async () => (el.querySelector('button.primary') as HTMLElement).click())
  expect(fetch).toHaveBeenCalledTimes(1)
  expect(el.textContent).toContain('already on main; fix forward')
 } finally {
  await act(async () => root.unmount())
  el.remove()
  vi.unstubAllGlobals()
 }
})
