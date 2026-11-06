// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it } from 'vitest'
import { Status } from './Status'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('keeps status explanations readable inside linked rows without a fold or an extra control', () => {
  const el = document.createElement('div'), root = createRoot(el)
  try {
    act(() => root.render(<a href="/deployment"><Status word="unreachable" help="The app may still be running." /></a>))
    expect(el.querySelector('details, summary, button')).toBeNull()
    expect(el.querySelector('.status-help-inline')?.textContent).toContain('The app may still be running.')
    expect(el.querySelector('a')?.getAttribute('href')).toBe('/deployment')
    expect(el.querySelector('[role="status"]')).toBeNull()
  } finally { act(() => root.unmount()) }
})
