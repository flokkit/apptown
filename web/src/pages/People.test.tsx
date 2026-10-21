// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { AdminNetworksCard, InvitePerson, TokensCard, UsersCard } from './People'
import type { Account } from '../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

describe('administrator networks', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // Each change is saved from the list the page read; when someone else saved
  // first, the save is refused, the list is read again, and the typed
  // network stays in the box to add again.
  it('saves from the list it read and reads again when someone saved first', async () => {
    let stored = ['10.0.0.0/8']
    const sent: Record<string, unknown>[] = []
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.method === 'PUT') {
        sent.push(JSON.parse(String(init.body)))
        return new Response(JSON.stringify({ error: 'moved' }), { status: 409, headers: { 'X-AppTown-Deny-Code': 'settings-moved' } })
      }
      return new Response(JSON.stringify({ networks: stored, you: '10.1.2.3', youInside: true }))
    }))
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => { root.render(<QueryClientProvider client={newQueryClient()}><AdminNetworksCard canEdit /></QueryClientProvider>) })
    await vi.waitFor(() => expect(el.textContent).toContain('10.0.0.0/8'))
    const input = el.querySelector('input') as HTMLInputElement
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, '192.0.2.0/24')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    stored = ['10.0.0.0/8', '198.51.100.0/24']
    await act(async () => { [...el.querySelectorAll('button')].find((b) => b.textContent === 'Add')!.click() })
    expect(sent[0]).toEqual({ expected: ['10.0.0.0/8'], networks: ['10.0.0.0/8', '192.0.2.0/24'] })
    await vi.waitFor(() => expect(el.textContent).toContain('198.51.100.0/24'))
    expect(el.textContent).toContain('The list changed since this page read it')
    expect((el.querySelector('input') as HTMLInputElement).value).toBe('192.0.2.0/24')
    act(() => root.unmount())
  })
})

describe('inviting a person', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // The administrator names the account and role and gets the link once;
  // pending invites are listed without their links.
  it('creates an invite and shows its link once', async () => {
    const sent: { method: string; url: string; body?: unknown }[] = []
    let pending: unknown[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      sent.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (method === 'POST') {
        const invite = { id: '00000000-0004-7000-8000-000000000000', username: 'kim', label: 'Kim Lee', role: 'admin', invitedBy: 'bls', createdAt: '2026-10-03T00:00:00Z', expiresAt: '2026-10-10T00:00:00Z', state: 'pending' }
        pending = [invite]
        return new Response(JSON.stringify({ invite, link: 'https://forge.test/invite/apptown_invite_abc', note: '' }), { status: 201 })
      }
      return new Response(JSON.stringify({ invites: pending }))
    }))
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => { root.render(<QueryClientProvider client={newQueryClient()}><InvitePerson /></QueryClientProvider>) })
    const [name, label] = [...el.querySelectorAll('input')] as HTMLInputElement[]
    const type = async (input: HTMLInputElement, value: string) => act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await type(name, 'kim')
    await type(label, 'Kim Lee')
    const select = el.querySelector('select') as HTMLSelectElement
    // An invitation assigns a concrete account role; token ceilings do not apply.
    expect([...select.options].map(o => o.value)).toEqual(['member', 'admin'])
    expect(select.title).not.toContain('token')
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')!.set!.call(select, 'admin')
      select.dispatchEvent(new Event('change', { bubbles: true }))
    })
    await act(async () => { [...el.querySelectorAll('button')].find((b) => b.textContent === 'Create invite link')!.click() })
    expect(sent.find((s) => s.method === 'POST')).toEqual({ method: 'POST', url: '/api/v1/invites', body: { username: 'kim', label: 'Kim Lee', role: 'admin' } })
    await vi.waitFor(() => expect(el.textContent).toContain('https://forge.test/invite/apptown_invite_abc'))
    await vi.waitFor(() => expect(el.textContent).toContain('invited by bls'))
    await act(async () => { [...el.querySelectorAll('button')].find((b) => b.textContent === 'Done')!.click() })
    expect(el.textContent).not.toContain('apptown_invite_abc')
    expect(el.textContent).toContain('kim')
    act(() => root.unmount())
  })
})

