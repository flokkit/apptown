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
  return { standing: { enabled: false, level: 'routine' }, heldBack: [], queues: [{ queue: 'security', state: 'stuck', failed: 1, since: source.observedAt, reason: 'tests: failed', packages: [], changeId: 'open-change', changeNumber: 1 }, { queue: 'updates', state: 'up_to_date', failed: 0, since: '', reason: '', packages: [], changeNumber: 0 }], scans: { main: source.subject.commit!, source: [source], running: [{ ...observation('image'), subject: { on: 'running', deployment: 'Production', part: 'web', digest: 'registry.example/app@sha256:012345abcdef67890123' }, scanner: 'trivy', verdict: 'fail', findings: [npm, os], projectFindings: [npm], imageOnlyFindings: [os], unclassifiedFindings: [] }], historical: [observation('old')], known: true, stale: false, lastScan: source.observedAt }, recent: [{ schedule: 'janitor-scan', ruleRevision: 0, attempted: true, failed: true, changeId: 'open-change', id: 'try', projectId: 'project', openChange: 'open-change', queue: 'security', what: 'Scan the project', when: source.observedAt, outcome: '', reason: '', observations: [], fingerprint: '', packages: [] }] }
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
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.includes('/authz/me') ? { project: { 'project:manage-standing-approval': true } } : data))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><LastScan slug="project" janitor={janitor} /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(client.getQueryCache().getAll()[0]?.state.status).toBe('success'))
    await vi.waitFor(() => expect(el.textContent).not.toContain('Loading…'))
    check(el)
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
}

it('shows queue states, findings, and recent tries linked to their Changes', async () => {
  await render(fixture(), true, el => {
    expect(el.textContent).toContain('· 2 findings')
    expect([...el.querySelectorAll('h3')].map(h => h.textContent)).toEqual(['Project’s findings', 'Running image only', 'Recent tries'])
    expect(el.textContent?.match(/CVE-lodash/g)).toHaveLength(1)
    expect(el.textContent).toContain('CVE-libssl')
    expect(el.querySelector('a')?.textContent).toBe('Try again')
    expect(el.querySelector('a')?.getAttribute('href')).toBe('/changes/open-change')
    expect(el.querySelector('table')?.getAttribute('aria-label')).toBe('Recent tries')
    expect(el.textContent).not.toMatch(/Earlier|Automatic updates|base-image/)
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
    expect(el.querySelector('h3')?.textContent).toBe('Recent tries')
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

it('puts rollback first when releases hold back a project kept by the janitor', async () => {
  const data = fixture()
  data.standing = { enabled: true, level: 'routine' }
  data.heldBack = [{ deploymentId: 'main', deployment: 'production', displayLabel: 'Production', releaseNumber: 12, reason: 'release failed' }]
  data.recent[0].ruleId = 'original-rule'
  data.recent[0].rule = { kind: 'Dependency updates', scope: 'all projects' }
  data.recent[0].ruleRevision = 3
  data.recent[0].ruleIssuer = { name: 'Priya', principal: 'issuer', via: 'session' }
  await render(data, true, el => {
    expect(el.textContent).toContain('Kept updated by the janitor · routine')
    expect([...el.querySelectorAll('button')].some(b => b.textContent === 'turn off')).toBe(true)
    expect(el.textContent).toContain('Stopped: release 12 to Production failed.')
    const links = [...el.querySelectorAll('a')]
    expect(links.slice(0, 2).map(a => a.textContent)).toEqual(['Roll back', 'Try again'])
    expect(links[0].getAttribute('href')).toBe('/projects/project/deployments/production?tab=history&rollbackBefore=12')
    expect(links[1].getAttribute('href')).toBe('/projects/project/deployments/production?tab=history')
    expect(el.textContent).toContain('Dependency updates approval for all projects (revision 3), granted by Priya')
  })
})

it('shows the off level with its recorded person and time', async () => {
  const data = fixture()
  data.standing = { enabled: true, level: 'off', choice: { person: { name: 'Sam', principal: 'person', via: '' }, at: '2026-11-12T00:00:00Z', reason: 'Recovery' } }
  await render(data, true, el => {
    expect(el.textContent).toContain('Turned off for this project · Sam')
    expect(el.textContent).not.toContain('Kept updated by the janitor · routine')
  })
})

it('names the aggressive level without calling its update queue weekly', async () => {
  const data = fixture()
  data.standing = { enabled: true, level: 'aggressive' }
  await render(data, true, el => {
    expect(el.textContent).toContain('Kept updated by the janitor · aggressive')
    expect(el.textContent).toContain('Updates')
    expect(el.textContent).not.toContain('Weekly updates')
  })
})

it('names pack work in the queue and recent tries reached from the janitor page', async () => {
 const data = fixture()
 data.queues.push({ ...data.queues[0], queue: 'packs', state: 'up_to_date', failed: 0, reason: '', changeId: undefined })
 data.recent[0].queue = 'packs'
 await render(data, true, el => {
  expect(el.querySelector('table[aria-label="Recent tries"] tbody tr td:nth-child(2)')?.textContent).toBe('Pack updates')
  expect(el.textContent?.match(/Pack updates/g)?.length).toBe(2)
 })
})
