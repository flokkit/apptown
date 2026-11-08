import { ProjectsList } from './Projects'
import type { ConformanceView } from '../../wire.gen'
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { JudgedAt, LensBanner, BeingFixed, fixableFindings, type Verdict, ReviewFeed, ReviewProgress, type LiveReview } from './parts'

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

// The date and source belong to each check, even when landings update only some checks.
it('shows each judgement’s commit, date and numbered source, and warns about older commits', () => {
  for (const [trigger, source] of [['landing', 'landing'], ['remediation', 'remediation'], ['manual', 'review']]) {
    const v = { sha: 'abcdef123456', at: '2026-11-07T03:02:00Z', reviewId: 'r', state: 'stale' } as Verdict
    const reviews = [{ id: 'r', number: 12, trigger }] as ConformanceView['reviews']
    const html = renderToStaticMarkup(<MemoryRouter><JudgedAt v={v} reviews={reviews} /></MemoryRouter>)
    expect(html).toContain('judged at')
    expect(html).toContain('abcdef12')
    expect(html).toContain('7 Nov')
    expect(html).toContain(`${source} #12`)
    expect(html).toContain('href="/conformance/reviews/r"')
    expect(html).toContain('judged on an older commit')
  }
  const html = renderToStaticMarkup(<JudgedAt v={{} as Verdict} reviews={[]} />)
  expect(html).toContain('Not judged yet')
  expect(html).not.toContain('via review')
})

it('labels current state and dated snapshots distinctly', () => {
  const current = { lens: { kind: 'current' } } as ConformanceView
  const render = (view: ConformanceView) => renderToStaticMarkup(<MemoryRouter><LensBanner view={view} /></MemoryRouter>)
  expect(render(current)).toContain('Current state')
  const snapshot = { lens: { kind: 'review', review: { number: 3, projects: 2, status: 'done', finishedAt: '2026-11-07T03:02:00Z' } } } as ConformanceView
  expect(render(snapshot)).toContain('Snapshot as of 7 Nov')
  expect(render(snapshot)).toContain('Back to current')
  expect(render(snapshot)).not.toContain('newest evidence')
})

// The roll-up is one line per project; each check's evidence (its date and
// source) is on the project's own page, which the row opens.
it('keeps the fleet roll-up to one line per project and leaves check evidence to its page', () => {
  const view = { lens: { kind: 'current' }, projects: [{ slug: 'shop', standing: 'conforms-so-far', sentence: 'Old evidence', lanes: { unassessed: 1 }, notKnown: [], adopted: [] }],
    byProject: { shop: [{ check: 'clean', sha: 'abcdef123456', at: '2026-11-07T03:02:00Z', reviewId: 'r', state: 'stale', lane: 'unassessed' }] },
    reviews: [{ id: 'r', number: 12, trigger: 'landing' }], checkDefs: {} } as unknown as ConformanceView
  const html = renderToStaticMarkup(<MemoryRouter><ProjectsList view={view} base="/conformance" /></MemoryRouter>)
  expect(html).toContain('Current state')
  expect(html).not.toContain('judged at')
  expect(html).toContain('href="/conformance/projects/shop"')
})
