// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import type { ChangeView, Step } from '../../api'
import { getKey, newQueryClient } from '../../data'
import { fmtStamp } from '../../format'
import { RunProgress, segmentState, stageState } from './RunProgress'
import { ContinueDecision, TurnDecision } from './Turn'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const at = '2026-11-04T01:00:00Z'
const step = (name: string, type: Step['type'], status: Step['status']): Step => ({ observation: false, isCheck: ['scan', 'tests', 'guidance/conformance'].includes(type), progress: status === 'pass' ? 'done' : status === 'pending' ? 'waiting' : status === 'fail' ? 'failed' : status, id: name, runId: 'landing', stage: 'land', name, type, status, statusLabel: status === 'pass' ? 'passed' : status, config: {}, evidence: {}, startedAt: status === 'pending' ? '' : at, finishedAt: status === 'pass' ? '2026-11-04T01:00:10Z' : '', overrideBy: '', overrideReason: '' } as Step)
const view = (): ChangeView => ({ projectSlug: 'notes', change: { id: 'change', state: 'landing' }, waitingOn: { kind: 'work', run: 'landing', step: 'build', says: 'Landing: deploying — step 3 of 4' }, runs: [{ id: 'landing', role: 'landing' }], detail: { run: { id: 'landing', currentStage: 'land', status: 'running', createdAt: at }, steps: [step('accept', 'approval', 'pass'), step('merged', 'scm/merged', 'pass'), step('build', 'build', 'running'), step('record', 'artifact/require', 'pending')], stages: ['land'] } } as ChangeView)
vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
function html(v: ChangeView, client = newQueryClient()) {
 const el = document.createElement('div'); document.body.append(el); const root = createRoot(el)
 try {
  act(() => root.render(<MemoryRouter><QueryClientProvider client={client}><RunProgress view={v} /></QueryClientProvider></MemoryRouter>))
  act(() => el.querySelector<HTMLButtonElement>('.ch-progress-trigger')?.click())
  return document.body.innerHTML
 } finally { act(() => root.unmount()); el.remove() }
}

it('shows landing progress, ordered plain titles, approval fact and timings during deploy', () => {
 const v = view(); v.detail!.steps[0] = { ...v.detail!.steps[0], evidence: { approver: 'bls' } } as Step
 const text = html(v)
 expect(text).toContain('Landing — step 3 of 4')
 expect(text).toContain('aria-label="Progress details"')
 expect(text).not.toContain('deploy-scene')
 expect(text).toContain('bls')
 expect(text).toContain('10s')
 expect(text).toContain('Started')
 expect(text).toContain('aria-label="running"')
 const el = document.createElement('div'); el.innerHTML = text
 expect([...el.querySelectorAll('.ch-now-steps li strong')].map(s => s.textContent)).toEqual(['Approval', 'Merged', 'Build', 'Required file recorded'])
 expect(el.querySelector('details')).toBeNull()
})

it('groups consecutive checks and expands the whole group on failure or running', () => {
 const v = view()
 v.detail!.steps = [step('secret scan', 'scan', 'pass'), step('dependencies', 'scan', 'pass'), step('unit tests', 'tests', 'pass'), step('conformance', 'guidance/conformance', 'pending'), step('build', 'build', 'running')]
 const draw = () => { const el = document.createElement('div'); el.innerHTML = html(v); return el }
 expect(draw().querySelectorAll('.ch-now-steps li')).toHaveLength(2)
 expect(draw().querySelector('li')!.textContent).toContain('Security scan, Security scan, Tests, Standards check')
 for (const status of ['fail', 'running'] as const) {
  v.detail!.steps[1].status = status; v.detail!.steps[1].progress = status === 'fail' ? 'failed' : status
  expect(draw().querySelectorAll('.ch-now-steps li')).toHaveLength(5)
  expect(draw().querySelectorAll('.ch-now-steps li')[1].textContent).toContain(status === 'fail' ? '✗' : 'Security scan')
 }
})

it('names work, keeps later stages out, and distinguishes pending deployment from completed work', () => {
 const v = view(); v.runs = []; v.change.state = 'in_progress'
 v.detail!.steps = [step('agent', 'agent/pi', 'pass'), step('build', 'build', 'pending'), { ...step('future', 'tests', 'pending'), stage: 'later' }]
 const text = html(v)
 expect(text).toContain('Work — step 2 of 2')
 expect(text).toContain('Helper finished')
 expect(text).toContain('Waiting to build')
 expect(text).not.toContain('waiting for a free slot')
 expect(text).not.toContain('future')
 v.detail!.steps[1].status = 'pass'
 expect(html(v)).toContain('Accepted work built')
})

