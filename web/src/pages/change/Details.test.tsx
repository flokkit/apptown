// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, useLocation, useSearchParams } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import type { Artifact, ChangeView, RunDetail, Step } from '../../api'
import { newQueryClient } from '../../data'
import { ChangeDetails, storyPhases } from './Details'
import { TechnicalDetailSwitch } from './TechnicalDetail'
import { checkResult, runStory } from './story'

Element.prototype.scrollIntoView = vi.fn()
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const step = (id: string, type: Step['type'], status: Step['status'], extra = {}) => ({ id, name: id, type, status, runId: 'work', config: {}, evidence: {}, overrideBy: '', stage: 'check', startedAt: '2026-11-01T10:00:00Z', finishedAt: '2026-11-01T10:01:00Z', ...extra } as Step)
const detail = (id: string, steps: Step[] = []): RunDetail => ({ run: { id, status: 'running', currentStage: 'check', workflowName: 'make-it', workflowVersion: 2, createdAt: '2026-11-01T09:00:00Z' }, steps, events: [], artifacts: [], next: [], landingMeans: [] } as unknown as RunDetail)
const view = (): ChangeView => ({ waitingOn: { kind: 'check', says: 'script failed', tone: 'bad', run: 'work', step: 'check', checks: 1, who: [], you: 'yes' }, change: { id: 'change', title: 'A better calendar', body: 'Show the times.\nKeep the invitation.', openedBy: 'sam', createdAt: '2026-11-01T08:00:00Z', state: 'in_progress', type: 'feature', editRevision: 1, branch: 'calendar', baseSha: 'abc' }, detail: detail('work', [step('agent', 'agent/pi', 'pass', { evidence: { durationMs: 120000, files: ['one', 'two'] } }), step('check', 'script', 'fail', { evidence: { reason: 'A start time is missing.', actions: ['Add a time to every ride.'] } }), step('review', 'agent/review', 'pass', { evidence: { verdict: 'accept', summary: 'The text is clear.\nA longer conclusion.' } }), step('accept', 'approval', 'pending', { startedAt: '', config: { approvers: ['priya'] } })]), runs: [], evidence: { targets: [], releases: [], merged: false }, usage: { total: 0, costMicros: 0 }, adrs: [], projectSlug: 'club' } as unknown as ChangeView)
afterEach(() => vi.unstubAllGlobals())
const settle = () => act(async () => { for (let i = 0; i < 8; i++) await new Promise(r => setTimeout(r, 0)) })
function Record({ v }: { v: ChangeView }) {
  const [params] = useSearchParams()
  const path = useLocation().pathname
  const id = path.match(/\/records\/([^/]+)$/)?.[1]
  return <ChangeDetails view={v} current reload={() => {}} recordId={id ? decodeURIComponent(id) : undefined} runId={params.get('run') || undefined} />
}
async function mount(v: ChangeView, answers: Record<string, unknown> = {}, initial = '/changes/change?tab=details') {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(answers[String(url).replace('/api/v1', '')] ?? []))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  document.body.append(el)
  await act(async () => root.render(<MemoryRouter initialEntries={[initial]}><QueryClientProvider client={client}><TechnicalDetailSwitch /><Record v={v} /></QueryClientProvider></MemoryRouter>))
  await settle()
  return { el, client, close: () => { act(() => root.unmount()); el.remove(); client.clear() }, click: async (a: HTMLAnchorElement) => { await act(async () => a.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }))); await settle() } }
}

it('shows only the story and valued facts, with plain evidence links and actions on their entries', async () => {
  const v = view(), f = await mount(v)
  const timeline = f.el.querySelector('ol')!
  for (const text of ['by sam', 'Show the times.', '2 min · 2 files changed', 'Checks', '✗', 'A start time is missing.', 'Add a time to every ride.', 'accept · The text is clear.', 'waiting for priya']) expect(timeline.textContent).toContain(text)
  expect(f.el.querySelector('details, summary, h1')).toBeNull()
  expect(timeline.textContent!.indexOf('waiting for priya')).toBeGreaterThan(timeline.textContent!.indexOf('accept ·'))
  for (const text of ['No release', 'No attachments', 'No merge', 'version 2', 'run 4', 'not configured', 'Try preview']) expect(timeline.textContent).not.toContain(text)
  expect([...timeline.querySelectorAll('.ch-technical')].every(e => e.getAttribute('aria-hidden') === 'true')).toBe(true)
  expect([...f.el.querySelectorAll('.ch-facts-table th')].map(e => e.textContent)).toEqual(['Branch', 'Started from', 'Kind', 'Attachments'])
  expect(f.el.textContent).toContain('make-it v2')
  expect(f.el.textContent).toContain('attach a file →')
  expect(f.el.querySelectorAll('button').item(f.el.querySelectorAll('button').length - 1).textContent).toBe('Attach a record')
  expect(f.el.querySelector('.step-card')).toBeNull()
  const failed = [...timeline.querySelectorAll('tr')].find(e => e.textContent?.includes('A start time is missing.'))!
  const rerun = [...failed.querySelectorAll('button')].find(b => b.textContent === 'Re-run')!
  await act(async () => rerun.click())
  expect(vi.mocked(fetch).mock.calls.some(([url, init]) => String(url).endsWith('/steps/check/run') && init?.method === 'POST')).toBe(true)
  await f.click(failed.querySelector('a')!)
  expect(f.el.querySelector('.step-card')).not.toBeNull()
  expect(f.el.querySelector('h1')?.textContent).toBe('Check')
  expect(f.el.querySelector('button[aria-expanded]')).toBeNull()
  f.close()
})