describe('disabling an account', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  const account = (username: string, disabled: boolean, role = 'member'): Account =>
    ({ principalId: '00000000-0001-7000-8000-000000000000', username, role, hasPassword: true, acted: true, lastSeen: '2026-10-02T00:00:00Z', disabled, groups: [] })
  const buttons = (el: HTMLElement) => [...el.querySelectorAll('button')]
  const press = async (el: HTMLElement, text: string) => act(async () => { buttons(el).find((b) => b.textContent === text)!.click() })

  // An administrator disables an account after confirming, sending no role so
  // its authority stays as it is, and enables a disabled one the same way.
  // Their own account offers neither.
  it('asks, then sends the account and its new state, no role', async () => {
    const sent: unknown[] = []
    const act$ = (fn: () => Promise<unknown>) => { void fn() }
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
      sent.push(JSON.parse(String(init?.body)))
      return new Response(JSON.stringify({ ok: true }))
    }))
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={newQueryClient()}>
        <UsersCard users={[account('bls', false, 'admin'), account('kim', false, 'admin'), account('probe', true)]} act={act$} canEdit invites={false} me="bls" />
      </QueryClientProvider>)
    })
    expect(buttons(el).filter((b) => b.textContent === 'disable')).toHaveLength(1)
    expect(el.textContent).toContain('disabled')
    await press(el, 'disable')
    expect(sent).toHaveLength(0)
    expect(el.textContent).toContain('Disable kim? They are signed out at once')
    await press(el, 'Yes')
    await vi.waitFor(() => expect(sent).toEqual([{ username: 'kim', disabled: true }]))
    await press(el, 'enable')
    expect(el.textContent).toContain('Enable probe?')
    await press(el, 'Yes')
    await vi.waitFor(() => expect(sent[1]).toEqual({ username: 'probe', disabled: false }))
    act(() => root.unmount())
  })

  // A single sign-on account never has a password; once it has signed in or
  // acted it is an account, offered disable and never remove. Only a name
  // that never signed in or acted is offered removal.
  it('offers disable, not removal, for an account without a password that has acted', async () => {
    const el = document.createElement('div')
    const root = createRoot(el)
    const sso: Account = { principalId: '00000000-0001-7000-8000-000000000000', username: 'sso', role: 'member', hasPassword: false, acted: true, lastSeen: '', disabled: false, groups: [] }
    const typo: Account = { principalId: '00000000-0001-7000-8000-000000000000', username: 'typo', role: 'member', hasPassword: false, acted: false, lastSeen: '', disabled: false, groups: [] }
    await act(async () => {
      root.render(<QueryClientProvider client={newQueryClient()}>
        <UsersCard users={[sso, typo]} act={() => {}} canEdit invites={false} me="bls" />
      </QueryClientProvider>)
    })
    const ssoRow = [...el.querySelectorAll('tr')].find((r) => r.textContent?.includes('sso'))
    expect([...(ssoRow?.querySelectorAll('button') ?? [])].map((b) => b.textContent)).toContain('disable')
    expect(el.textContent).toContain('names without an account (1)')
    expect(buttons(el).filter((b) => b.textContent === 'remove')).toHaveLength(1)
    act(() => root.unmount())
  })

  // Someone who cannot change accounts sees the state but no control.
  it('offers no control without the right to change accounts', async () => {
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={newQueryClient()}>
        <UsersCard users={[account('kim', false), account('probe', true)]} act={() => {}} canEdit={false} invites={false} />
      </QueryClientProvider>)
    })
    expect(buttons(el).map((b) => b.textContent)).not.toContain('disable')
    expect(buttons(el).map((b) => b.textContent)).not.toContain('enable')
    act(() => root.unmount())
  })

  // A new password, or claiming a name the record has seen, changes only the
  // password: no role is sent, so an administrator through a group gains no
  // direct grant from a reset.
  it('sets a password without sending a role', async () => {
    const sent: unknown[] = []
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
      sent.push(JSON.parse(String(init?.body)))
      return new Response(JSON.stringify({ ok: true }))
    }))
    const seen: Account = { principalId: '00000000-0001-7000-8000-000000000000', username: 'ghost', role: 'admin', hasPassword: false, acted: false, lastSeen: '', disabled: false, groups: [] }
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={newQueryClient()}>
        <UsersCard users={[account('kim', false, 'admin'), seen]} act={(fn) => { void fn() }} canEdit invites={false} me="bls" />
      </QueryClientProvider>)
    })
    const typePassword = async () => act(async () => {
      const input = el.querySelector('input[type=password][placeholder="min 8 characters"]') as HTMLInputElement
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'new-password-1')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await press(el, 'set password')
    await typePassword()
    await press(el, 'Set')
    await vi.waitFor(() => expect(sent).toEqual([{ username: 'kim', password: 'new-password-1' }]))
    await press(el, 'claim')
    await typePassword()
    await press(el, 'Claim')
    await vi.waitFor(() => expect(sent[1]).toEqual({ username: 'ghost', password: 'new-password-1' }))
    act(() => root.unmount())
  })
})