it('keeps bars for decisions and finished runs, and hides them without a run', () => {
 const v = view()
 for (const kind of ['approval', 'check', 'ready', 'chat', 'closed']) { v.waitingOn.kind = kind; expect(html(v)).toContain('ch-progress-trigger') }
 v.detail!.run.status = 'done'; expect(html(v)).toContain('Progress details')
 v.detail = null; expect(html(v)).not.toContain('ch-progress-trigger')
})

it('polls called work and deploy facts inside the same block', async () => {
 const v = view(), child = { run: { id: 'child', currentStage: 'checks', status: 'running', workflowName: 'Verification' }, steps: [{ ...step('test', 'tests', 'running'), stage: 'checks', runId: 'child' }], events: [], stages: ['checks'], artifacts: [] }
 v.waitingOn = { ...v.waitingOn, run: 'child', step: 'test' }
 v.detail!.steps = [step('call', 'workflow/call', 'running')]
 v.detail!.steps[0].evidence = { childRunId: 'child' }
 vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(child))))
 const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
 document.body.append(el)
 try {
  await act(async () => root.render(<QueryClientProvider client={client}><RunProgress view={v} /></QueryClientProvider>))
  act(() => el.querySelector<HTMLButtonElement>('.ch-progress-trigger')!.click())
  await vi.waitFor(async () => { await act(async () => {}); expect(document.body.textContent).toContain('Verification') })
  expect(document.body.textContent).toContain('Tests')
  expect(document.body.querySelector('h3')?.textContent).toBe('Landing — step 1 of 1')
  expect(document.body.querySelectorAll('[aria-label="Progress details"]')).toHaveLength(1)
  v.detail!.steps = [step('build', 'build', 'running')]
  vi.mocked(fetch).mockImplementation(async () => new Response(JSON.stringify({ running: true, lines: ['building the image for main'], total: 1 })))
  await act(async () => root.render(<QueryClientProvider client={client}><RunProgress view={{ ...v }} /></QueryClientProvider>))
  await vi.waitFor(async () => { await act(async () => {}); expect(document.body.textContent).toContain('building the image for main') })
  v.detail!.steps = [step('standards', 'guidance/conformance', 'running')]
  vi.mocked(fetch).mockImplementation(async () => new Response(JSON.stringify({ running: true, lines: ['4 of 14 · lockfile-in-step (django) · checking'], total: 7 })))
  await act(async () => root.render(<QueryClientProvider client={client}><RunProgress view={{ ...v }} /></QueryClientProvider>))
  await vi.waitFor(async () => { await act(async () => {}); expect(document.body.textContent).toContain('4 of 14'); expect(document.body.querySelector('progress')?.value).toBe(3) })
  vi.mocked(fetch).mockImplementation(async () => new Response(JSON.stringify({ running: true, lines: ['14 of 14 · readme (django) · reused · 0.0s'], total: 28 })))
  await act(async () => { await client.invalidateQueries({ queryKey: getKey('/steps/standards/live?from=0') }) })
  await vi.waitFor(async () => { await act(async () => {}); expect(document.body.textContent).toContain('14 of 14'); expect(document.body.querySelector('progress')?.value).toBe(14) })

 } finally { await act(async () => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

it('continues inline without opening a dialog', () => {
 const run = vi.fn(), el = document.createElement('div'), root = createRoot(el)
 try {
  act(() => root.render(<TurnDecision active={false}><ContinueDecision busy={false} run={run} /></TurnDecision>))
  act(() => el.querySelector('button')!.click())
  expect(run).toHaveBeenCalledOnce()
  expect(el.querySelector('dialog')).toBeNull()
 } finally { act(() => root.unmount()) }
})

// Seed the completed response so rendering must read it, independent of fetch timing.
it.each([
 { running: true, total: 0 },
 { running: true, lines: null, total: 0 },
 { running: true, lines: [], total: 0 },
])('keeps deployment visible when progress has no lines: %j', progress => {
 const client = newQueryClient()
 client.setQueryData(getKey('/steps/build/live?from=0'), progress)
 try {
  const text = html(view(), client)
  expect(text).toContain('Building the accepted work')
  expect(text).toContain('Landing — step 3 of 4')
 } finally { client.clear() }
})

it.each([
 ['4 of 14 · lockfile-in-step (lang/selection/django-fullstack) · checking', 3, '14'],
 ['14 of 14 · lockfile-in-step (lang/selection/django-fullstack) · reused · 0.0s', 14, '14'],
 ['check 4 · lockfile-in-step (lang/selection/django-fullstack) · checking', null, null],
])('shows Standards check position and completed progress from the feed: %s', (line, value, total) => {
 const v = view(), client = newQueryClient()
 v.detail!.steps = [step('standards', 'guidance/conformance', 'running')]
 client.setQueryData(getKey('/steps/standards/live?from=0'), { running: true, lines: [line], total: 8 })
 try {
  const el = document.createElement('div')
  el.innerHTML = html(v, client)
  expect(el.textContent).toContain('Standards check')
  expect(el.textContent).toContain(line)
  const bar = el.querySelector('progress')!
  expect(bar).not.toBeNull()
  expect(bar.getAttribute('value')).toBe(value === null ? null : String(value))
  expect(bar.getAttribute('max')).toBe(total)
 } finally { client.clear() }
})

// Applicability and missing evidence must never look like a passing execution.
it.each(['not_applicable', 'skipped'] as const)('shows %s checks without a passed mark or zero duration', status => {
 const v = view()
 v.detail!.steps = [{ ...step('scan-source:sbom', 'scan', status), evidence: { notApplicable: 'No declared dependencies.' }, finishedAt: at } as Step]
 const el = document.createElement('div'); el.innerHTML = html(v)
 expect(el.textContent).toContain('Software inventory')
 expect(el.textContent).toContain(status === 'skipped' ? 'Skipped:' : 'Not applicable:')
 expect(el.querySelector('.ok')).toBeNull()
 expect(el.textContent).not.toContain('0s')
 expect(el.textContent).not.toContain('source:sbom')
})
it('does not turn an unreported test count into a passed tick', () => {
 const v = view(); v.detail!.steps = [{ ...step('tests', 'tests', 'pass'), progress: 'unknown' }]
 expect(html(v)).toContain('aria-label="unknown"')
 expect(html(v)).not.toContain('aria-label="passed"')
})
it('uses a neutral mark for pending work', () => {
 const el = document.createElement('div'); el.innerHTML = html(view())
 expect(el.querySelector('[aria-label="pending"]')?.textContent).toBe('○')
})

it('uses the owner’s failed verdict when a standards gate allowed existing failures', () => {
 const v = view(); v.detail!.steps = [{ ...step('guidance-conformance', 'guidance/conformance', 'pass'), statusLabel: 'failed', progress: 'failed' }]
 const el = document.createElement('div'); el.innerHTML = html(v)
 expect(el.querySelector('[aria-label="failed"]')?.textContent).toBe('✗')
 expect(el.querySelector('.ok')).toBeNull()
})

it('uses stage step start times and ticks running durations', async () => {
 vi.useFakeTimers()
 vi.setSystemTime(new Date('2026-11-04T01:04:12Z'))
 const v = view(), s = step('scan', 'scan', 'running')
 v.detail!.run.createdAt = '2026-11-03T00:00:00Z'
 v.detail!.steps = [s]
 const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
 document.body.append(el)
 try {
  await act(async () => root.render(<QueryClientProvider client={client}><RunProgress view={v} /></QueryClientProvider>))
  act(() => el.querySelector<HTMLButtonElement>('.ch-progress-trigger')!.click())
  expect(document.body.querySelector('.ch-now > p')?.textContent).toBe(`Started ${fmtStamp(at)}`)
  expect(document.body.querySelector('.ch-now > p')?.textContent).toContain('Started')
  expect(document.body.textContent).toContain('4m 12s')
  await act(async () => vi.advanceTimersByTime(1000))
  expect(document.body.textContent).toContain('4m 13s')
  s.status = 'pending'; s.startedAt = ''
  s.attempt = 2
  v.waitingOn = {...v.waitingOn, step:s.id, attempt:2, queue:{binding:'checks',ahead:0,says:'waiting for a slot on checks, next in line'}, says:'building · waiting for a free slot'}
  await act(async () => root.render(<QueryClientProvider client={client}><RunProgress view={{...v}} /></QueryClientProvider>))
  expect(document.body.querySelector('.ch-now > p')).toBeNull()
  expect(document.body.textContent).toContain('waiting for a slot on checks, next in line')
 } finally { act(() => root.unmount()); client.clear(); vi.useRealTimers() }
})

it('keeps settled failures above the current step and aggregates stage states', () => {
 const v = view(), waiting = v.waitingOn
 for (const type of ['build', 'scan'] as const) {
  const s = { ...step('build', type, 'fail'), progress: 'failed' }
  expect(segmentState(s, waiting)).toBe('failed')
  expect(stageState([s, step('next', 'build', 'pending')], waiting)).toBe('failed')
 }
 expect(segmentState({ ...step('build', 'build', 'error'), progress: 'unknown' }, waiting)).toBe('unknown')
 expect(segmentState(step('build', 'build', 'pending'), waiting)).toBe('current')
 expect(stageState([step('na', 'scan', 'not_applicable')], waiting)).toBe('not_applicable')
 expect(stageState([step('skip', 'scan', 'skipped')], waiting)).toBe('skipped')
 expect(stageState([], waiting)).toBe('waiting')
})

it('opens by hover and keyboard focus, and closes with Escape and outside tap', async () => {
 vi.useFakeTimers()
 const el = document.createElement('div'); document.body.append(el)
 const root = createRoot(el), client = newQueryClient()
 try {
  act(() => root.render(<QueryClientProvider client={client}><RunProgress view={view()} /></QueryClientProvider>))
  const trigger = el.querySelector<HTMLButtonElement>('.ch-progress-trigger')!
  const hover = new Event('pointerover', { bubbles: true }); Object.defineProperty(hover, 'pointerType', { value: 'mouse' })
  act(() => trigger.dispatchEvent(hover))
  act(() => vi.advanceTimersByTime(1000))
  expect(document.querySelector('.popover')).not.toBeNull()
  expect(document.activeElement).not.toBe(document.querySelector('.popover'))
  act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
  expect(document.querySelector('.popover')).toBeNull()
  act(() => vi.runAllTicks())
  act(() => trigger.focus())
  act(() => trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })))
  expect(trigger.getAttribute('aria-expanded')).toBe('true')
  expect(document.querySelector('.popover')?.contains(document.activeElement)).toBe(true)
  act(() => document.body.dispatchEvent(new Event('pointerdown', { bubbles: true })))
  expect(document.querySelector('.popover')).toBeNull()
 } finally { act(() => root.unmount()); el.remove(); client.clear(); vi.useRealTimers() }
})