it('keeps the earlier rounds evidence, log and sender beside that round', () => {
  const d = detail('work', [step('agent', 'agent/pi', 'pass', { evidence: { durationMs: 90000, files: ['new'] } })])
  d.events = [
    { runId: 'work', id: 'start-one', kind: 'step.started', at: '2026-11-01T09:00:00Z', payload: { step: 'agent', attempt: 1, sessionId: 'first-session' } },
    { runId: 'work', id: 'finish-one', kind: 'step.finished', at: '2026-11-01T09:01:00Z', payload: { step: 'agent', attempt: 1, status: 'pass' } },
    { runId: 'work', id: 'retry', kind: 'step.retried', at: '2026-11-01T09:02:00Z', payload: { step: 'agent', previousStatus: 'pass', previousEvidence: { durationMs: 60000, files: ['old', 'other'] } } },
    { runId: 'work', id: 'sendback', kind: 'step.sendback', at: '2026-11-01T09:02:00Z', payload: { step: 'agent', by: 'priya', note: 'Keep the invitation.' } },
    { runId: 'work', id: 'start-two', kind: 'step.started', at: '2026-11-01T10:00:00Z', payload: { step: 'agent', attempt: 2, sessionId: 'second-session' } },
  ] as RunDetail['events']
  d.artifacts = [{ id: 'old-log', name: 'step-agent', kind: 'agent-log', createdAt: '2026-11-01T09:01:00Z' }, { id: 'new-log', name: 'step-agent', kind: 'agent-log', createdAt: '2026-11-01T10:01:00Z' }] as RunDetail['artifacts']
  const entries = runStory(d)
  expect(entries).toHaveLength(2)
  expect(entries[0].text).toBe('Agent worked, 1 min · 2 files changed · Sent back by priya: “Keep the invitation.”')
  expect(entries.map(e => e.session)).toEqual(['first-session', 'second-session'])
  expect(entries.map(e => e.records.map(a => a.id))).toEqual([['old-log'], ['new-log']])
  expect(entries[0].current).toBe(false)
  expect(entries[1].text).toBe('Agent worked, 2 min · 1 file changed')
})

it('reads earlier and called work into the same chronological timeline and puts releases after landing', async () => {
  const v = view()
  v.runs = [{ id: 'old' }, { id: 'work' }] as ChangeView['runs']
  v.detail!.steps.push(step('call', 'workflow/call', 'running', { evidence: { childRunId: 'child' } }))
  v.change.landedSha = 'deadbeef'; v.change.closedAt = '2026-11-01T13:00:00Z'
  v.evidence.releases = [{ number: 7, state: 'released', deployment: 'main' }] as ChangeView['evidence']['releases']
  const old = detail('old'); old.run.createdAt = '2026-11-01T08:10:00Z'
  old.events = [{ runId: 'old', id: 'stopped', kind: 'run.abandoned', at: '2026-11-01T08:30:00Z', payload: { by: 'sam', reason: 'Correct the times first.' } }] as RunDetail['events']
  const child = detail('child', [step('child-check', 'script', 'pass', { startedAt: '2026-11-01T12:00:00Z' })]); child.run.createdAt = '2026-11-01T12:00:00Z'; child.run.parentRunId = 'work'
  const f = await mount(v, { '/runs/old/trace': old, '/runs/child/trace': child, '/projects/club/releases/7': { release: { createdAt: '2026-11-01T14:00:00Z', releasedAt: '2026-11-01T14:01:00Z' } } })
  const lines = [...f.el.querySelectorAll('.ch-story-line')].map(e => e.textContent).join('\n')
  expect(lines).toContain('by sam: Correct the times first.')
  expect(lines.indexOf('by sam')).toBeLessThan(lines.indexOf('2 min'))
  expect(f.el.querySelector('.ch-details')?.textContent).toContain('Child check')
  expect(f.el.querySelector('.ch-details')!.textContent!.indexOf('Child check')).toBeLessThan(f.el.querySelector('.ch-details')!.textContent!.indexOf('merged at deadbeef'))
  expect(lines.indexOf('merged at deadbeef')).toBeLessThan(lines.indexOf('Released #7 to main'))
  expect([...f.el.querySelectorAll('.ch-phase h2')].map(e => e.textContent)).toEqual(['Work', 'Work, round 2', 'Landing'])
  f.close()
})

