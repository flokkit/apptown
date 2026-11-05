// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it } from 'vitest'
import { Modal } from './Modal'
import { FieldRow } from './FieldRow'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const el = document.createElement('div')
let root: ReturnType<typeof createRoot>
afterEach(() => { act(() => root.unmount()); el.remove() })
it.each(['Edit project', <><h2>Edit project</h2><span>Details</span></>])('names dialogs from their visible title, including rich titles', title => {
  document.body.append(el); root = createRoot(el)
  act(() => root.render(<Modal title={title} onClose={() => {}}>Body</Modal>))
  const dialog = el.querySelector('[role="dialog"]')!
  const titleElement = document.getElementById(dialog.getAttribute('aria-labelledby')!)
  expect(titleElement?.textContent).toContain('Edit project')
  expect(dialog.contains(titleElement)).toBe(true)
})
it('connects each field label to its control, preserving explicit ids', () => {
  document.body.append(el); root = createRoot(el)
  act(() => root.render(<><FieldRow label="Name"><input /></FieldRow><FieldRow label="Group"><select id="group"><option>One</option></select></FieldRow></>))
  const labels = [...el.querySelectorAll('label')]
  expect(labels[0].control).toBe(el.querySelector('input'))
  expect(labels[1].control).toBe(el.querySelector('select'))
  expect(labels[1].htmlFor).toBe('group')
})