describe('a password reset link', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  const account = (username: string, disabled: boolean): Account =>
    ({ principalId: '00000000-0001-7000-8000-000000000000', username, role: 'member', hasPassword: true, acted: true, lastSeen: '2026-10-02T00:00:00Z', disabled, groups: [] })
  const buttons = (el: HTMLElement) => [...el.querySelectorAll('button')]
  const press = async (el: HTMLElement, text: string) => act(async () => { buttons(el).find((b) => b.textContent === text)!.click() })

  // An administrator asks for a reset link for an enabled account and sees
  // it once with a copy button; only the name is sent, so the reset can
  // carry no role. A disabled account offers no reset link.
  it('asks, makes the link for the account and shows it once', async () => {
    const posted: { url: string; body: unknown }[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        posted.push({ url, body: JSON.parse(String(init.body)) })
        const invite = { id: '00000000-0007-7000-8000-000000000000', purpose: 'password', username: 'kim', label: '', role: '', invitedBy: 'bls', createdAt: '2026-10-08T00:00:00Z', expiresAt: '2026-10-11T00:00:00Z', state: 'pending' }
        return new Response(JSON.stringify({ invite, link: 'https://forge.test/invite/apptown_invite_reset', note: '' }), { status: 201 })
      }
      return new Response(JSON.stringify({ invites: [] }))
    }))
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={newQueryClient()}>
        <UsersCard users={[account('kim', false), account('probe', true)]} act={() => {}} canEdit invites me="bls" />
      </QueryClientProvider>)
    })
    expect(buttons(el).filter((b) => b.textContent === 'reset password')).toHaveLength(1)
    await press(el, 'reset password')
    expect(posted).toHaveLength(0)
    expect(el.textContent).toContain('Make a password reset link for kim?')
    await press(el, 'Make link')
    await vi.waitFor(() => expect(el.textContent).toContain('https://forge.test/invite/apptown_invite_reset'))
    expect(posted).toEqual([{ url: '/api/v1/password-links', body: { username: 'kim' } }])
    expect(el.textContent).toContain('Password reset link for kim')
    expect(buttons(el).map((b) => b.textContent)).toContain('Copy link')
    await press(el, 'Done')
    expect(el.textContent).not.toContain('apptown_invite_reset')
    act(() => root.unmount())
  })

  // A second link made without dismissing the first says Copy link until it
  // has itself been copied: the clipboard still holds the first one.
  it('does not say Copied for a newer link', async () => {
    const clipboard: string[] = []
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: async (t: string) => { clipboard.push(t) } }, configurable: true })
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        const { username } = JSON.parse(String(init.body)) as { username: string }
        const invite = { id: '00000000-0001-7000-8000-000000000000', purpose: 'password', username, label: '', role: '', invitedBy: 'bls', createdAt: '2026-10-08T00:00:00Z', expiresAt: '2026-10-11T00:00:00Z', state: 'pending' }
        return new Response(JSON.stringify({ invite, link: `https://forge.test/invite/apptown_invite_${username}`, note: '' }), { status: 201 })
      }
      return new Response(JSON.stringify({ invites: [] }))
    }))
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={newQueryClient()}>
        <UsersCard users={[account('kim', false), account('lee', false)]} act={() => {}} canEdit invites me="bls" />
      </QueryClientProvider>)
    })
    const resetFor = async (name: string) => {
      const row = [...el.querySelectorAll('tr')].find((r) => r.textContent?.includes(name))!
      await act(async () => { [...row.querySelectorAll('button')].find((b) => b.textContent === 'reset password')!.click() })
      await press(el, 'Make link')
      await vi.waitFor(() => expect(el.textContent).toContain(`apptown_invite_${name}`))
    }
    await resetFor('kim')
    await press(el, 'Copy link')
    await vi.waitFor(() => expect(buttons(el).map((b) => b.textContent)).toContain('Copied'))
    expect(clipboard).toEqual(['https://forge.test/invite/apptown_invite_kim'])
    await resetFor('lee')
    expect(buttons(el).map((b) => b.textContent)).toContain('Copy link')
    expect(buttons(el).map((b) => b.textContent)).not.toContain('Copied')
    act(() => root.unmount())
  })

  // Under single sign-on AppTown holds no passwords: no reset button, and
  // the page says where passwords are reset instead.
  it('says where to reset a password under single sign-on', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ invites: [] }))))
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={newQueryClient()}>
        <UsersCard users={[account('kim', false)]} act={() => {}} canEdit invites={false} sso me="bls" />
      </QueryClientProvider>)
    })
    expect(buttons(el).map((b) => b.textContent)).not.toContain('reset password')
    expect(el.textContent).toContain("Password reset links need AppTown's own sign-in (--auth internal); with single sign-on, reset the password in your identity provider.")
    act(() => root.unmount())
  })
})