it('keeps non-applicability, overrides and unknown evidence distinct from a clean pass', () => {
  expect(checkResult(step('scan', 'scan', 'not_applicable', { evidence: { notApplicable: 'No dependencies.' } }))).toBe("doesn't apply: No dependencies.")
  expect(checkResult(step('tests', 'tests', 'pass'))).toContain('Number of tests unknown')
  expect(checkResult(step('scan', 'scan', 'pass', { checkReading: { unreadable: 'Broken report' } }))).toBe('unknown: Broken report')
  expect(checkResult(step('scan', 'scan', 'fail', { overrideBy: 'sam', overrideReason: 'False positive' }))).toBe('overridden by sam: False positive')
  expect(checkResult(step('pending', 'script', 'pending'))).toBe('not run')
})

it('keeps a finished round intact while its replacement is queued', () => {
  const d = detail('work', [step('agent', 'agent/pi', 'pending', { evidence: {}, startedAt: '', finishedAt: '' })])
  d.events = [
    { runId: 'work', id: 'start', kind: 'step.started', at: '2026-11-01T09:00:00Z', payload: { step: 'agent', attempt: 1 } },
    { runId: 'work', id: 'finish', kind: 'step.finished', at: '2026-11-01T09:01:00Z', payload: { step: 'agent', status: 'pass' } },
    { runId: 'work', id: 'reset', kind: 'step.retried', at: '2026-11-01T09:02:00Z', payload: { step: 'agent', previousStatus: 'pass', previousEvidence: { durationMs: 60000, files: ['old'] } } },
  ]
  const entries = runStory(d)
  expect(entries).toHaveLength(1)
  expect(entries[0].text).toBe('Agent worked, 1 min · 1 file changed')
  expect(entries[0].actionStep?.status).toBe('pending')
  expect(entries[0].current).toBe(false)
})

it('names who abandoned a draft even when no run ever started', async () => {
  const v = view(); v.detail = null; v.change.state = 'abandoned'; v.change.abandonedBy = 'priya'; v.stateDetail = 'Duplicate request.'; v.change.closedAt = '2026-11-01T09:00:00Z'
  const f = await mount(v)
  expect(f.el.querySelector('ol')?.textContent).toContain('Abandoned by priya: Duplicate request.')
  expect(f.el.textContent).not.toContain('No run')
  expect(f.el.querySelectorAll('th')).toHaveLength(3)
  f.close()
})

it('omits unrecorded dates from opened approval evidence', async () => {
  const v = view(); const approval = v.detail!.steps.find(s => s.type === 'approval')!; approval.finishedAt = ''
  const f = await mount(v, {}, '/changes/change/records/entry-accept-0')
  const evidence = f.el.querySelector('.ch-story-evidence')!
  expect(evidence.textContent).not.toContain('Started')
  expect(evidence.textContent).not.toContain('Finished')
  f.close()
})

it('dates an acceptance by its recorded decision and shows the merge once', async () => {
  const v = view()
  v.detail!.steps = [step('agent', 'agent/pi', 'pass'), step('accept', 'approval', 'pass', { startedAt: '', finishedAt: '2026-11-01T11:00:00Z', evidence: { at: '2026-11-01T11:00:00Z', decision: 'approve', approver: 'priya', comment: 'Ready.' } }), step('merge', 'scm/merged', 'pass', { startedAt: '', finishedAt: '2026-11-01T12:00:00Z' })]
  v.change.landedSha = 'deadbeef'; v.change.closedAt = '2026-11-01T13:00:00Z'
  const f = await mount(v)
  const entries = [...f.el.querySelectorAll('.ch-story-line')]
  expect(entries.findIndex(e => e.textContent?.includes('accepted by priya: “Ready.”'))).toBeGreaterThan(entries.findIndex(e => e.textContent?.includes('2 min')))
  expect(entries.filter(e => e.textContent?.includes('merged at deadbeef'))).toHaveLength(1)
  expect(f.el.querySelector('ol')?.textContent).not.toContain('Merged ✓')
  f.close()
})

