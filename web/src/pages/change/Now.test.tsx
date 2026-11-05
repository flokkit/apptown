// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import type { ChangeView, Step } from '../../api'
import { getKey, newQueryClient } from '../../data'
import { Now } from './Now'
import { ContinueDecision, TurnDecision } from './Turn'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const at = '2026-11-04T01:00:00Z'
const step = (name: string, type: Step['type'], status: Step['status']): Step => ({ id: name, runId: 'landing', stage: 'land', name, type, status, statusLabel: status === 'pass' ? 'passed' : status, config: {}, evidence: {}, startedAt: status === 'pending' ? '' : at, finishedAt: status === 'pass' ? '2026-11-04T01:00:10Z' : '', overrideBy: '', overrideReason: '' } as Step)
const view = (): ChangeView => ({ change: { id: 'change', state: 'landing' }, waitingOn: { kind: 'work', run: 'landing', step: 'deploy', says: 'Landing: deploying — step 3 of 4' }, runs: [{ id: 'landing', role: 'landing' }], detail: { run: { id: 'landing', currentStage: 'land', status: 'running', createdAt: at }, steps: [step('accept', 'approval', 'pass'), step('merged', 'scm/merged', 'pass'), step('deploy', 'deploy', 'running'), step('record', 'artifact/require', 'pending')], stages: ['land'] } } as ChangeView)
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
 expect([...el.querySelectorAll('.ch-now-steps li strong')].map(s => s.textContent)).toEqual(['Approval', 'Merged', 'Deploy', 'Required file recorded'])
 expect(el.querySelector('details')).toBeNull()
})

it('groups consecutive checks and expands the whole group on failure or running', () => {
 const v = view()
 v.detail!.steps = [step('secret scan', 'scan', 'pass'), step('dependencies', 'scan', 'pass'), step('unit tests', 'tests', 'pass'), step('conformance', 'guidance/conformance', 'pending'), step('deploy', 'deploy', 'running')]
 const draw = () => { const el = document.createElement('div'); el.innerHTML = html(v); return el }
 expect(draw().querySelectorAll('.ch-now-steps li')).toHaveLength(2)
 expect(draw().querySelector('li')!.textContent).toContain('secret scan, dependencies, unit tests, conformance')
 for (const status of ['fail', 'running'] as const) {
  v.detail!.steps[1].status = status
  expect(draw().querySelectorAll('.ch-now-steps li')).toHaveLength(5)
  expect(draw().querySelectorAll('.ch-now-steps li')[1].textContent).toContain(status === 'fail' ? '✗' : 'Security scan')
 }
})

it('names work, keeps later stages out, and distinguishes pending deployment from completed work', () => {
 const v = view(); v.runs = []; v.change.state = 'in_progress'
 v.detail!.steps = [step('agent', 'agent/pi', 'pass'), step('deploy', 'deploy', 'pending'), { ...step('future', 'tests', 'pending'), stage: 'later' }]
 const text = html(v)
 expect(text).toContain('Work — step 2 of 2')
 expect(text).toContain('Agent finished')
 expect(text).toContain('Waiting to deploy')
 expect(text).not.toContain('future')
 v.detail!.steps[1].status = 'pass'
 expect(html(v)).toContain('Accepted work deployed')
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
  v.detail!.steps = [step('deploy', 'deploy', 'running')]
  vi.mocked(fetch).mockImplementation(async () => new Response(JSON.stringify({ running: true, lines: ['building the image for main'], total: 1 })))
  await act(async () => root.render(<QueryClientProvider client={client}><Now view={{ ...v }} /></QueryClientProvider>))
  await vi.waitFor(async () => { await act(async () => {}); expect(el.textContent).toContain('building the image for main') })
 } finally { await act(async () => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

it.each(['Accept', 'Continue'])('keeps the decision open after %s through progress and outcome, with Close available', async label => {
 class Observer { observe() {} disconnect() {} }
 vi.stubGlobal('ResizeObserver', Observer)
 HTMLDialogElement.prototype.showModal = function () { this.open = true }
 HTMLDialogElement.prototype.close = function () { this.open = false }
 const el = document.createElement('div'), root = createRoot(el)
 const draw = (active: boolean, progress?: string, outcome?: string) => act(() => root.render(<div className="ch-page"><TurnDecision active={active} actions={[label]} progress={progress && <p>{progress}</p>} outcome={<p>{outcome}</p>}>{label === 'Continue' ? <ContinueDecision busy={false} run={() => {}} /> : <button>{label}</button>}</TurnDecision></div>))
 try {
  draw(label === 'Accept')
  act(() => [...el.querySelectorAll('button')].find(b => b.textContent === (label === 'Accept' ? 'Decide…' : 'Continue'))!.click())
  const dialog = el.querySelector('dialog')!
  expect(dialog.open).toBe(true)
  draw(false, 'Landing — step 3 of 4')
  expect(dialog.open).toBe(true); expect(dialog.textContent).toContain('Landing — step 3 of 4')
  draw(false, undefined, 'landed')
  expect(dialog.open).toBe(true); expect(dialog.textContent).toContain('landed')
  draw(true, undefined, 'deploy failed')
  expect(dialog.open).toBe(true); expect(dialog.querySelector('button')).not.toBeNull()
  act(() => [...dialog.querySelectorAll('button')].find(b => b.textContent === 'Close')!.click())
  expect(el.querySelector('dialog')?.open ?? false).toBe(false)
 } finally { act(() => root.unmount()); vi.unstubAllGlobals() }
})

// Seed the completed response so rendering must read it, independent of fetch timing.
it.each([
 { running: true, total: 0 },
 { running: true, lines: null, total: 0 },
 { running: true, lines: [], total: 0 },
])('keeps deployment visible when progress has no lines: %j', progress => {
 const client = newQueryClient()
 client.setQueryData(getKey('/steps/deploy/live?from=0'), progress)
 try {
  const text = renderToStaticMarkup(<QueryClientProvider client={client}><Now view={view()} /></QueryClientProvider>)
  expect(text).toContain('Deploying the accepted work')
  expect(text).toContain('Landing — step 3 of 4')
 } finally { client.clear() }
})
