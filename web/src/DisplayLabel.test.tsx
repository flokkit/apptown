// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { newQueryClient } from './data'
import { DisplayLabelEdit } from './DisplayLabel'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('keeps a dirty label and its comparison value when a newer reading arrives', async () => {
  const sent: unknown[] = []
  vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
    sent.push(JSON.parse(String(init?.body)))
    return new Response('{"error":"label changed"}', { status: 409 })
  }))
  const el = document.createElement('div')
  document.body.appendChild(el)
  const root = createRoot(el)
  const client = newQueryClient()
  const render = async (value: string, path = '/targets/example/label') => act(async () => {
    root.render(<QueryClientProvider client={client}><DisplayLabelEdit value={value} machineKey="lan" path={path} /></QueryClientProvider>)
  })
  try {
    await render('Original')
    await act(async () => { el.querySelector('button')!.click() })
    const input = el.querySelector('input')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'My draft')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await render('Another operator label')
    expect(input.value).toBe('My draft')
    await act(async () => { el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })) })
    expect(sent).toEqual([{ expectedLabel: 'Original', label: 'My draft' }])
    expect(input.value).toBe('My draft')
    await act(async () => { [...el.querySelectorAll('button')].find(b => b.textContent === 'Cancel')!.click() })
    await act(async () => { el.querySelector('button')!.click() })
    expect(el.querySelector('input')!.value).toBe('Another operator label')
    await render('Other target', '/targets/another/label')
    expect(el.querySelector('input')).toBeNull()
    await act(async () => { el.querySelector('button')!.click() })
    expect(el.querySelector('input')!.value).toBe('Other target')
  } finally {
    await act(async () => root.unmount())
    client.clear()
    el.remove()
    vi.unstubAllGlobals()
  }
})
