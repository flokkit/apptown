import { enabledFeatures } from '../features.fixture'
import { expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import Conformance from './Conformance'
vi.mock('../data', () => ({
  useGet: (path: string) => ({ data: path === '/features' ? enabledFeatures : path === '/conformance' ? {
    openRemediations: [], byProject: {}, lens: { kind: 'current' }, reviews: [], projects: [],
    summary: { findings: 0, flagged: 0, incompleteProjects: 1, byStanding: { 'review-broken': 1 } },
  } : null, reload: () => {}, current: true }),
  useAction: () => ({ run: () => {}, busy: false, err: '' }),
}))
it('uses the owner’s incomplete count even when a failed evaluator has priority in standing', () => {
 const html = renderToStaticMarkup(<MemoryRouter><Conformance tab="projects" /></MemoryRouter>)
 expect(html).toContain('1 app not fully checked')
 expect(html).not.toContain('0 apps not fully checked')
})

// A list row is a summary: three checks that could not run read as a count in
// words, and their reasons (tool output) stay on the project's own page.
it('summarises checks that could not run as a count with none of their details', async () => {
  const { projectNext } = await import('./conformance/Projects')
  const detail = (tool: string) => `${tool}: web: error (exit -1): the tour has no Docker to run ${tool}:v1.2.3-alpine`
  const p = {
    projectId: null, slug: 'lunch', name: 'Lunch Orders', statusLabel: 'unknown', standing: 'review-broken',
    sentence: `Review incomplete · 3 not known: ${['hadolint', 'semgrep', 'uv'].map(detail).join('; ')}`,
    lanes: { conforms: 0, excepted: 0, findings: 0, flagged: 0, unassessed: 0, broken: 3, na: 0 }, total: 3,
    notKnown: ['hadolint', 'semgrep', 'uv'].map((t) => ({ detail: detail(t), evaluator: 'tool' })),
    judged: 0, high: 0, adopted: [], sha: '', judgedAt: '',
  }
  const view = { byProject: {}, checkDefs: {} }
  const fact = projectNext(p as never, view as never)
  expect(fact).toBe("3 checks couldn't run")
  for (const word of ['hadolint', 'semgrep', 'exit -1', 'alpine']) expect(fact).not.toContain(word)
})
