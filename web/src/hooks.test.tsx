// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { act, useLayoutEffect } from 'react'
import { createRoot } from 'react-dom/client'
import { createStore } from './hooks'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// A value set after a reader rendered but before it subscribed is still
// shown: the page publishing its crumb from a layout effect while the top
// bar mounts. The hand-built stores missed it and showed the old value.
it('a store reader sees a value set between its render and its subscription', async () => {
  const store = createStore('before')
  function Reader() { return <span>{store.use()}</span> }
  function Publisher() { useLayoutEffect(() => { store.set('after') }, []); return null }
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => { root.render(<><Reader /><Publisher /></>) })
  expect(el.textContent).toBe('after')
  act(() => root.unmount())
})
