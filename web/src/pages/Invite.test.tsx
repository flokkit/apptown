// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import InvitePage from './Invite'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const offer = (state: string, purpose = 'invite') => ({ purpose, username: 'kim', role: purpose === 'invite' ? 'member' : '', invitedBy: 'bls', expiresAt: '2026-10-10T00:00:00Z', state })

describe('the invite link page', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

  // The token goes in the body of both requests, and the chosen password
  // is sent only when it was typed the same twice.
  it('shows who invited whom and sets the password', async () => {
    const sent: { url: string; body: Record<string, string> }[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      sent.push({ url, body: JSON.parse(String(init?.body)) })
      return new Response(JSON.stringify(url.endsWith('/accept') ? { ok: true, user: 'kim', role: 'member' } : offer('pending')))
    }))
    const replace = vi.fn()
    vi.stubGlobal('location', { ...window.location, replace })
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => { root.render(<InvitePage token="apptown_invite_abc" />) })
    await vi.waitFor(() => expect(el.textContent).toContain('bls invited you to AppTown as kim'))
    const [pw, again] = [...el.querySelectorAll('input[type=password]')] as HTMLInputElement[]
    const type = async (input: HTMLInputElement, value: string) => act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await type(pw, 'kims-password')
    await type(again, 'kims-passwore')
    await act(async () => { (el.querySelector('form') as HTMLFormElement).requestSubmit() })
    expect(el.textContent).toContain('The two passwords are different.')
    expect(sent.filter((s) => s.url.endsWith('/accept'))).toHaveLength(0)
    await type(again, 'kims-password')
    await act(async () => { (el.querySelector('form') as HTMLFormElement).requestSubmit() })
    expect(sent.find((s) => s.url.endsWith('/accept'))).toEqual({ url: '/api/v1/auth/invite/accept', body: { token: 'apptown_invite_abc', password: 'kims-password' } })
    expect(sent[0]).toEqual({ url: '/api/v1/auth/invite', body: { token: 'apptown_invite_abc' } })
    await vi.waitFor(() => expect(replace).toHaveBeenCalledWith('/'))
    act(() => root.unmount())
  })

  // A password reset link opens the same page, says it replaces the
  // account's password and signs out its other sessions, and sends the same
  // token-and-password request.
  it('shows a password reset link and sets the new password', async () => {
    const sent: { url: string; body: Record<string, string> }[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      sent.push({ url, body: JSON.parse(String(init?.body)) })
      return new Response(JSON.stringify(url.endsWith('/accept') ? { ok: true, user: 'kim', role: 'member' } : offer('pending', 'password')))
    }))
    const replace = vi.fn()
    vi.stubGlobal('location', { ...window.location, replace })
    const el = document.createElement('div')
    const root = createRoot(el)
    const moved = vi.spyOn(window.history, 'replaceState')
    await act(async () => { root.render(<InvitePage token="apptown_invite_reset" />) })
    await vi.waitFor(() => expect(el.textContent).toContain('bls made a password reset link for kim'))
    // The token leaves the address bar; the page keeps it in memory, and a
    // render without it (the router reading /invite) changes nothing.
    expect(moved.mock.calls.map((c) => c[2])).toContain('/invite')
    await act(async () => { root.render(<InvitePage token="" />) })
    expect(el.textContent).toContain('bls made a password reset link for kim')
    expect(el.textContent).toContain('Your old password stops working and you are signed out everywhere else')
    expect(el.textContent).not.toContain('invited you')
    for (const input of [...el.querySelectorAll('input[type=password]')] as HTMLInputElement[]) {
      await act(async () => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'kims-new-password')
        input.dispatchEvent(new Event('input', { bubbles: true }))
      })
    }
    await act(async () => { (el.querySelector('form') as HTMLFormElement).requestSubmit() })
    expect(sent.find((s) => s.url.endsWith('/accept'))).toEqual({ url: '/api/v1/auth/invite/accept', body: { token: 'apptown_invite_reset', password: 'kims-new-password' } })
    await vi.waitFor(() => expect(replace).toHaveBeenCalledWith('/'))
    act(() => root.unmount())
  })

  it('says plainly when a password reset link cannot be used', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(offer('accepted', 'password')))))
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => { root.render(<InvitePage token="apptown_invite_used" />) })
    await vi.waitFor(() => expect(el.textContent).toContain('This password reset link has already been used.'))
    expect(el.querySelector('input[type=password]')).toBeNull()
    act(() => root.unmount())
  })

  // Reloaded after the token left the address bar: nothing to read.
  it('asks for the link again when the address no longer carries it', async () => {
    const fetched = vi.fn()
    vi.stubGlobal('fetch', fetched)
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => { root.render(<InvitePage token="" />) })
    expect(el.textContent).toContain('Open the link you were given again.')
    expect(fetched).not.toHaveBeenCalled()
    act(() => root.unmount())
  })

  it('says plainly when an invite cannot be used', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(offer('expired')))))
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => { root.render(<InvitePage token="apptown_invite_old" />) })
    await vi.waitFor(() => expect(el.textContent).toContain('This invite has expired.'))
    expect(el.querySelector('input[type=password]')).toBeNull()
    act(() => root.unmount())
  })
})