it('keeps called-stage bars live while closed and retains them after a failed refresh', async () => {
 const v = view(), client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
 document.body.append(el)
 client.setDefaultOptions({ queries: { ...client.getDefaultOptions().queries, retry: false } })
 v.waitingOn = { ...v.waitingOn, run: 'child', step: 'test' }
 v.detail!.steps = [{ ...step('call', 'workflow/call', 'running'), evidence: { childRunId: 'child' } } as Step]
 const child = { run: { id: 'child', currentStage: 'child-checks', status: 'running' }, stages: ['child-checks'], steps: [{ ...step('test', 'tests', 'running'), runId: 'child', stage: 'child-checks' }], events: [] }
 vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(child))))
 try {
  await act(async () => root.render(<QueryClientProvider client={client}><RunProgress view={v} /></QueryClientProvider>))
  await vi.waitFor(async () => { await act(async () => {}); expect(el.querySelector('button')?.getAttribute('aria-label')).toBe('Progress: child checks step 1 of 1') })
  expect(document.querySelector('.popover')).toBeNull()
  expect(el.querySelector('[aria-label="Steps"] .current')).not.toBeNull()
  vi.mocked(fetch).mockRejectedValue(new Error('offline'))
  await act(async () => { await client.invalidateQueries({ queryKey: getKey('/runs/child/trace') }) })
  act(() => el.querySelector<HTMLButtonElement>('button')!.click())
  await vi.waitFor(async () => { await act(async () => {}); expect(document.querySelector('.popover .stale-note')).not.toBeNull() })
  expect(el.querySelector('button')?.getAttribute('aria-label')).toBe('Progress: child checks step 1 of 1')
  expect(el.querySelector('[aria-label="Steps"] .current')).not.toBeNull()
 } finally { act(() => root.unmount()); el.remove(); client.clear(); vi.unstubAllGlobals() }
})

