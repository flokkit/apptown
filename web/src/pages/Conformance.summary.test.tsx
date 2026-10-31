import { expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import Conformance from './Conformance'
vi.mock('../data', () => ({
  useGet: (path: string) => ({ data: path === '/conformance' ? {
    lens: { kind: 'current' }, reviews: [], projects: [],
    summary: { findings: 0, flagged: 0, incompleteProjects: 1, byStanding: { 'review-broken': 1 } },
  } : null, reload: () => {}, current: true }),
  useAction: () => ({ run: () => {}, busy: false, err: '' }),
}))
it('uses the owner’s incomplete count even when a failed evaluator has priority in standing', () => {
 const html = renderToStaticMarkup(<MemoryRouter><Conformance tab="projects" /></MemoryRouter>)
 expect(html).toContain('1 app not fully checked')
 expect(html).not.toContain('0 apps not fully checked')
})
