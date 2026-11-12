// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { getKey, newQueryClient } from '../../../data'
import { RoutineUpdates } from './RoutineUpdates'
import type { DeploymentView } from '../../../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('saves the selected release setting with its revision and refuses a moved draft', async () => {
  const deployment = { name: 'main', kind: 'standing', routineUpdatesRevision: 7, routineUpdates: { mode: 'automatic' } } as DeploymentView
  const fetch = vi.fn(async () => new Response(JSON.stringify([deployment])))
  vi.stubGlobal('fetch', fetch)
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><RoutineUpdates slug="garden" name="main" /></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelector('select')).not.toBeNull())
    const select = el.querySelector('select')!
    await act(async () => { select.value = 'person'; select.dispatchEvent(new Event('change', { bubbles: true })) })
    await act(async () => el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    const save = fetch.mock.calls.find(call => ((call as unknown[])[1] as RequestInit)?.method === 'PUT') as unknown[] | undefined
    expect(save?.[0]).toContain('/projects/garden/deployments/main/routine-updates')
    expect(JSON.parse((save?.[1] as RequestInit).body as string)).toEqual({ revision: 7, routineUpdates: { mode: 'person' } })
    await act(async () => { select.value = 'window'; select.dispatchEvent(new Event('change', { bubbles: true })) })
    expect(el.querySelector('[aria-label="Window days"]')).not.toBeNull()
    await act(async () => client.setQueryData(getKey('/projects/garden/deployments'), [{ ...deployment, routineUpdatesRevision: 8 }]))
    await vi.waitFor(() => expect(el.textContent).toContain('settings changed while you were editing'))
    expect((el.querySelector('button[type="submit"]') as HTMLButtonElement).disabled).toBe(true)
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