// A record's page is titled in the words its link uses, and its subtitle
// does not repeat a name that only says the kind again.
it('titles a record page with its plain label and drops a name equal to its kind', async () => {
  const v = view()
  v.detail!.artifacts = [{ id: 'inv', runId: 'work', name: 'sbom', kind: 'sbom', body: '{"packages":[]}', createdAt: '2026-11-01T10:01:00Z' }] as unknown as Artifact[]
  const f = await mount(v, {}, '/changes/change/records/record-inv')
  try {
    expect(f.el.querySelector('h1')?.textContent).toBe('Software inventory')
    const sub = f.el.querySelector('.ch-story-evidence > p.small.muted')!.textContent!
    expect(sub.startsWith('sbom · ')).toBe(true)
    expect(sub).not.toContain('sbom · sbom')
    // Its JSON is source, kept whole on each line rather than wrapped mid-token.
    expect(f.el.querySelector('pre.source')?.textContent).toBe('{"packages":[]}')
  } finally { f.close() }
})

// The starting commit is a short hash for everyone; the full hash is for the
// technical switch. The branch is an exact git name and stays whole.
it('shows the starting commit short, and whole with the technical switch', async () => {
  const v = view(), sha = '0123456789abcdef0123456789abcdef01234567'
  v.change.baseSha = sha
  v.change.branch = 'change-9-a-very-long-branch-name-people-copy'
  const f = await mount(v)
  const cell = (name: string) => [...f.el.querySelectorAll('.ch-facts-table tr')].find(r => r.querySelector('th')?.textContent === name)!.querySelector('td')!.textContent
  try {
    expect(cell('Started from')).toBe('01234567')
    expect(cell('Branch')).toBe('change-9-a-very-long-branch-name-people-copy')
    await act(async () => f.el.querySelector<HTMLButtonElement>('button.ch-goggles')!.click())
    expect(cell('Started from')).toBe(sha)
    await act(async () => f.el.querySelector<HTMLButtonElement>('button.ch-goggles')!.click())
  } finally { f.close() }
})

it.each(['preview', 'review', 'checks'])('opens a shareable %s page with its evidence', async anchor => {
  const v = view()
  v.detail!.steps.push(step('boot', 'preview', 'pass', { evidence: { reason: 'Preview recorded.' } }))
  const f = await mount(v, {}, `/changes/change/records/${anchor}`)
  expect(f.el.querySelector('.ch-story-evidence')).not.toBeNull()
  expect(f.el.querySelector('h1')).not.toBeNull()
  f.close()
})

it('groups checks into one aligned table and keeps full non-applicability reasons technical', async () => {
  const v = view()
  v.detail!.steps.push(step('scan', 'scan', 'not_applicable', { evidence: { notApplicable: 'Full scan explanation about the absent package.' } }), step('pending', 'script', 'pending'))
  const f = await mount(v)
  expect(f.el.querySelectorAll('table[aria-label="Checks"]')).toHaveLength(1)
  expect([...f.el.querySelectorAll('.ch-event-label')].filter(e => e.textContent === 'Checks')).toHaveLength(1)
  const rows = [...f.el.querySelectorAll('table[aria-label="Checks"] tr')]
  expect(rows).toHaveLength(3)
  expect(rows.every(r => r.children.length === 4)).toBe(true)
  expect(rows[0].children[2].textContent).toBe('60s')
  expect(rows[1].textContent).toContain("doesn't apply: no matching part")
  expect(rows[1].querySelector('.ch-technical[aria-hidden="true"]')).not.toBeNull()
  expect(rows[2].children[1].textContent).toBe('not run')
  await act(async () => f.el.querySelector<HTMLButtonElement>('button.ch-goggles')!.click())
  expect(rows[1].children[1].textContent).toContain('Full scan explanation')
  await act(async () => f.el.querySelector<HTMLButtonElement>('button.ch-goggles')!.click())
  f.close()
})

it('moves agent records to its own session page', async () => {
  const v = view()
  v.detail!.artifacts = [
    { id: 'one', name: 'Agent notes', kind: 'report', body: 'Recorded notes.', createdAt: '2026-11-01T10:00:00Z' },
    { id: 'two', name: 'Agent instructions', kind: 'steering', body: 'Keep the invitation.', createdAt: '2026-11-01T10:00:00Z' },
  ] as RunDetail['artifacts']
  const f = await mount(v)
  expect(f.el.querySelector('table[aria-label="Records"]')).toBeNull()
  const row = f.el.querySelector('#entry-agent-0')!
  expect(row.querySelectorAll('.ch-story-record')).toHaveLength(0)
  const session = [...row.querySelectorAll('a')].find(a => a.textContent === 'session')!
  expect(session.getAttribute('href')).toBe('/changes/change/work/agent?run=work&attempt=agent-0')
  expect([...row.querySelectorAll('a')].find(a => a.textContent === 'code')?.getAttribute('href')).toBe('/changes/change?tab=changes')
  expect(f.el.textContent).not.toContain('Full work record')
  await f.click(session)
  expect(row.querySelector('.ch-story-evidence')).toBeNull()
  expect(f.el.querySelector('details')).toBeNull()
  f.close()
})

