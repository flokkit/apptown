// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { getKey, newQueryClient } from '../../../data'
import { DeploymentAddressEdit } from './DeploymentAddress'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('keeps the address edit revision across refresh and resets drafts across deployment identities', async () => {
  const sent: unknown[] = []
  let settings = { revision: 2, customHostname: 'saved.example', generatedHostname: 'app-id.apps.example', verified: true, reservedHosts: [] }
  vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
    if (init?.method === 'PUT') { sent.push(JSON.parse(String(init.body))); return new Response('{"error":"address changed"}', { status: 409 }) }
    return new Response(JSON.stringify(settings))
  }))
  const el = document.createElement('div')
  document.body.appendChild(el)
  const root = createRoot(el)
  const client = newQueryClient()
  const refresh = async (id: string) => { await act(async () => { await client.refetchQueries({ queryKey: getKey(`/projects/p/deployments/${id}/address`) }); await new Promise(resolve => setTimeout(resolve, 0)) }) }
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><DeploymentAddressEdit slug="p" id="one" /></QueryClientProvider>) })
    await refresh('one')
    await act(async () => {
      const input = el.querySelector('input')!
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'draft.example')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    settings = { ...settings, revision: 3, customHostname: 'other.example' }
    await refresh('one')
    expect(el.querySelector('input')!.value).toBe('draft.example')
    await act(async () => { el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })) })
    expect(sent).toEqual([{ expectedRevision: 2, customHostname: 'draft.example' }])
    await act(async () => { root.render(<QueryClientProvider client={client}><DeploymentAddressEdit slug="p" id="two" /></QueryClientProvider>) })
    await refresh('two')
    expect(el.querySelector('input')!.value).toBe('other.example')
    expect(el.textContent).not.toContain('Discard changes')
  } finally { await act(async () => root.unmount()); client.clear(); el.remove(); vi.unstubAllGlobals() }
})


it('preserves a hostname typed while an earlier save is pending', async () => {
  let finishSave!: (value: Response) => void
  const settings = { revision: 2, customHostname: 'saved.example', generatedHostname: 'app-id.apps.example', verified: true, reservedHosts: [] }
  vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
    if (init?.method === 'PUT') return new Promise<Response>(resolve => { finishSave = resolve })
    return new Response(JSON.stringify(settings))
  }))
  const el = document.createElement('div')
  document.body.appendChild(el)
  const root = createRoot(el)
  const client = newQueryClient()
  const type = async (value: string) => { await act(async () => {
    const input = el.querySelector('input')!
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  }) }
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><DeploymentAddressEdit slug="p" id="one" /></QueryClientProvider>) })
    await act(async () => { await client.refetchQueries({ queryKey: getKey('/projects/p/deployments/one/address') }); await new Promise(resolve => setTimeout(resolve, 0)) })
    await type('first.example')
    await act(async () => { el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })) })
    // Either prevent further editing during the save, or preserve later edits.
    if (el.querySelector('input')!.disabled) {
      await act(async () => { finishSave(new Response(JSON.stringify({ revision: 3 }))) })
      return
    }
    await type('second.example')
    expect(el.querySelector('input')!.value).toBe('second.example')
    settings.revision = 3
    settings.customHostname = 'first.example'
    await act(async () => { finishSave(new Response(JSON.stringify({ revision: 3 }))); await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(el.querySelector('input')!.value).toBe('second.example')
  } finally { await act(async () => root.unmount()); client.clear(); el.remove(); vi.unstubAllGlobals() }
})
