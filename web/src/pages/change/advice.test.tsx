// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { newQueryClient } from '../../data'
import type { ChangeView } from '../../api'
import { useFailureAdvice } from './advice'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('polls for an answer, stops once answered, and reads each new held step or attempt', async () => {
  vi.useFakeTimers()
  const fetch = vi.fn(async (_url: string) => new Response(JSON.stringify(fetch.mock.calls.length === 1 ? { text: '', recommend: '' } : { text: 'The check failed.', recommend: '' })))
  vi.stubGlobal('fetch', fetch)
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  const view = { waitingOn: { kind: 'check', step: 'review', attempt: 1 }, detail: { steps: [{ id: 'review', attempt: 1 }], next: [] }, next: [] } as unknown as ChangeView
  function Reader() { const a = useFailureAdvice(view); return <p>{a.text}</p> }
  const tick = async (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })
  const draw = async () => { await act(async () => root.render(<QueryClientProvider client={client}><Reader /></QueryClientProvider>)); await tick(10) }
  try {
    await draw()
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(el.textContent).toBe('')
    await tick(3100)
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(el.textContent).toBe('The check failed.')
    await tick(12000)
    expect(fetch).toHaveBeenCalledTimes(2)
    view.waitingOn.attempt = 2
    await draw()
    expect(fetch).toHaveBeenCalledTimes(3)
    await tick(6000)
    expect(fetch).toHaveBeenCalledTimes(3)
    view.waitingOn.step = 'other'
    view.detail!.steps[0].id = 'other'
    await draw()
    expect(fetch).toHaveBeenCalledTimes(4)
    expect(String(fetch.mock.calls[3][0])).toContain('/steps/other/advice')
  } finally {
    await act(async () => root.unmount())
    client.clear(); vi.unstubAllGlobals(); vi.useRealTimers()
  }
})