it('uses phase cards, four fixed cells per event and dates only when the day changes', async () => {
  const f = await mount(view())
  expect([...f.el.querySelectorAll('.ch-change-card > h2')].map(e => e.textContent)).toEqual(['Work', 'About this change'])
  const rows = [...f.el.querySelectorAll('.ch-detail-row')]
  expect(rows.length).toBeGreaterThan(3)
  expect(rows.every(row => row.querySelector(':scope > time') && row.querySelector(':scope > .ch-event-label'))).toBe(true)
  expect(f.el.querySelectorAll('.ch-detail-row > time > span')).toHaveLength(1)
  expect(f.el.querySelector('.ch-request-row #request > .ch-story-line > button')?.textContent).toBe('Add ticket link')
  expect(f.el.querySelector('.ch-phase .ch-story-attach')?.textContent).toContain('Attach a record')
  f.close()
})

it('keeps sent-back work and abandoned landing attempts in their own cards', () => {
  const work = detail('work', [step('agent', 'agent/pi', 'pass')])
  work.events = [
    { runId: 'work', id: 'one', kind: 'step.started', at: '2026-11-01T09:00:00Z', payload: { step: 'agent' } },
    { runId: 'work', id: 'back', kind: 'step.sendback', at: '2026-11-01T09:30:00Z', payload: { step: 'agent', by: 'priya', note: 'Keep the invitation.' } },
    { runId: 'work', id: 'two', kind: 'step.started', at: '2026-11-01T10:00:00Z', payload: { step: 'agent' } },
  ] as RunDetail['events']
  const first = detail('first-landing', [step('merge-one', 'scm/merged', 'fail', { startedAt: '2026-11-01T11:00:00Z' })]); first.run.kind = 'landing'; first.run.createdAt = '2026-11-01T11:00:00Z'
  const second = detail('second-landing', [step('merge-two', 'scm/merged', 'pass', { startedAt: '2026-11-01T12:00:00Z' })]); second.run.kind = 'landing'; second.run.createdAt = '2026-11-01T12:00:00Z'
  const phases = storyPhases([...runStory(work), ...runStory(first), ...runStory(second)].sort((a, b) => a.at.localeCompare(b.at)), [{ id: 'first-landing', role: 'landing' }, { id: 'second-landing', role: 'landing' }] as ChangeView['runs'])
  expect(phases.map(p => p.title)).toEqual(['Work', 'Work, round 2 — sent back by priya: Keep the invitation.', 'Landing', 'Landing'])
})

it('links scan reports to the check that recorded their artifact id', () => {
  const d = detail('scan', [step('scan-step', 'scan', 'pass', { evidence: { reportArtifactId: 'report' } })])
  d.artifacts = [{ id: 'report', name: 'secrets', kind: 'scan-report', createdAt: '2026-11-01T10:01:00Z' }] as RunDetail['artifacts']
  const entries = runStory(d)
  expect(entries.find(e => e.step?.id === 'scan-step')?.records.map(a => a.id)).toEqual(['report'])
  expect(entries.find(e => e.key === 'work-scan')?.records).toEqual([])
})

it('places advance acceptance at its recorded time inside the work phase', async () => {
  const v = view(); v.evidence.inAdvance = { approver: 'sam', principal: null, workflow: 'make-it', at: '2026-11-01T09:30:00Z', shown: ['Accepted before checking.'] }
  const f = await mount(v)
  const labels = [...f.el.querySelectorAll('.ch-phase .ch-event-label')].map(e => e.textContent)
  expect(labels.slice(0, 3)).toEqual(['Asked', 'Approval', 'Agent worked'])
  const times = [...f.el.querySelectorAll('.ch-detail-row > time')].map(e => e.getAttribute('datetime')!)
  expect(times).toEqual([...times].sort())
  f.close()
})

