// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient, useGet } from './data'
import { settled } from './settled'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// A read enabled only by another read's answer.
function Page() {
  const parent = useGet<{ ready: boolean }>('/parent')
  const child = useGet<{ ready: boolean }>(parent.data ? '/child' : null)
  return <div>{child.data ? 'child answered' : 'waiting'}</div>
}

describe('settled', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // The child read starts only after React renders the parent's answer, so
  // at the moment the parent's answer arrives nothing is in flight. settled
  // must still wait for the child's answer (review TW2-01).
  it('waits for a read that another read\'s answer starts', async () => {
    const held = new Map<string, () => void>()
    vi.stubGlobal('fetch', vi.fn((url: string) => new Promise<Response>((resolve) => {
      held.set(url.replace('/api/v1', ''), () => resolve(new Response(JSON.stringify({ ready: true }))))
    })))
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => { root.render(<QueryClientProvider client={client}><Page /></QueryClientProvider>) })
    try {
      let returned = false
      const done = settled(client).then(() => { returned = true })
      held.get('/parent')!()
      await vi.waitFor(() => expect(held.has('/child')).toBe(true))
      expect(returned, 'settled returned while the child read was in flight').toBe(false)
      held.get('/child')!()
      await done
      expect(el.textContent).toBe('child answered')
    } finally {
      act(() => root.unmount())
      client.clear()
    }
  })

  // The child can start and answer while React renders the parent's answer;
  // its answer then waits on the client's next delivery tick. settled must
  // still return only once the page shows it (review TW2-01, round 3).
  it('shows a dependent read that is answered at once', async () => {
    let release = () => {}
    vi.stubGlobal('fetch', vi.fn((url: string) => new Promise<Response>((resolve) => {
      const answer = () => resolve(new Response(JSON.stringify({ ready: true })))
      if (url.endsWith('/child')) answer()
      else release = answer
    })))
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => { root.render(<QueryClientProvider client={client}><Page /></QueryClientProvider>) })
    try {
      const done = settled(client)
      release()
      await done
      expect(el.textContent).toBe('child answered')
    } finally {
      act(() => root.unmount())
      client.clear()
    }
  })
})
