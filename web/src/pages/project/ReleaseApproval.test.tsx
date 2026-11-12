// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { newQueryClient } from '../../data'
import type { Release } from '../../wire.gen'
import { ReleaseApprovalActions } from './ReleaseApproval'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it.each(['approve', 'cancel'])('sends the displayed release decision and keeps a refusal visible: %s', async action => {
  const calls: { path: string; body: unknown }[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init: RequestInit) => {
    calls.push({ path, body: JSON.parse(String(init.body)) })
    return new Response('{"error":"this release is no longer waiting for approval; reload its deployment"}', { status: 409 })
  }))
  const el = document.createElement('div')
  const root = createRoot(el)
  const client = newQueryClient()
  const release = { number: 12, state: 'waiting for approval', approvalRole: 'release-manager' } as Release
  const decision = { meaning: 'I accept release 12 running on production', canApprove: true, canCancel: true }
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><ReleaseApprovalActions slug="lunch" release={release} decision={decision} /></QueryClientProvider>))
    expect(el.textContent).toContain(decision.meaning)
    await act(async () => { [...el.querySelectorAll('button')].find(b => b.textContent?.toLowerCase() === action)!.click() })
    expect(calls).toEqual([{ path: `/api/v1/projects/lunch/releases/12/${action}`, body: action === 'approve' ? { meaning: decision.meaning } : {} }])
    await vi.waitFor(() => expect(el.querySelector('[role="alert"]')?.textContent).toContain('no longer waiting'))
    await act(async () => root.render(<QueryClientProvider client={client}><ReleaseApprovalActions slug="lunch" release={release} decision={decision} current={false} /></QueryClientProvider>))
    expect([...el.querySelectorAll('button')].every(b => b.disabled)).toBe(true)
  } finally {
    await act(async () => root.unmount())
    client.clear(); vi.unstubAllGlobals()
  }
})

// Routine settings and release holds ask a person to release, with no approval role.
it.each(['waiting for a person to release this update', 'waiting for a person: the latest release failed'])('shows the release reason and personal door: %s', async waitReason => {
  const calls: { path: string; body: unknown }[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init: RequestInit) => {
    calls.push({ path, body: JSON.parse(String(init.body)) })
    return new Response('{}')
  }))
  const el = document.createElement('div')
  const root = createRoot(el)
  const client = newQueryClient()
  const release = { number: 12, state: 'waiting for approval', approvalRole: '' } as Release
  const decision = { meaning: 'I accept release 12 running on production', canApprove: true, canCancel: true, waitReason }
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><ReleaseApprovalActions slug="lunch" release={release} decision={decision} /></QueryClientProvider>))
    expect(el.textContent).toContain(waitReason)
    expect(el.textContent).not.toContain('Waiting for approval from')
    expect(el.textContent).toContain(decision.meaning)
    const buttons = [...el.querySelectorAll('button')]
    expect(buttons.map(b => b.textContent)).toEqual(['Release', 'Cancel'])
    await act(async () => { buttons[0].click() })
    expect(calls).toEqual([{ path: '/api/v1/projects/lunch/releases/12/approve', body: { meaning: decision.meaning } }])
    await act(async () => root.render(<QueryClientProvider client={client}><ReleaseApprovalActions slug="lunch" release={release} decision={decision} current={false} /></QueryClientProvider>))
    expect([...el.querySelectorAll('button')].every(b => b.disabled)).toBe(true)
  } finally {
    await act(async () => root.unmount())
    client.clear(); vi.unstubAllGlobals()
  }
})
