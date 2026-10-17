// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MainNote } from './Project'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function render(main: Parameters<typeof MainNote>[0]['main']) {
  const el = document.createElement('div')
  const root = createRoot(el)
  act(() => root.render(<MainNote main={main} />))
  const text = el.textContent ?? ''
  act(() => root.unmount())
  return text
}

// Main at its record says nothing; a difference and the last fetched upstream
// are shown as the owner words them, and nothing else is claimed.
it('shows the main note only when main differs from its record', () => {
  expect(render({ state: 'recorded' })).toBe('')
  expect(render(null)).toBe('')
  expect(render({ state: 'ahead', ahead: 2, note: "main is 2 commits ahead of the forge's record at abc1234" }))
    .toBe("Main is 2 commits ahead of the forge's record at abc1234.")
  expect(render({ state: 'recorded', upstream: { known: true, ahead: 1, note: "last fetched upstream has 1 commit the forge's record does not" } }))
    .toBe("Last fetched upstream has 1 commit the forge's record does not.")
})
