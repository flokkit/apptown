// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import Login from './pages/Login'
import { LocalTrialNote } from './LocalTrialNote'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function render(node: React.ReactNode): HTMLDivElement {
  const el = document.createElement('div')
  document.body.appendChild(el)
  act(() => { createRoot(el).render(node) })
  return el
}

describe('the desktop trial first run', () => {
  afterEach(() => { document.body.innerHTML = ''; window.history.replaceState(null, '', '/') })

  // The trial prints http://localhost:7777/?setup=<token>: the first-account
  // form arrives with the token filled in, and the token leaves the address.
  it('fills the setup token from the ready link and clears it from the address', () => {
    window.history.replaceState(null, '', '/?setup=abc123&x=1')
    const el = render(<Login needsBootstrap onSignedIn={() => {}} />)
    const inputs = [...el.querySelectorAll('input')]
    expect(inputs.map(i => i.value)).toContain('abc123')
    expect(window.location.search).toBe('?x=1')
  })

  it('warns on this computer and nowhere else', () => {
    expect(render(<LocalTrialNote hostname="localhost" />).textContent).toContain('full control of your Docker')
    expect(render(<LocalTrialNote hostname="127.0.0.1" />).textContent).toContain('Running AppTown')
    expect(render(<LocalTrialNote hostname="forge.example.com" />).textContent).toBe('')
  })
})
