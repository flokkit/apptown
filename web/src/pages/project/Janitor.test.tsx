// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import { newQueryClient } from '../../data'
import { LastScan } from './Janitor'
import type { Finding, ProjectScanView, ScanObservation } from '../../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const finding = (name: string): Finding => ({ fingerprint: name, kind: 'vuln', severity: 'high', title: name, identifiers: [`CVE-${name}`], package: { name, ecosystem: 'npm' }, source: 'scanner' })
const observation = (id: string): ScanObservation => ({ id, projectId: 'project', runId: 'run', stepId: id, attempt: 1, subject: { on: 'source', commit: 'abcdef0123456789abcdef' }, scanner: 'osv', expectedScanners: ['osv'], bindingVersion: 3, configHash: 'config', configuration: '{}', stepConfig: {}, observedAt: '2026-11-12T05:00:00Z', verdict: 'pass', findings: [] })
const fixture = (): ProjectScanView => {
  const npm = finding('lodash'), os = finding('libssl'), source = { ...observation('source'), verdict: 'fail', findings: [npm] }
  return { scans: { main: source.subject.commit!, source: [source], running: [{ ...observation('image'), subject: { on: 'running', deployment: 'Production', part: 'web', digest: 'registry.example/app@sha256:012345abcdef67890123' }, scanner: 'trivy', verdict: 'fail', findings: [npm, os], projectFindings: [npm], imageOnlyFindings: [os], unclassifiedFindings: [] }], historical: [observation('old')], known: true, stale: false, lastScan: source.observedAt }, recent: [{ id: 'try', projectId: 'project', openChange: 'open-change', queue: 'security', what: 'Scan the project', when: source.observedAt, outcome: '', reason: '', observations: [], fingerprint: '', packages: [] }] }
}

it('keeps an incomplete source comparison visibly unknown on Conformance', async () => {
  const data = fixture()
  // One source scanner answered; its peer has not published an observation.
  data.scans.source = [{ ...observation('finished-peer'), scanner: 'gitleaks' }]
  data.scans.running = []
  data.scans.historical = []
  data.scans.known = false
  data.scans.reason = 'scan not finished'
  await render(data, false, el => {
    expect(el.querySelector('p')?.textContent).toBe('Not known · scan not finished')
    expect(el.textContent).toContain('No findings')
  })
})
async function render(data: ProjectScanView, janitor: boolean, check: (el: HTMLDivElement) => void) {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(data))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><LastScan slug="project" janitor={janitor} /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(client.getQueryCache().getAll()[0]?.state.status).toBe('success'))
    await vi.waitFor(() => expect(el.textContent).not.toContain('Loading…'))
    check(el)
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
}

it('keeps Janitor to a state line, current findings split, and Scan history', async () => {
  await render(fixture(), true, el => {
    expect(el.textContent).toContain('· 2 findings')
    expect([...el.querySelectorAll('h3')].map(h => h.textContent)).toEqual(['Project’s findings', 'Running image only'])
    expect(el.textContent?.match(/CVE-lodash/g)).toHaveLength(1)
    expect(el.textContent).toContain('CVE-libssl')
    expect(el.querySelector('a')?.textContent).toBe('Scan history')
    expect(el.querySelector('a')?.getAttribute('href')).toBe('/conformance/projects/project')
    expect(el.querySelector('table, .list-row')).toBeNull()
    expect(el.textContent).not.toMatch(/Earlier|Recent tries|Automatic updates|base-image|Scan the project/)
  })
})

it('uses subject tables with short titled hashes and the newest ten earlier rows', async () => {
  const data = fixture()
  data.scans.historical = Array.from({ length: 12 }, (_, i) => ({ ...observation(`old-${i}`), scanner: `scanner-${i}`, observedAt: `2026-11-${String(i + 1).padStart(2, '0')}T01:00:00Z` }))
  await render(data, false, el => {
    const tables = el.querySelectorAll('table')
    expect(tables).toHaveLength(2)
    expect([...tables[0].querySelectorAll('th')].map(h => h.textContent)).toEqual(['Subject', 'Scanner', 'Result', 'When'])
    expect(tables[0].querySelectorAll('tbody tr')).toHaveLength(2)
    expect(tables[0].textContent).toContain('Code at abcdef012345')
    expect(tables[0].textContent).toContain('Production · web image 012345abcdef')
    expect([...tables[0].querySelectorAll('code')].map(c => [c.textContent, c.title])).toEqual([
      ['abcdef012345', data.scans.main], ['012345abcdef', data.scans.running[0].subject.digest],
    ])
    expect([...tables[0].querySelectorAll('.badge')].map(b => b.textContent)).toEqual(['1 finding', '2 findings'])
    const earlier = tables[1].querySelectorAll('tbody tr')
    expect(earlier).toHaveLength(10)
    expect(earlier[0].textContent).toContain('scanner-11')
    expect(earlier[9].textContent).toContain('scanner-2')
    expect(el.querySelector('h3')?.textContent).toBe('Earlier')
    expect(el.textContent).not.toContain('These describe')
    expect(el.querySelector('.list-row')).toBeNull()
  })
})

it('puts unknown reasons, not applicable and no findings in result cells', async () => {
  const data = fixture()
  data.scans.source = []
  data.scans.running = ['error', 'not_applicable', 'pass'].map((verdict, i) => ({ ...observation(`image-${i}`), subject: { on: 'running' }, verdict, reason: i === 0 ? 'the running bundle is not known' : i === 1 ? 'No supported packages' : '', projectFindings: [], imageOnlyFindings: [], unclassifiedFindings: [] }))
  data.scans.historical = []
  data.scans.known = false
  data.scans.stale = true
  await render(data, false, el => {
    expect([...el.querySelectorAll('tbody .badge')].map(b => b.textContent)).toEqual(['Not known', 'Not known', 'Not applicable', 'No findings'])
    expect(el.textContent).toContain('No scan of the current project code')
    expect(el.textContent).toContain('the running bundle is not known')
    expect(el.textContent).toContain('No supported packages')
    expect(el.textContent).toContain('older than three days')
  })
  await render(data, true, el => {
    expect(el.textContent).toContain('Last scan: not known')
    expect(el.textContent).not.toContain('no findings')
    expect(el.querySelector('h3')).toBeNull()
  })
})

it('keeps unclassified running findings separate when comparison is unavailable', async () => {
  const data = fixture()
  data.scans.source = []
  data.scans.running[0].projectFindings = []
  data.scans.running[0].imageOnlyFindings = []
  data.scans.running[0].unclassifiedFindings = [finding('libssl')]
  await render(data, true, el => {
    expect(el.querySelector('h3')?.textContent).toBe('Not compared with project code')
    expect(el.textContent).toContain('CVE-libssl')
    expect(el.textContent).not.toContain('Running image only')
  })
})

 it('shows a missing current running observation above completed source results', async () => {
 const data = fixture()
 data.scans.running = []
 data.scans.known = false
 data.scans.reason = 'no running image scan'
 await render(data, false, el => {
  expect(el.querySelector('p')?.textContent).toBe('Not known · no running image scan')
  expect(el.querySelector('table')?.textContent).toContain('osv')
 })
 })