it('puts check actions and produced records together on the check row', async () => {
  const v = view()
  v.detail!.steps.push(step('empty', 'script', 'fail'))
  v.detail!.steps.push(step('scan', 'scan', 'pass', { evidence: { reportArtifactId: 'inventory' } }))
  v.detail!.artifacts.push({ id: 'inventory', kind: 'sbom', name: 'inventory', createdAt: '2026-11-01T10:01:00Z' } as RunDetail['artifacts'][number])
  const f = await mount(v)
  const row = f.el.querySelector('#entry-scan-0')!
  expect(row.querySelector('.ch-check-links')?.textContent).toBe('detailsRe-run')
  expect(row.querySelector('.ch-check-result a')?.textContent).toBe('software inventory')
  expect(row.children).toHaveLength(4)
  expect(f.el.textContent).not.toContain('Resolve the reported issue and record a passing result')
  f.close()
})

// A passed check with no notes and no report reads "passed", not a lone tick.
it('says passed on a check row that has nothing else to show', async () => {
  const v = view()
  v.detail!.steps = [step('style', 'script', 'pass', { statusLabel: 'passed' })]
  const f = await mount(v)
  try { expect(f.el.querySelector('#entry-style-0 .ch-check-result')?.textContent).toBe('✓ passed') } finally { f.close() }
})

it('describes run-level work by its output, with session and code links', async () => {
  const v = view(); v.detail!.steps = []; v.diffstat = '2 files changed, 5 insertions(+)'
  let f = await mount(v)
  let row = f.el.querySelector('#entry-work-work')!
  expect(row.querySelector('.ch-story-line')?.textContent).toBe('2 files changed')
  expect([...row.querySelectorAll('.ch-detail-actions a')].map(a => a.textContent)).toContain('session')
  expect([...row.querySelectorAll('.ch-detail-actions a')].map(a => a.textContent)).toContain('code')
  expect(row.textContent).not.toContain('Work started')
  f.close()
  v.diffstat = ''; f = await mount(v); row = f.el.querySelector('#entry-work-work')!
  expect(row.querySelector('.ch-story-line')?.textContent).toBe('no report')
  f.close()
})


it('retains the conflict catch-up command promised by Overview', async () => {
  const v = view()
  v.mainConflict = { mainBranch: 'main', files: ['app.ts'], catchUp: 'git merge origin/main' } as ChangeView['mainConflict']
  const f = await mount(v)
  try { expect(f.el.textContent).toContain('git merge origin/main') } finally { f.close() }
})

it('gives an unreadable test report no green pass mark', () => {
  const s = step('tests', 'tests', 'pass', { statusLabel: 'passed', evidence: { command: 'npm test', testReportUnreadable: 'Malformed JUnit report' } })
  expect(checkResult(s)).toContain('test report unreadable')
  expect(checkResult(s)).not.toContain('✓')
})

it.each([
  { testReportUnreadable: 'Malformed JUnit report' },
  {},
])('marks unknown test evidence amber in the rendered check row: %j', async evidence => {
  const v = view()
  v.detail!.steps = [step('tests', 'tests', 'pass', { statusLabel: 'passed', evidence })]
  const f = await mount(v)
  try {
    const result = f.el.querySelector('.ch-check-result')!
    expect(result.textContent).toContain('unknown')
    expect(result.querySelector('.ch-mark.ok')).toBeNull()
    expect(result.querySelector('.ch-mark.warn')?.textContent).toBe('unknown')
  } finally { f.close() }
})

it('keeps not-applicable check reasons and the merge re-check on the live Details table', async () => {
  const v = view()
  v.detail!.steps = [
    step('guidance', 'guidance/conformance', 'pass', { checkReading: { checks: [{ check: 'go-vet-clean', verdict: 'not-applicable', detail: 'No part is built with Go.' }] } }),
    step('scan', 'scan', 'not_applicable', { evidence: { notApplicable: 'The public part is a static site.' } }),
    step('merged', 'scm/merged', 'needs_human', { evidence: { reason: 'Not merged yet.' } }),
  ]
  const f = await mount(v)
  try {
    expect(f.el.textContent).toContain('No part is built with Go.')
    expect(f.el.textContent).toContain('The public part is a static site.')
    await act(async () => [...f.el.querySelectorAll('button')].find(b => b.textContent === 'Re-check')!.click())
    expect(vi.mocked(fetch).mock.calls.some(([url, init]) => String(url).endsWith('/steps/merged/run') && init?.method === 'POST')).toBe(true)
  } finally { f.close() }
})

it('keeps known counts distinct from unknown evidence when some tests were skipped', () => {
  const s = step('tests', 'tests', 'pass', { evidence: { testCounts: { passed: 8, failed: 0, skipped: 2 } } })
  expect(checkResult(s)).toBe('✓ · 8 passed, 2 skipped')
})

