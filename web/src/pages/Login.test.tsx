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

// The first account needs the setup token from the forge's host: the form
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
