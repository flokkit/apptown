// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { NameStep } from './NameStep'
import { newDraft } from './draft'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it.each([false, true])('replaces a suggested name on paste and preserves normal paste for an edited name (%s)', nameEdited => {
 const el = document.createElement('div'), root = createRoot(el), dispatch = vi.fn()
 act(() => root.render(<NameStep d={{ ...newDraft(), name: 'Suggested', nameEdited }} act={dispatch} />))
 const event = new Event('paste', { bubbles: true, cancelable: true })
 Object.defineProperty(event, 'clipboardData', { value: { getData: () => 'My app' } })
 act(() => el.querySelector('input')!.dispatchEvent(event))
 expect(event.defaultPrevented).toBe(!nameEdited)
 if (!nameEdited) expect(dispatch).toHaveBeenCalledWith({ type: 'name', name: 'My app' })
 else expect(dispatch).not.toHaveBeenCalled()
 act(() => root.unmount())
})
