import { expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { ReviewsList } from './Reviews'
const { reviews } = vi.hoisted(() => ({ reviews: [] as Record<string, unknown>[] }))
vi.mock('../../data', () => ({ useGet: () => ({ data: { reviews } }) }))

it('distinguishes a clean assessment from incomplete checks, evaluator errors and pending review', () => {
  const record = { projects: 2, status: 'done', statusLabel: 'finished', startedAt: '2026-10-31T01:00:00Z', findings: 0, judged: 2, total: 2, errors: 0, flagged: 0 }
  reviews.splice(0, reviews.length,
    { ...record, id: 'clean' }, { ...record, id: 'incomplete', judged: 0 },
    { ...record, id: 'broken', errors: 1 }, { ...record, id: 'flagged', flagged: 1 })
  const html = renderToStaticMarkup(<MemoryRouter><ReviewsList /></MemoryRouter>)
  expect(html).toContain('31 Oct')
  expect(html).toContain('href="/conformance/reviews/clean"')
  expect(html).toContain('Not fully checked')
  expect(html).toContain('Checks could not run')
  expect(html).toContain('1 possible problem to review')
  expect(html).toContain('0 confirmed problems at this review')
})

 it('keeps a failed review distinct from quiet completion and shows live progress', () => {
  const record = { projects: 2, projectsTotal: 3, projectsDone: 1, startedAt: '2026-10-31T01:00:00Z', findings: 0, judged: 0, total: 0, errors: 0, flagged: 0, actor: 'Alice' }
  reviews.splice(0, reviews.length, { ...record, id: 'failed', status: 'error', statusLabel: 'failed' }, { ...record, id: 'quiet', status: 'done', statusLabel: 'finished' }, { ...record, id: 'running', status: 'running', statusLabel: 'working' })
  const html = renderToStaticMarkup(<MemoryRouter><ReviewsList /></MemoryRouter>)
  expect(html).toMatch(/class="chip[^"]*\bfail\b[^>]*>[\s\S]*?failed/)
  expect(html).toContain('in progress')
  expect(html).toContain('1 of 3 apps checked')
  expect(html).toContain('finished')
  expect(html).toContain('Started by')
  expect(html).toContain('>Alice<')
 })

// A finished review whose checks could not all run is incomplete (amber), never a green finish.
it('marks a finished review with checks that could not run as incomplete', () => {
  const record = { projects: 2, projectsTotal: 2, projectsDone: 2, startedAt: '2026-10-31T01:00:00Z', findings: 0, judged: 1, total: 2, flagged: 0, actor: 'Alice', status: 'done', statusLabel: 'finished' }
  reviews.splice(0, reviews.length, { ...record, id: 'partial', errors: 1 })
  const html = renderToStaticMarkup(<MemoryRouter><ReviewsList /></MemoryRouter>)
  expect(html).toMatch(/chip pending[^>]*>[\s\S]*?incomplete/)
  expect(html).not.toContain('chip pass')
})
