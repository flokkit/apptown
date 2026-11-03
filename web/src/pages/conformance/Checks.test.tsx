import { expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import type { ConformanceView } from '../../wire.gen'
import { ChecksList } from './Checks'
vi.mock('./parts', async (original) => ({ ...await original<typeof import('./parts')>(), useStartRemediation: () => ({ start: vi.fn(), busy: false, err: '' }) }))

it('shows findings and their Fix action on the row while results stay closed', () => {
 const v = { projectSlug: 'lunch', lane: 'findings', state: 'does-not-conform', statusLabel: 'failed', severity: 'block', block: 'docs', check: 'readme', detail: 'README missing' }
 const view = { openRemediations: [], lens: { kind: 'current' }, adjudications: {}, checkDefs: { readme: { key: 'readme', title: 'README', adopters: [], severity: 'block' }, quiet: { key: 'quiet', title: 'Quiet' } }, byCheck: { readme: [v], quiet: [{ ...v, lane: 'conforms', state: 'conforms', statusLabel: 'passed', projectSlug: 'quiet-project' }] } } as unknown as ConformanceView
 const html = renderToStaticMarkup(<MemoryRouter><ChecksList view={view} base="/conformance" /></MemoryRouter>)
 expect(html).toContain('Fix everywhere')
 expect(html).toContain('1 finding')
 expect(html).not.toContain('README missing')
 expect(html).not.toContain('quiet-project')
})

it('shows findings on a closed group of versions of the same check', () => {
 const passed = { projectSlug: 'one', lane: 'conforms', state: 'conforms', statusLabel: 'passed' }
 const failed = { projectSlug: 'two', lane: 'findings', state: 'does-not-conform', statusLabel: 'failed' }
 const view = { openRemediations: [], lens: { kind: 'current' }, adjudications: {}, checkDefs: { v1: { key: 'v1', title: 'Lint', adopters: [], block: 'style', check: 'lint', version: 1 }, v2: { key: 'v2', title: 'Lint', adopters: [], block: 'style', check: 'lint', version: 2 } }, byCheck: { v1: [passed], v2: [failed] } } as unknown as ConformanceView
 const html = renderToStaticMarkup(<MemoryRouter><ChecksList view={view} base="/conformance" /></MemoryRouter>)
 // The row itself, before it is opened, says there is a finding.
 const row = html.slice(html.indexOf('check-row'), html.indexOf('</div>', html.indexOf('check-row')))
 expect(row).toContain('1 finding')
 expect(row).toContain('1 passed')
 expect(html).not.toContain('Version 1')
})
