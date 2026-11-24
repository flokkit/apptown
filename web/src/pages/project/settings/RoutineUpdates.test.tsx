// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { getKey, newQueryClient } from '../../../data'
import { DeploymentFeed } from './DeploymentFeed'
import type { DeploymentView } from '../../../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// Both owners keep their revisions and drafts despite sharing the submit.
it.each(['person', 'window', 'automatic'])('saves routine mode %s and feed together only on Save settings', async mode => {
  let deployment = { id: 'main', name: 'main', kind: 'standing', feed: 'manual', feedRevision: 3, routineUpdatesRevision: 7, routineUpdates: { mode: mode === 'person' ? 'automatic' : 'person' } } as DeploymentView
  const sent: { url: string; body: unknown }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (url.endsWith('/authz/roles')) return new Response(JSON.stringify({ roles: [] }))
    if (init?.method === 'PUT') {
      const body = JSON.parse(String(init.body))
      sent.push({ url, body })
      deployment = url.endsWith('/feed') ? { ...deployment, ...body, feedRevision: 4, feedConfirmed: true } : { ...deployment, routineUpdates: body.routineUpdates, routineUpdatesRevision: 8 }
      return new Response(JSON.stringify(deployment))
    }
    return new Response(JSON.stringify([deployment]))
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  const change = async (label: string, value: string) => act(async () => {
    const select = el.querySelector<HTMLSelectElement>(`[aria-label="${label}"]`)!
    select.value = value; select.dispatchEvent(new Event('change', { bubbles: true }))
  })
  const submit = async () => act(async () => { el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })) })
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><DeploymentFeed slug="garden" name="main" /></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelector('select')).not.toBeNull())
    expect(el.querySelectorAll('section')).toHaveLength(1)
    expect(el.querySelector('h2')?.textContent).toBe('Releases')
    expect(el.querySelectorAll('form')).toHaveLength(1)
    expect(el.querySelectorAll('button[type="submit"]')).toHaveLength(1)
    await change('Routine updates', mode)
    await change('Release feed', 'main')
    expect(sent).toEqual([])
    await submit()
    expect(sent).toEqual([
      { url: '/api/v1/projects/garden/deployments/main/routine-updates', body: { revision: 7, routineUpdates: mode === 'window' ? { mode, days: 'sun', from: '02:00', to: '04:00', zone: Intl.DateTimeFormat().resolvedOptions().timeZone } : { mode } } },
      { url: '/api/v1/projects/garden/deployments/main/feed', body: { revision: 3, feed: 'main', from: '', approvalRole: '' } },
    ])
    await vi.waitFor(() => expect(el.querySelector<HTMLButtonElement>('button[type="submit"]')?.disabled).toBe(true))
    expect(client.getQueryData<DeploymentView[]>(getKey('/projects/garden/deployments'))?.[0].routineUpdatesRevision).toBe(8)
    // A routine-only edit leaves the feed owner alone.
    const nextMode = mode === 'person' ? 'automatic' : 'person'
    await change('Routine updates', nextMode)
    await submit()
    expect(sent).toHaveLength(3)
    expect(sent[2].body).toEqual({ revision: 8, routineUpdates: { mode: nextMode } })
  } finally { await act(async () => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

it('keeps a failed routine draft after the feed succeeds and blocks a moved revision', async () => {
  const deployment = { id: 'main', name: 'main', kind: 'standing', feed: 'manual', feedRevision: 3, routineUpdatesRevision: 7, routineUpdates: { mode: 'automatic' } } as DeploymentView
  const sent: unknown[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (url.endsWith('/authz/roles')) return new Response(JSON.stringify({ roles: [] }))
    if (init?.method === 'PUT') {
      sent.push(JSON.parse(String(init.body)))
      if (url.endsWith('/routine-updates')) return new Response('{"error":"routine update settings changed"}', { status: 409 })
      Object.assign(deployment, { feed: 'main', feedRevision: 4, feedConfirmed: true })
      return new Response(JSON.stringify(deployment))
    }
    return new Response(JSON.stringify([deployment]))
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  const change = async (label: string, value: string) => act(async () => {
    const select = el.querySelector<HTMLSelectElement>(`[aria-label="${label}"]`)!
    select.value = value; select.dispatchEvent(new Event('change', { bubbles: true }))
  })
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><DeploymentFeed slug="garden" name="main" /></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelector('select')).not.toBeNull())
    await change('Routine updates', 'window')
    await change('Release feed', 'main')
    await act(async () => { el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })) })
    expect(sent).toHaveLength(2)
    expect(el.textContent).toContain('routine update settings changed')
    expect(el.querySelector<HTMLSelectElement>('[aria-label="Routine updates"]')?.value).toBe('window')
    await act(async () => client.setQueryData(getKey('/projects/garden/deployments'), [{ ...deployment, routineUpdatesRevision: 8 }]))
    await vi.waitFor(() => expect(el.textContent).toContain('routine update settings changed while you were editing'))
    expect(el.querySelector<HTMLButtonElement>('button[type="submit"]')?.disabled).toBe(true)
    await act(async () => { [...el.querySelectorAll('button')].find(b => b.textContent === 'Discard changes')!.click() })
    expect(el.querySelector<HTMLSelectElement>('[aria-label="Routine updates"]')?.value).toBe('automatic')
    expect(el.querySelector('[aria-label="Window days"]')).toBeNull()
  } finally { await act(async () => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
