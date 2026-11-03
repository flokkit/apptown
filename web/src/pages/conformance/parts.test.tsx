import type { ConformanceView } from '../../wire.gen'
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { BeingFixed, fixableFindings, type Verdict, ReviewFeed, ReviewProgress, type LiveReview } from './parts'

it('shows an unanswered check as not known in the review feed and progress', () => {
  const d = { review: { id: 'review', status: 'done' }, verdicts: [{ projectSlug: 'lunch', lane: 'broken', state: 'error', statusLabel: 'unknown', detail: 'No Docker executor can run this check' }] } as unknown as LiveReview
  for (const part of [<ReviewFeed d={d} />, <ReviewProgress d={d} />]) {
    const html = renderToStaticMarkup(<MemoryRouter>{part}</MemoryRouter>)
    expect(html).toContain('1 not known')
    expect(html).not.toContain('1 findings')
  }
  expect(renderToStaticMarkup(<MemoryRouter><ReviewFeed d={d} /></MemoryRouter>)).toContain('No Docker executor')
})

it('keeps review deferrals out of not-known counts and uses the owner lane', () => {
  const d = { review: { id: 'review', status: 'done' }, verdicts: [
    { projectSlug: 'lunch', lane: 'unassessed', state: 'cannot-assess', statusLabel: 'unknown', detail: 'no standing deployment' },
    { projectSlug: 'lunch', lane: 'unassessed', state: 'cannot-assess', statusLabel: 'unknown', detail: 'exec-image tool runs at the land gate' },
    { projectSlug: 'lunch', lane: 'broken', state: 'error', statusLabel: 'unknown', detail: 'launch failed' },
    { projectSlug: 'lunch', lane: 'findings', state: 'does-not-conform', statusLabel: 'failed', detail: 'confirmed AI flag' },
  ] } as unknown as LiveReview
  for (const part of [<ReviewFeed d={d} />, <ReviewProgress d={d} />]) {
    const html = renderToStaticMarkup(<MemoryRouter>{part}</MemoryRouter>)
    expect(html).toContain('1 not known')
    expect(html).not.toContain('3 not known')
    expect(html).toContain('1 findings')
  }
})

it('counts only uncovered findings and links the change fixing them', () => {
  const finding = { projectId: 'project', block: 'hygiene', version: 1, check: 'clean', lane: 'findings' } as Verdict
  const change = { projectId: 'project', block: 'hygiene', version: 1, check: 'clean', changeId: 'change', number: 12 }
  const view = { openRemediations: [change] } as ConformanceView
  expect(fixableFindings(view, [finding, { ...finding, check: 'other' }])).toBe(1)
  const html = renderToStaticMarkup(<MemoryRouter><BeingFixed change={change} /></MemoryRouter>)
  expect(html).toContain('being fixed in')
  expect(html).toContain('change #12')
  expect(html).toContain('href="/changes/change"')
  expect(fixableFindings({ ...view, openRemediations: [] }, [finding])).toBe(1)
})
