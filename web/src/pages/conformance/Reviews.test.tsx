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
  expect(html).toContain('Not fully checked')
  expect(html).toContain('Checks could not run')
  expect(html).toContain('Needs your review')
  expect(html).toContain('0 problems at this check')
})

 it('keeps a failed review distinct from quiet completion and shows live progress', () => {
  const record = { projects: 2, projectsTotal: 3, projectsDone: 1, startedAt: '2026-10-31T01:00:00Z', findings: 0, judged: 0, total: 0, errors: 0, flagged: 0, actor: 'Alice' }
  reviews.splice(0, reviews.length, { ...record, id: 'failed', status: 'error', statusLabel: 'failed' }, { ...record, id: 'quiet', status: 'done', statusLabel: 'finished' }, { ...record, id: 'running', status: 'running', statusLabel: 'working' })
  const html = renderToStaticMarkup(<MemoryRouter><ReviewsList /></MemoryRouter>)
  expect(html).toMatch(/chip fail[^>]*[\s\S]*?failed/)
  expect(html).toContain('finished')
  expect(html).toContain('1 of 3 apps checked')
  expect(html).toContain('working')
  expect(html).toContain('<summary>Details</summary>Started by Alice')
 })