it('shows a finished non-gating scan with findings and a Security link', () => {
 const v = view()
 v.detail!.run.status = 'done'
 v.detail!.steps = [{...step('scan-bundle', 'scan', 'fail'), observation: true, progress: 'done', evidence: {findings: {total: 3, actionable: 3}}} as Step]
 const el = document.createElement('div'); el.innerHTML = html(v)
 expect(el.textContent).toContain('finished · 3 findings')
 expect(el.querySelector('.ch-now .bad')).toBeNull()
 expect(el.querySelector('.ch-now a')?.getAttribute('href')).toBe('/projects/notes/security')
})


it('names the ordered stages, emphasises the current stage and colours failed stages', () => {
 const v = view()
 v.detail!.stages = ['gates', 'approval', 'ship-image']
 v.detail!.run.currentStage = 'approval'
 v.detail!.steps = [
  { ...step('checks', 'scan', 'fail'), stage: 'gates' },
  { ...step('accept', 'approval', 'pending'), stage: 'approval' },
  { ...step('build', 'build', 'pending'), stage: 'ship-image' },
 ] as Step[]
 const el = document.createElement('div'); el.innerHTML = html(v)
 const stages = el.querySelector('.ch-now-stages')!
 expect(stages).not.toBeNull()
 expect(stages.textContent).toBe('Gates · Approval · Ship image')
 expect(stages.querySelector('strong')?.textContent).toBe('Approval')
 expect(stages.querySelector('.bad')?.textContent).toBe('Gates')
 v.detail!.run.currentStage = 'gates'
 el.innerHTML = html(v)
 expect(el.querySelector('.ch-now-stages strong.bad')?.textContent).toBe('Gates')
})
