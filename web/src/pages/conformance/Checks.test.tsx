import { expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import type { ConformanceView } from '../../wire.gen'
import { ChecksList } from './Checks'
vi.mock('./parts', async (original) => ({ ...await original<typeof import('./parts')>(), useStartRemediation: () => ({ start: vi.fn(), busy: false, err: '' }) }))

it('shows findings and their Fix action immediately, while quiet checks stay folded', () => {
 const v = { projectSlug: 'lunch', state: 'does-not-conform', statusLabel: 'failed', severity: 'block', block: 'docs', check: 'readme', detail: 'README missing' }
 const view = { lens: { kind: 'current' }, adjudications: {}, checkDefs: { readme: { key: 'readme', title: 'README', adopters: [], severity: 'block' }, quiet: { key: 'quiet', title: 'Quiet' } }, byCheck: { readme: [v], quiet: [{ ...v, state: 'conforms', statusLabel: 'passed', projectSlug: 'quiet-project' }] } } as unknown as ConformanceView
 const html = renderToStaticMarkup(<MemoryRouter><ChecksList view={view} base="/conformance" /></MemoryRouter>)
 expect(html).toContain('Fix everywhere')
 expect(html).toContain('README missing')
 expect(html).not.toContain('quiet-project')
})
