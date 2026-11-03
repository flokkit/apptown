// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import type { ChangeView, RunDetail, Step } from '../../api'
import { newQueryClient } from '../../data'
import { ChangeDetails, storyPhases } from './Details'
import { TechnicalDetailSwitch } from './TechnicalDetail'
import { checkResult, runStory } from './story'

Element.prototype.scrollIntoView = vi.fn()
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const step = (id: string, type: Step['type'], status: Step['status'], extra = {}) => ({ id, name: id, type, status, runId: 'work', config: {}, evidence: {}, overrideBy: '', stage: 'check', startedAt: '2026-11-01T10:00:00Z', finishedAt: '2026-11-01T10:01:00Z', ...extra } as Step)
const detail = (id: string, steps: Step[] = []): RunDetail => ({ run: { id, status: 'running', currentStage: 'check', workflowName: 'make-it', workflowVersion: 2, createdAt: '2026-11-01T09:00:00Z' }, steps, events: [], artifacts: [], next: [], landingMeans: [] } as unknown as RunDetail)
const view = (): ChangeView => ({ change: { id: 'change', title: 'A better calendar', body: 'Show the times.\nKeep the invitation.', openedBy: 'sam', createdAt: '2026-11-01T08:00:00Z', state: 'in_progress', type: 'feature', editRevision: 1, branch: 'calendar', baseSha: 'abc' }, detail: detail('work', [step('agent', 'agent/pi', 'pass', { evidence: { durationMs: 120000, files: ['one', 'two'] } }), step('check', 'script', 'fail', { evidence: { reason: 'A start time is missing.', actions: ['Add a time to every ride.'] } }), step('review', 'agent/review', 'pass', { evidence: { verdict: 'accept', summary: 'The text is clear.\nA longer conclusion.' } }), step('accept', 'approval', 'pending', { startedAt: '', config: { approvers: ['priya'] } })]), runs: [], evidence: { targets: [], releases: [], merged: false }, usage: { total: 0, costMicros: 0 }, adrs: [], projectSlug: 'club' } as unknown as ChangeView)
afterEach(() => vi.unstubAllGlobals())
const settle = () => act(async () => { for (let i = 0; i < 8; i++) await new Promise(r => setTimeout(r, 0)) })
async function mount(v: ChangeView, answers: Record<string, unknown> = {}, initial = '/changes/change?tab=details') {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(answers[String(url).replace('/api/v1', '')] ?? []))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  document.body.append(el)
  await act(async () => root.render(<MemoryRouter initialEntries={[initial]}><QueryClientProvider client={client}><TechnicalDetailSwitch /><ChangeDetails view={v} current reload={() => {}} /></QueryClientProvider></MemoryRouter>))
  await settle()
  return { el, client, close: () => { act(() => root.unmount()); el.remove(); client.clear() }, click: async (a: HTMLAnchorElement) => { await act(async () => a.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }))); await settle() } }
}

it('shows only the story and valued facts, with plain evidence links and actions on their entries', async () => {
  const v = view(), f = await mount(v)
  const timeline = f.el.querySelector('ol')!
  for (const text of ['by sam', 'Keep the invitation.', '2 min · 2 files changed', 'Checks', '✗', 'A start time is missing.', 'Add a time to every ride.', 'accept · The text is clear.', 'waiting for priya']) expect(timeline.textContent).toContain(text)
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
  const opened = failed.nextElementSibling!
  expect(opened.classList.contains('ch-check-evidence-row')).toBe(true)
  expect(opened.querySelector('.step-card')).not.toBeNull()
  const buttons = [...failed.querySelectorAll('button'), ...opened.querySelectorAll('button')]
  expect(buttons.filter(b => b.textContent === 'hide')).toHaveLength(1) // The opened link becomes "hide" in place.
  expect(buttons.filter(b => b.textContent !== 'hide')).toHaveLength(2) // No duplicate actions inside the evidence.
  await act(async () => buttons.find(b => b.textContent === 'hide')!.click())
  expect(failed.nextElementSibling?.classList.contains('ch-check-evidence-row') ?? false).toBe(false)
  expect(failed.querySelector('a')?.textContent).toBe('details')
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
  const f = await mount(v, {}, '/changes/change?tab=details#entry-accept-0')
  const evidence = f.el.querySelector('#entry-accept-0 .ch-story-evidence')!
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

it.each(['preview', 'review', 'checks'])('opens a shareable %s anchor at its timeline evidence', async anchor => {
  const v = view()
  v.detail!.steps.push(step('boot', 'preview', 'pass', { evidence: { reason: 'Preview recorded.' } }))
  const f = await mount(v, {}, `/changes/change?tab=details#${anchor}`)
  expect(f.el.querySelector(`#${anchor}`)?.closest('li')?.querySelector('.ch-story-evidence')).not.toBeNull()
  expect(Element.prototype.scrollIntoView).toHaveBeenCalled()
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
  const phases = storyPhases([...runStory(work), ...runStory(first), ...runStory(second)].sort((a, b) => a.at.localeCompare(b.at)))
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
  v.mainConflict = { mainBranch: 'main', step: 'implement', files: ['app.ts'], catchUp: 'git merge origin/main' } as ChangeView['mainConflict']
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
