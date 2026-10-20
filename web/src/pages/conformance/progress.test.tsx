// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { ReviewProgress, reviewProgressWords, type LiveReview } from './parts'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// A queued review has not started: it shows no count and no spinner. A
// running one shows what it has done, and its total only when that is known.
it('says how far a review has got without guessing', () => {
  expect(reviewProgressWords('queued', 0, 0)).toBe('not started yet')
  expect(reviewProgressWords('running', 3, 0)).toBe('3 projects so far')
  expect(reviewProgressWords('running', 3, 9)).toBe('3 of 9 projects')
})

function render(status: string): HTMLElement {
  const d = { review: { id: 'r1', number: 1, status, projectsDone: 0, projectsTotal: 0, projects: 0 }, verdicts: [] } as unknown as LiveReview
  const el = document.createElement('div')
  const root = createRoot(el)
  act(() => root.render(<MemoryRouter><ReviewProgress d={d} /></MemoryRouter>))
  return el
}

it('shows a queued review as waiting, with no spinner and no "of ?"', () => {
  const queued = render('queued')
  expect(queued.textContent).toContain('Queued')
  expect(queued.textContent).not.toContain('?')
  expect(queued.querySelector('[style*="spin"]')).toBeNull()
  expect(render('running').querySelector('[style*="spin"]')).not.toBeNull()
})
