// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { newQueryClient } from '../../data'
import type { ChangeView, Step } from '../../api'
import { ApproveEvidence, MainConflictLine, StatePanel, notApplicableHere, requestShortfall, requestUnchecked } from './StatePanel'

// Record ids are UUIDv7 strings.
const CHANGE = '00000000-0004-7000-8000-000000000000'

const evidence = (shortfall?: string) => renderToStaticMarkup(
  <MemoryRouter><QueryClientProvider client={newQueryClient()}>
    <ApproveEvidence changeId={CHANGE} soFar="The tab title changed." shortfall={shortfall} />
  </QueryClientProvider></MemoryRouter>)

describe('the approval says when the summary reads the work as short of the request', () => {
  // A change whose summary says the request is unfinished must say so next
  // to the approve buttons, not only in the prose above them.
  it('warns with the shortfall when the request is not met or only partly met', () => {
    expect(requestShortfall({ requestMet: 'no', requestGap: 'only the tab title changed' })).toBe('only the tab title changed.')
    expect(requestShortfall({ requestMet: 'partly', requestGap: '' })).toBe('it does only part of what was asked.')
    const html = evidence(requestShortfall({ requestMet: 'no', requestGap: 'only the tab title changed' }))
    expect(html).toContain('class="ch-shortfall"')
    expect(html).toContain('only the tab title changed')
  })
  // Met and unknown say nothing: unknown is not a warning, nor a pass.
  it.each(['yes', 'unknown', undefined])('says nothing when the reading is %s', (met) => {
    expect(requestShortfall({ requestMet: met, requestGap: 'ignored' })).toBeUndefined()
    expect(evidence(undefined)).not.toContain('ch-shortfall')
  })
})

describe('the approval says when the summary could not check all of the request', () => {
  it('shows unknown with what would settle it, as could-not-check and not as a shortfall', () => {
    const gap = 'the rest of the diff was not read. To satisfy it: the rest of the diff, files X and Y.'
    expect(requestShortfall({ requestMet: 'unknown', requestGap: gap })).toBeUndefined()
    expect(requestUnchecked({ requestMet: 'unknown', requestGap: gap })).toBe(gap)
    expect(requestUnchecked({ requestMet: 'no', requestGap: gap })).toBeUndefined()
    const html = renderToStaticMarkup(
      <MemoryRouter><QueryClientProvider client={newQueryClient()}>
        <ApproveEvidence changeId={CHANGE} soFar="x" unchecked={gap} />
      </QueryClientProvider></MemoryRouter>)
    expect(html).toContain('could not check all of the request')
    expect(html).toContain('To satisfy it')
    expect(html).not.toContain('falls short')
  })
})

// Main AppTown could not merge in is said where the change's state is,
// with the files and the command that catches up in the chat.
describe('main that conflicts with the change', () => {
  it('names the files and the catch-up', () => {
    const html = renderToStaticMarkup(<MainConflictLine conflict={{ mainBranch: 'main', main: 'abc', step: 'rework', files: ['README.md'],
      catchUp: 'git fetch "$APPTOWN_MAIN_REMOTE" main && git merge --no-edit -m "Merge main into change-4" FETCH_HEAD' }} />)
    const el = document.createElement('div')
    el.innerHTML = html
    expect(el.textContent).toBe('main has moved and AppTown could not merge it in before step rework: README.md conflicts. Catch up in the change\'s chat: git fetch "$APPTOWN_MAIN_REMOTE" main && git merge --no-edit -m "Merge main into change-4" FETCH_HEAD')
  })
})

describe('the state panel', () => {
  it('says main conflicts while the change has not caught up', () => {
    const conflict = { mainBranch: 'main', main: 'abc', step: 'rework', files: ['README.md'], catchUp: 'git fetch' }
    const view = { change: { id: CHANGE, number: 4, state: 'in_progress' }, projectSlug: 'demo', detail: null, stateDetail: '', runs: [], mainConflict: conflict } as unknown as ChangeView
    const html = (v: ChangeView) => renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={newQueryClient()}>
      <StatePanel view={v} onChange={() => {}} workTab={() => '/work'} />
    </QueryClientProvider></MemoryRouter>)
    expect(html(view)).toContain('AppTown could not merge it in before step rework')
    expect(html({ ...view, mainConflict: null })).not.toContain('could not merge')
  })
})

describe('the approval panel and not-applicable checks', () => {
 it('lists every check and step of the run that is not applicable, with its reason', () => {
  const steps = [
   { type: 'guidance/conformance', name: 'guidance', status: 'pass', evidence: {}, checkReading: { checks: [
    { check: 'builds', verdict: 'pass' }, { check: 'go-vet-clean', verdict: 'not-applicable', detail: 'no part is built with go' }] } },
   { type: 'scan', name: 'scan-image:trivy', status: 'not_applicable', evidence: { notApplicable: 'the public part is a static site: there is no image' } },
   { type: 'tests', name: 'tests', status: 'pass', evidence: {} },
  ] as unknown as Step[]
  expect(notApplicableHere(steps)).toEqual([
   { name: 'go-vet-clean', why: 'no part is built with go' },
   { name: 'scan-image:trivy', why: 'the public part is a static site: there is no image' },
  ])
 })
})

