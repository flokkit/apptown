// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import type { ChangeView, Step } from '../../api'
import { getKey, newQueryClient } from '../../data'
import { fmtStamp } from '../../format'
import { Now } from './Now'
import { ContinueDecision, TurnDecision } from './Turn'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const at = '2026-11-04T01:00:00Z'
const step = (name: string, type: Step['type'], status: Step['status']): Step => ({ id: name, runId: 'landing', stage: 'land', name, type, status, statusLabel: status === 'pass' ? 'passed' : status, config: {}, evidence: {}, startedAt: status === 'pending' ? '' : at, finishedAt: status === 'pass' ? '2026-11-04T01:00:10Z' : '', overrideBy: '', overrideReason: '' } as Step)
const view = (): ChangeView => ({ change: { id: 'change', state: 'landing' }, waitingOn: { kind: 'work', run: 'landing', step: 'build', says: 'Landing: deploying — step 3 of 4' }, runs: [{ id: 'landing', role: 'landing' }], detail: { run: { id: 'landing', currentStage: 'land', status: 'running', createdAt: at }, steps: [step('accept', 'approval', 'pass'), step('merged', 'scm/merged', 'pass'), step('build', 'build', 'running'), step('record', 'artifact/require', 'pending')], stages: ['land'] } } as ChangeView)
function html(v: ChangeView) { return renderToStaticMarkup(<QueryClientProvider client={newQueryClient()}><Now view={v} /></QueryClientProvider>) }

it('shows landing progress, ordered plain titles, approval fact and timings during deploy', () => {
 const v = view(); v.detail!.steps[0] = { ...v.detail!.steps[0], evidence: { approver: 'bls' } } as Step
 const text = html(v)
 expect(text).toContain('Landing — step 3 of 4')
 expect(text).toContain('aria-label="Now"')
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
  v.detail!.steps[1].status = status
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

it('is absent when idle, awaiting a decision, or finished', () => {
 const v = view()
 for (const kind of ['approval', 'check', 'ready', 'chat', 'closed']) { v.waitingOn.kind = kind; expect(html(v)).toBe('') }
 v.waitingOn.kind = 'work'; v.detail!.run.status = 'done'; expect(html(v)).toBe('')
})

it('polls called work and deploy facts inside the same block', async () => {
 const v = view(), child = { run: { id: 'child', currentStage: 'checks', status: 'running', workflowName: 'Verification' }, steps: [{ ...step('test', 'tests', 'running'), stage: 'checks', runId: 'child' }], events: [], stages: ['checks'], artifacts: [] }
 v.waitingOn = { ...v.waitingOn, run: 'child', step: 'test' }
 v.detail!.steps = [step('call', 'workflow/call', 'running')]
 v.detail!.steps[0].evidence = { childRunId: 'child' }
 vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(child))))
 const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
 try {
  await act(async () => root.render(<QueryClientProvider client={client}><Now view={v} /></QueryClientProvider>))
  await vi.waitFor(async () => { await act(async () => {}); expect(el.textContent).toContain('Verification') })
  expect(el.textContent).toContain('Tests')
  expect(el.querySelector('h3')?.textContent).toBe('Landing — step 1 of 1')
  expect(el.querySelectorAll('[aria-label="Now"]')).toHaveLength(1)
  v.detail!.steps = [step('build', 'build', 'running')]
  vi.mocked(fetch).mockImplementation(async () => new Response(JSON.stringify({ running: true, lines: ['building the image for main'], total: 1 })))
  await act(async () => root.render(<QueryClientProvider client={client}><Now view={{ ...v }} /></QueryClientProvider>))
  await vi.waitFor(async () => { await act(async () => {}); expect(el.textContent).toContain('building the image for main') })
  v.detail!.steps = [step('standards', 'guidance/conformance', 'running')]
  vi.mocked(fetch).mockImplementation(async () => new Response(JSON.stringify({ running: true, lines: ['4 of 14 · lockfile-in-step (django) · checking'], total: 7 })))
  await act(async () => root.render(<QueryClientProvider client={client}><Now view={{ ...v }} /></QueryClientProvider>))
  await vi.waitFor(async () => { await act(async () => {}); expect(el.textContent).toContain('4 of 14'); expect(el.querySelector('progress')?.value).toBe(3) })
  vi.mocked(fetch).mockImplementation(async () => new Response(JSON.stringify({ running: true, lines: ['14 of 14 · readme (django) · reused · 0.0s'], total: 28 })))
  await act(async () => { await client.invalidateQueries({ queryKey: getKey('/steps/standards/live?from=0') }) })
  await vi.waitFor(async () => { await act(async () => {}); expect(el.textContent).toContain('14 of 14'); expect(el.querySelector('progress')?.value).toBe(14) })

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
  const text = renderToStaticMarkup(<QueryClientProvider client={client}><Now view={view()} /></QueryClientProvider>)
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
  el.innerHTML = renderToStaticMarkup(<QueryClientProvider client={client}><Now view={v} /></QueryClientProvider>)
  expect(el.textContent).toContain('Standards check')
  expect(el.textContent).toContain(line)
  const bar = el.querySelector('progress')!
  expect(bar).not.toBeNull()
  expect(bar.getAttribute('value')).toBe(value === null ? null : String(value))
  expect(bar.getAttribute('max')).toBe(total)
 } finally { client.clear() }
})

// Applicability and missing evidence must never look like a passing execution.
it.each(['not_applicable', 'skipped'] as const)('shows %s checks neutrally without a zero duration', status => {
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
 const v = view(); v.detail!.steps = [step('tests', 'tests', 'pass')]
 expect(html(v)).toContain('aria-label="number of tests unknown"')
 expect(html(v)).not.toContain('aria-label="passed"')
})
it('uses a neutral mark for pending work', () => {
 const el = document.createElement('div'); el.innerHTML = html(view())
 expect(el.querySelector('[aria-label="pending"]')?.textContent).toBe('○')
})

it('uses the owner’s failed verdict when a standards gate allowed existing failures', () => {
 const v = view(); v.detail!.steps = [{ ...step('guidance-conformance', 'guidance/conformance', 'pass'), statusLabel: 'failed' }]
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
 try {
  await act(async () => root.render(<QueryClientProvider client={client}><Now view={v} /></QueryClientProvider>))
  expect(el.querySelector('.ch-now > p')?.textContent).toBe(`Started ${fmtStamp(at)}`)
  expect(el.querySelector('.ch-now > p')?.textContent).toContain('Started')
  expect(el.textContent).toContain('4m 12s')
  await act(async () => vi.advanceTimersByTime(1000))
  expect(el.textContent).toContain('4m 13s')
  s.status = 'pending'; s.startedAt = ''
  s.attempt = 2
  v.waitingOn = {...v.waitingOn, step:s.id, attempt:2, queue:{binding:'checks',ahead:0,says:'waiting for a slot on checks, next in line'}, says:'building · waiting for a free slot'}
  await act(async () => root.render(<QueryClientProvider client={client}><Now view={{...v}} /></QueryClientProvider>))
  expect(el.querySelector('.ch-now > p')).toBeNull()
  expect(el.textContent).toContain('waiting for a slot on checks, next in line')
 } finally { act(() => root.unmount()); client.clear(); vi.useRealTimers() }
})
