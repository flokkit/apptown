// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import Login from './Login'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const typeInto = (input: HTMLInputElement, value: string) => {
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  set.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

// The first account needs the setup token from AppTown's host: the form
// asks for it, names where it is, and sends it; signing in does not.
describe('the first account', () => {
  afterEach(() => { vi.unstubAllGlobals(); document.body.innerHTML = '' })

  it('asks for the setup token and sends it with the first account', async () => {
    const fetch = vi.fn(async () => new Response('{"ok":true}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetch)
    const div = document.createElement('div'); document.body.appendChild(div)
    const signedIn = vi.fn()
    await act(async () => { createRoot(div).render(<Login needsBootstrap onSignedIn={signedIn} />) })
    expect(div.textContent).toContain('setup-token')
    const [user, pass, token] = Array.from(div.querySelectorAll('input'))
    const button = div.querySelector('button')!
    await act(async () => { typeInto(user, 'bls'); typeInto(pass, 'correct-horse') })
    expect(button.disabled).toBe(true) // no token yet
    await act(async () => { typeInto(token, 'abc123') })
    await act(async () => { button.click() })
    const [url, init] = fetch.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toContain('/auth/bootstrap')
    expect(JSON.parse(String(init.body))).toEqual({ username: 'bls', password: 'correct-horse', setupToken: 'abc123' })
    expect(signedIn).toHaveBeenCalled()
  })

  it('signs in without one', async () => {
    const div = document.createElement('div'); document.body.appendChild(div)
    await act(async () => { createRoot(div).render(<Login needsBootstrap={false} onSignedIn={() => {}} />) })
    expect(div.querySelectorAll('input')).toHaveLength(2)
    expect(div.textContent).not.toContain('setup-token')
  })
})

describe('external sign-in', () => {
  it('explains occupied-name recovery without removing local sign-in', async () => {
    history.replaceState({}, '', '/?signin=name-unavailable')
    const div = document.createElement('div'); document.body.appendChild(div)
    const root = createRoot(div)
    try {
      await act(async () => { root.render(<Login needsBootstrap={false} onSignedIn={() => {}} external={{ displayName: 'Example identity', allowSignup: true }} />) })
      expect(div.textContent).toContain('Choose another username')
      const [user, pass] = Array.from(div.querySelectorAll('input'))
      await act(async () => { typeInto(user, 'boss'); typeInto(pass, 'local-password') })
      expect(Array.from(div.querySelectorAll('button')).find(b => b.textContent === 'Sign in')?.disabled).toBe(false)
    } finally {
      await act(async () => { root.unmount() })
      history.replaceState({}, '', '/')
    }
  })

  afterEach(() => { vi.unstubAllGlobals(); document.body.innerHTML = '' })
  it('sends an external name proposal without the local password and keeps local recovery on failure', async () => {
    const fetch = vi.fn(async () => new Response('{"error":"provider unavailable"}', { status: 503, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetch)
    const div = document.createElement('div'); document.body.appendChild(div)
    await act(async () => { createRoot(div).render(<Login needsBootstrap={false} onSignedIn={() => {}} external={{ displayName: 'Example identity', allowSignup: true }} />) })
    const [user, pass] = Array.from(div.querySelectorAll('input'))
    await act(async () => { typeInto(user, 'new-person'); typeInto(pass, 'local-password') })
    const button = Array.from(div.querySelectorAll('button')).find(b => b.textContent === 'Sign in with Example identity')!
    await act(async () => { button.click() })
    const [url, init] = fetch.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toContain('/auth/external/start')
    expect(JSON.parse(String(init.body))).toEqual({ name: 'new-person', returnPath: '/', password: '', link: false })
    expect(div.textContent).toContain('provider unavailable')
    expect(Array.from(div.querySelectorAll('button')).find(b => b.textContent === 'Sign in')?.disabled).toBe(false)
  })
})