it('keeps the landing merge and its later checks in Landing using the recorded role', async () => {
  const v = view()
  v.detail = detail('land', [step('reconcile', 'reconcile', 'pass'), step('tests', 'tests', 'pass', { startedAt: '2026-11-01T11:00:00Z', evidence: { testCounts: { passed: 8, failed: 0, skipped: 0 } } })])
  v.detail.run.kind = 'pipeline'
  v.runs = [{ id: 'land', role: 'landing' }] as ChangeView['runs']
  v.detail.events = [{ id: 'merge', kind: 'main.merged', at: '2026-11-01T10:02:00Z', payload: {} }] as RunDetail['events']
  const f = await mount(v)
  try {
    expect([...f.el.querySelectorAll('.ch-phase h2')].map(e => e.textContent)).toEqual(['Work', 'Landing'])
    const landing = [...f.el.querySelectorAll('.ch-phase')].find(e => e.querySelector('h2')?.textContent === 'Landing')!
    expect(landing.querySelector('#entry-tests-0')).not.toBeNull()
    expect(landing.querySelector('#entry-event-merge .ch-event-label')?.textContent).toBe('Brought up to date with main')
    expect([...landing.querySelectorAll('.ch-event-label')].filter(e => e.textContent === 'Brought up to date with main')).toHaveLength(1)
  } finally { f.close() }
})

it('shows a short request quote once and links to the complete request without a disclosure', async () => {
  const v = view()
  v.change.body = Array.from({ length: 40 }, (_, i) => `Request line ${i + 1}.`).join('\n')
  const f = await mount(v)
  try {
    const asked = f.el.querySelector('.ch-request-row')!
    expect(asked.textContent?.match(/Request line 1\./g)).toHaveLength(1)
    expect(asked.textContent).not.toContain('Request line 4.')
    expect(asked.textContent).not.toContain('Request line 5.')
    const link = [...asked.querySelectorAll('a')].find(a => a.textContent === 'full request →')!
    expect(link.getAttribute('href')).toBe('/changes/change/records/request')
    await f.click(link)
    expect(f.el.querySelector('#request-text')?.textContent).toBe(v.change.body)
    expect(f.el.querySelector('details')).toBeNull()
  } finally { f.close() }
})

it('marks a rejected review red before its positive opening and preserves the later objection', async () => {
  const v = view()
  v.detail!.steps = [step('review', 'agent/review', 'fail', { evidence: { verdict: 'fail', summary: 'The preview was styled.\n\nThe requested adoption is incomplete.' } })]
  const f = await mount(v)
  try {
    const row = f.el.querySelector('#entry-review-0')!
    expect(row.querySelector('.ch-mark.bad')?.textContent).toBe('✗')
    expect(row.querySelector('.ch-story-line')?.textContent).toBe('✗ Rejected · The preview was styled.\n\nThe requested adoption is incomplete.')
  } finally { f.close() }
})

it.each([[0, 2, 'not priced'], [3000, 1, 'partly priced'], [0, 0, '$0.00']] as const)('shows the recorded price state in Details (%s, %s)', async (costMicros, unpriced, expected) => {
  const v = view()
  v.usage = { total: unpriced ? 0 : 100, costMicros, unpriced }
  const f = await mount(v)
  try {
    const row = [...f.el.querySelectorAll('.ch-facts-table tr')].find(r => r.querySelector('th')?.textContent === 'Cost')
    expect(row?.textContent).toContain(expected)
  } finally { f.close() }
})

it('retains binary records and their provenance on the record page', async () => {
  const v = view()
  v.detail!.artifacts = [{id:'shot',runId:'work',name:'step-check',kind:'screenshot',body:'',binary:true,ref:'https://example.test/record',createdAt:'2026-11-01T10:01:00Z'}]
  const f = await mount(v, {'/artifacts/shot':{bodyBase64:'cGhvdG8='}}, '/changes/change/records/record-shot')
  try {
    await vi.waitFor(() => expect(f.el.querySelector('img[src="data:image/png;base64,cGhvdG8="]')).not.toBeNull())
    expect(f.el.querySelector('h1')?.textContent).toBe('Screenshots')
    expect(f.el.textContent).toContain('step-check · screenshot')
    expect(f.el.querySelector('a[href="https://example.test/record"]')?.textContent).toBe('Open record')
  } finally { f.close() }
})


