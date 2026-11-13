// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { ConversationLog } from './change/Conversation'
import { getKey, newQueryClient } from '../data'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('keeps the transcript refresh failure visible after an empty reading', async () => {
  let fail = false
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(fail ? { error: 'transcript unavailable' } : { sessions: [], unreadable: 0 }), { status: fail ? 400 : 200 })))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><ConversationLog changeId="X" live /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(client.getQueryData(getKey('/changes/X/transcript'))).toEqual({ sessions: [], unreadable: 0 }))
    fail = true
    await act(async () => { await client.refetchQueries({ queryKey: getKey('/changes/X/transcript') }) })
    expect(el.querySelector('.chip.unknown')).not.toBeNull()
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
