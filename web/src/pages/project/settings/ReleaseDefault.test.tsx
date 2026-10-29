// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { getKey, newQueryClient } from '../../../data'
import type { DeploymentView } from '../../../wire.gen'
import { ReleaseDefaultPicker } from './ReleaseDefault'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('retains the selected identity and starting revision across a refreshed preference', async () => {
  const sent: unknown[] = []
  let current = { projectId: 'project', deploymentId: 'one', revision: 4 }
  vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
    if (init?.method === 'PUT') {
      sent.push(JSON.parse(String(init.body)))
      return new Response('{"error":"preference changed"}', { status: 409 })
    }
    return new Response(JSON.stringify(current))
  }))
  const el = document.createElement('div')
  document.body.appendChild(el)
  const root = createRoot(el)
  const client = newQueryClient()
  const deployments = ['one', 'two', 'three'].map(id => ({ id, name: id, displayLabel: id, kind: 'standing', feed: 'manual' }) as DeploymentView)
  try {
    await act(async () => {
      root.render(<QueryClientProvider client={client}><ReleaseDefaultPicker slug="sample" deployments={deployments} /></QueryClientProvider>)
    })
    await act(async () => { await client.refetchQueries({ queryKey: getKey('/projects/sample/release/default') }); await new Promise(resolve => setTimeout(resolve, 0)) })
    await act(async () => {
      const select = el.querySelector('select')!
      select.value = 'two'
      select.dispatchEvent(new Event('change', { bubbles: true }))
    })
    current = { ...current, deploymentId: 'three', revision: 5 }
    await act(async () => { await client.refetchQueries({ queryKey: getKey('/projects/sample/release/default') }); await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(el.querySelector('select')!.value).toBe('two')
    await act(async () => { el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })) })
    await vi.waitFor(async () => { await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) }); expect(el.querySelector('.error')?.textContent).toContain('preference changed') })
    expect(sent).toEqual([{ deploymentId: 'two', expectedRevision: 4 }])
    expect(el.querySelector('select')!.value).toBe('two')
    await act(async () => { [...el.querySelectorAll('button')].find(b => b.textContent === 'Discard changes')!.click() })
    expect(el.querySelector('select')!.value).toBe('three')
    await act(async () => {
      const select = el.querySelector('select')!
      select.value = 'one'
      select.dispatchEvent(new Event('change', { bubbles: true }))
    })
    await act(async () => {
      root.render(<QueryClientProvider client={client}><ReleaseDefaultPicker slug="another" deployments={deployments} /></QueryClientProvider>)
      await new Promise(resolve => setTimeout(resolve, 0))
    })
    await act(async () => { await client.refetchQueries({ queryKey: getKey('/projects/another/release/default') }); await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(el.querySelector('select')!.value).toBe('three')
    expect([...el.querySelectorAll('button')].some(b => b.textContent === 'Discard changes')).toBe(false)

  } finally {
    await act(async () => root.unmount())
    client.clear()
    el.remove()
    vi.unstubAllGlobals()
  }
})