describe('handover checks', () => {
  const view = (status: string, evidence: object = {}, events: unknown[] = []) => ({
    change: { id: CHANGE, number: 4, state: 'in_progress' }, projectSlug: 'demo', runs: [], state: 'blocked', statusLabel: 'failed',
    detail: { run: { id: 'run', workflowName: 'quick-change', currentStage: 'apply', status: 'running' }, state: 'blocked', next: [], events,
      steps: [{ id: 'agent', name: 'apply-change', type: 'agent/pi', stage: 'apply', status, evidence, config: {} },
        { id: 'boot', name: 'boot', type: 'preview', stage: 'apply', status: 'fail', evidence: { reason: 'fourth failure' }, config: {} }] },
  } as unknown as ChangeView)
  const html = (v: ChangeView) => renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={newQueryClient()}>
    <StatePanel view={v} onChange={() => {}} workTab={() => '/work'} />
  </QueryClientProvider></MemoryRouter>)
  const events = ['first failure', 'second failure', 'third failure'].map((reason) => ({ kind: 'step.rework', payload: { step: 'boot', reason } }))
  it('leads with agent work while revising, even with the earlier failed check', () => {
    expect(html(view('pending', {}, events.slice(0, 2)))).toContain('The agent is working on it (round 2 of 3)')
    expect(html(view('running', {}, events))).not.toContain('the agent tried 3 times;')
  })
  it('keeps every attempt reason after the latest check replaces its evidence', () => {
    const result = html(view('pass', {}, events))
    expect(result).toContain('the agent tried 3 times; boot still fails: fourth failure')
    for (const reason of ['first failure', 'second failure', 'third failure']) expect(result).toContain(reason)
    expect(result).toContain('Take it to chat, or abandon it.')
  })
  it('lists every person action together and keeps an inherited boot cause once', () => {
    const v = view('needs_human', { actions: ['Decide the retention period', 'Bind WP_ADMIN_PASSWORD'] })
    v.detail!.steps[1] = { ...v.detail!.steps[1], status: 'needs_human', evidence: { actions: ['Repair the browser configuration'] } } as Step
    v.detail!.steps.push({ name: 'review', stage: 'apply', type: 'agent/review', status: 'skipped', evidence: { blockedBy: 'boot' } } as Step)
    const el = document.createElement('div'); el.innerHTML = html(v)
    const actions = [...el.querySelectorAll('.ch-state-words li')].map((e) => e.textContent)
    expect(actions).toEqual(['Decide the retention period', 'Bind WP_ADMIN_PASSWORD', 'Repair the browser configuration'])
    expect(el.textContent).toContain('Waiting for you:')
  })
  it('leads with the person action while an agent is still pending', () => {
    const v = view('pending')
    v.detail!.steps[1] = { ...v.detail!.steps[1], status: 'needs_human', evidence: { actions: ['Bind WP_ADMIN_PASSWORD'] } } as Step
    const el = document.createElement('div'); el.innerHTML = html(v)
    expect(el.querySelector('.ch-headline')?.textContent).toBe('Waiting for you: Bind WP_ADMIN_PASSWORD')
  })
  it('says old attempt reasons were not kept and a person retry resets the allowance', () => {
    const result = html(view('pass', {}, [...events, { kind: 'step.retried', payload: {} }, { kind: 'step.rework', payload: { step: 'boot', reason: 'preview failed — evidence fed back to the agent' } }]))
    expect(result).toContain('The reason was not kept.')
    expect(result).not.toContain('the agent tried 3 times;')
  })
  it('says passed when the checked run is done', () => {
    const v = view('pass'); v.detail!.run.status = 'done'; v.detail!.steps[1].status = 'pass'
    expect(html(v)).toContain('Passed')
  })
})

 it('states a landing failure with the same state line and offers Fix it', () => {
  const v = { change: { id: CHANGE, number: 3, state: 'landing' }, projectSlug: 'lunch-orders', runs: [], state: 'blocked', statusLabel: 'failed', next: [{ act: 'send-back', stepId: null, step: '' }],
    detail: { run: { id: 'run', currentStage: 'land', status: 'running' }, state: 'blocked', next: [], events: [], steps: [
      { id: 'boot', name: 'boot', type: 'preview', stage: 'land', status: 'fail', evidence: { reason: 'the app did not start' }, config: {} }
    ] } } as unknown as ChangeView
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={newQueryClient()}>
    <StatePanel view={v} onChange={() => {}} workTab={() => '/work'} />
  </QueryClientProvider></MemoryRouter>)
  const el = document.createElement('div'); el.innerHTML = html
  expect(el.querySelector('.ch-headline')?.textContent).toBe('Waiting for you: boot failed: the app did not start')
  expect(el.textContent).toContain('Fix it')
 })
