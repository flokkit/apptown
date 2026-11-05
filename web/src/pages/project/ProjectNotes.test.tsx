// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { newQueryClient } from '../../data'
import { ProjectNotes } from './ProjectNotes'
import { NotesDiff } from '../change/NotesDiff'
import type { ProjectNotesView } from '../../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const cleanups: (() => void)[] = []
afterEach(() => { cleanups.splice(0).forEach(clean => clean()); vi.unstubAllGlobals() })
const note = { text: 'Real photos only.', author: 'agent', change: 4, line: '- Real photos only. (agent · change 4)', changeId: '00000000-0004-7000-8000-000000000000', acceptedBy: ['Priya'] }
function mount(notes: ProjectNotesView, writable = true) {
  const el = document.createElement('div'), root = createRoot(el)
  act(() => root.render(<MemoryRouter><QueryClientProvider client={newQueryClient()}><ProjectNotes slug="photos" notes={notes} writable={writable} /></QueryClientProvider></MemoryRouter>))
  cleanups.push(() => act(() => root.unmount()))
  return el
}
it('offers a governed starting change for an absent file', async () => {
  const fetch = vi.fn(async () => new Response(JSON.stringify({ change: { id: 'new-change' } })))
  vi.stubGlobal('fetch', fetch)
  const el = mount({ present: false, commit: 'abc', notes: [] })
  expect(el.textContent).toContain('No notes yet')
  await act(async () => { el.querySelector('button')!.click() })
  expect(fetch.mock.calls[0]).toEqual(['/api/v1/catalog-apply/notes/start', expect.objectContaining({ body: JSON.stringify({ project: 'photos', params: {} }) })])
})
it('keeps authorship separate from recorded acceptance and removes the exact line through a change', async () => {
  const fetch = vi.fn(async () => new Response(JSON.stringify({ change: { id: 'new-change' } })))
  vi.stubGlobal('fetch', fetch)
  const el = mount({ present: true, commit: 'abc', notes: [note] })
  expect(el.textContent).toContain('agent · change 4')
  expect(el.textContent).toContain('Accepted by Priya')
  expect(el.querySelector('a')!.getAttribute('href')).toBe(`/changes/${note.changeId}`)
  await act(async () => { [...el.querySelectorAll('button')].find(b => b.textContent === 'Remove')!.click() })
  expect(fetch.mock.calls[0]).toEqual(['/api/v1/catalog-apply/notes/remove', expect.objectContaining({ body: JSON.stringify({ project: 'photos', params: { line: note.line } }) })])
})
it('adds the entered fact through a catalog change', async () => {
  const fetch = vi.fn(async () => new Response(JSON.stringify({ change: { id: 'new-change' } })))
  vi.stubGlobal('fetch', fetch)
  const el = mount({ present: true, commit: 'abc', notes: [] })
  act(() => { el.querySelector('button')!.click() })
  act(() => {
    const field = el.querySelector('textarea')!
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(field, 'Credit the source.')
    field.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await act(async () => { el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })) })
  expect(fetch.mock.calls[0]).toEqual(['/api/v1/catalog-apply/notes/add', expect.objectContaining({ body: JSON.stringify({ project: 'photos', params: { text: 'Credit the source.' } }) })])
})
it('shows unreadable notes as an error and hides editing for a reader', () => {
  expect(mount({ present: true, commit: 'abc', notes: [], error: 'Could not read project notes' }).textContent).not.toContain('No notes yet')
  const el = mount({ present: true, commit: 'abc', notes: [note] }, false)
  expect(el.querySelector('button')).toBeNull()
})
it('shows every proposed line, including malformed notes, beside the note counts', () => {
  const el = document.createElement('div'), root = createRoot(el)
  act(() => root.render(<NotesDiff diff={{ added: [note.line, 'malformed proposal'], removed: ['- Old. (Sam · change 1)'] }} />))
  expect(el.textContent).toContain('Project notes: adds 1, removes 1')
  expect(el.querySelectorAll('ins')).toHaveLength(2)
  expect(el.querySelector('del')!.textContent).toBe('- Old. (Sam · change 1)')
  act(() => root.unmount())
})