describe('minting a token', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // A token's role is left to AppTown unless narrowed: a command-line token
  // then carries its person's own role (an administrator's can release), an
  // agent token a member's. Kills: the page's old member default.
  it('sends no role unless one is chosen', async () => {
    const sent: { method: string; url: string; body?: unknown }[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      sent.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (method === 'POST') return new Response(JSON.stringify({ token: 'apptown_t', name: 'laptop-cli', user: 'bls', role: 'admin', purpose: 'cli', note: '' }), { status: 201 })
      if (url.includes('/users')) return new Response(JSON.stringify({ users: [] }))
      return new Response(JSON.stringify({ tokens: [] }))
    }))
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => { root.render(<QueryClientProvider client={newQueryClient()}><TokensCard /></QueryClientProvider>) })
    const input = el.querySelector('input') as HTMLInputElement
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'laptop-cli')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    const purpose = [...el.querySelectorAll('select')].find((s) => s.title === 'who will hold the token') as HTMLSelectElement
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')!.set!.call(purpose, 'cli')
      purpose.dispatchEvent(new Event('change', { bubbles: true }))
    })
    await act(async () => { [...el.querySelectorAll('button')].find((b) => b.textContent === 'Mint token')!.click() })
    expect(sent.find((s) => s.method === 'POST')).toEqual({ method: 'POST', url: '/api/v1/tokens', body: { name: 'laptop-cli', purpose: 'cli' } })
    await vi.waitFor(() => expect(el.textContent).toContain('apptown_t'))
    act(() => root.unmount())
  })
})