it.each([['preview', 'No run recorded a preview.'], ['review', 'No review was recorded.'], ['checks', 'No checks were recorded.']])('names an empty %s record without inventing evidence', async (name, words) => {
  const v = view()
  v.detail!.steps = [step('agent', 'agent/pi', 'pass', {evidence:{output:'This is agent work, not a preview.'}})]
  const f = await mount(v, {}, `/changes/change/records/${name}`)
  try {
    expect(f.el.querySelector('h1')?.textContent).toBe(name.charAt(0).toUpperCase()+name.slice(1))
    expect(f.el.textContent).toContain(words)
    expect(f.el.textContent).not.toContain('This is agent work')
  } finally {f.close()}
})

// A preview someone started with Try it is live without a run recording
// it: the record says no run recorded one, beside the live preview's own
// control, as the Overview offers it, never only "none".
it('offers the live preview beside a preview record no run made', async () => {
  const v = view()
  v.detail!.steps = [step('agent', 'agent/pi', 'pass')]
  const f = await mount(v, {'/changes/change/preview': {state: 'live', url: 'https://preview.example.test', built: '2026-11-01T10:00:00Z', commit: 'abc1234def'}}, '/changes/change/records/preview')
  try {
    expect(f.el.textContent).toContain('No run recorded a preview.')
    expect([...f.el.querySelectorAll('button')].map(b => b.textContent)).toContain('Open it and try it')
    expect(f.el.textContent).toContain('built from commit abc1234')
  } finally {f.close()}
})

// The record of one run says absence of that run only: an earlier run's
// preview is not denied.
it('says only the run it read recorded no preview', async () => {
  const v = view()
  v.detail!.steps = [step('agent', 'agent/pi', 'pass')]
  const old = detail('older', [step('boot', 'preview', 'pass', {runId: 'older'})])
  v.runs = [{id: 'older'}] as ChangeView['runs']
  const f = await mount(v, {'/runs/older/trace': old}, `/changes/change/records/preview?run=${v.detail!.run.id}`)
  try {
    expect(f.el.textContent).toContain('This run recorded no preview.')
    expect(f.el.textContent).not.toContain('No run recorded a preview.')
  } finally {f.close()}
})

it('reads the review from the run named by the link', async () => {
  const v = view()
  v.detail!.steps = [step('new-review', 'agent/review', 'pass', {evidence:{verdict:'approve',summary:'Newer review conclusion.'}})]
  const old = detail('older', [step('old-review', 'agent/review', 'fail', {runId:'older',evidence:{verdict:'reject',summary:'Earlier review conclusion.'}})])
  v.runs = [{id:'older'}] as ChangeView['runs']
  const f = await mount(v, {'/runs/older/trace':old}, '/changes/change/records/review?run=older')
  try {
    expect(f.el.textContent).toContain('Earlier review conclusion.')
    expect(f.el.textContent).not.toContain('Newer review conclusion.')
  } finally {f.close()}
})


it('does not claim that a review is absent before its run has been read', async () => {
  const v = view()
  v.runs = [{id:'unread'}] as ChangeView['runs']
  const f = await mount(v, {}, '/changes/change/records/review?run=unread')
  try {
    expect(f.el.textContent).toContain('This record has not been read.')
    expect(f.el.textContent).not.toContain('No review was recorded.')
  } finally {f.close()}
})


it('shows a landing result recording failure with its remedy in the change evidence', async () => {
  const v = view()
  const reason = 'The change landed, but its check results could not be saved. Run a conformance review for this project.'
  v.detail!.events = [{ runId: 'work', id: 'recording', kind: 'conformance.recording-failed', at: '2026-11-01T10:02:00Z', payload: { reason } }] as RunDetail['events']
  const f = await mount(v)
  try {
    const row = f.el.querySelector('#entry-event-recording')!
    expect(row.querySelector('.ch-event-label')?.textContent).toBe('Check results')
    expect(row.querySelector('.ch-story-line')?.textContent).toBe(reason)
    expect([...f.el.querySelectorAll('.ch-event-label')].filter(e => e.textContent === 'Check results')).toHaveLength(1)
  } finally { f.close() }
})

it('ticks elapsed time for a running check in Details', async () => {
 vi.useFakeTimers()
 vi.setSystemTime(new Date('2026-11-01T10:04:12Z'))
 vi.stubGlobal('fetch', vi.fn(async () => new Response('[]')))
 const v = view(); v.detail!.steps = [step('scan', 'scan', 'running', {finishedAt:''})]
 const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
 try {
  await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><ChangeDetails view={v} current reload={() => {}} /></QueryClientProvider></MemoryRouter>))
  expect(el.querySelector('.ch-check-duration')?.textContent).toBe('4m 12s')
  await act(async () => vi.advanceTimersByTime(1000))
  expect(el.querySelector('.ch-check-duration')?.textContent).toBe('4m 13s')
 } finally { act(() => root.unmount()); client.clear(); vi.useRealTimers() }
})
