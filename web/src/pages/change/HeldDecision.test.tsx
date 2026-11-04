// @vitest-environment jsdom
import { describe, expect, it, beforeEach, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { newQueryClient } from '../../data'
import type { ChangeView, Step } from '../../api'
import { StatePanel, recoveryNote } from './StatePanel'
import { HeldDecision, heldLabels } from './HeldDecision'
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
beforeEach(() => vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} }))
const CHANGE = '00000000-0004-7000-8000-000000000000'
describe('held runs offer recovery rather than an approval', () => {
  const held = (rounds = 3): ChangeView => ({
    change: { id: CHANGE, number: 4, title: 'Fix the menu', state: 'in_progress' }, projectSlug: 'fieldnotes', state: 'waiting on you', statusLabel: 'waiting for you',
    stateDetail: 'review-change (fail): reviewer rejected: The page never loaded.',
    next: [{ act: 'send-back' }, { act: 'abandon' }],
    detail: {
      run: { id: 'run', status: 'running', currentStage: 'apply' }, next: [
        { act: 'override', stepId: 'review' }, { act: 'retry', stepId: 'review' }, { act: 'rework', stepId: 'agent' },
      ],
      steps: [
        { id: 'agent', name: 'apply-change', type: 'agent/pi', stage: 'apply', status: 'pass', evidence: {}, config: {} },
        { id: 'review', name: 'review-change', type: 'agent/review', stage: 'apply', status: 'fail', evidence: { reason: 'reviewer rejected: The page never loaded. The screenshot shows the loading screen.' }, config: {} },
        { id: 'accept', name: 'accept', type: 'approval', stage: 'apply', status: 'pending', evidence: {}, config: {} },
      ],
      events: Array.from({ length: rounds }, (_, i) => ({ kind: 'step.rework', payload: { step: 'review-change', reason: `Earlier attempt ${i + 1}` } })),
    },
  } as unknown as ChangeView)
  const markup = (v: ChangeView) => {
    const el = document.createElement('div')
    el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={newQueryClient()}>
      <StatePanel view={v} /><HeldDecision view={v} onChange={() => {}} />
    </QueryClientProvider></MemoryRouter>)
    return el
  }
  it.each([
    ['review rejection', 'agent/review', 'fail', 'override', 'Accept anyway'],
    ['failed check', 'script', 'fail', 'override', 'Accept anyway'],
    ['could not run', 'script', 'error', 'retry', 'Try again'],
    ['record required', 'artifact/require', 'needs_human', 'retry', 'Re-check'],
    ['decision required', 'adr/require', 'fail', 'retry', 'Re-check'],
    ['merge required', 'scm/merged', 'fail', 'retry', 'Re-check'],
  ])('offers three ordered decisions and one highlighted forward act: %s', (_name, type, status, forward, label) => {
    const v = held()
    Object.assign(v.detail!.steps[1], { type, status })
    v.detail!.next = [{ act: forward, stepId: 'review' }, { act: 'rework', stepId: 'agent' }] as ChangeView['next']
    const el = markup(v)
    const labels = [label, 'Send back…', 'Abandon change']
    expect([...el.querySelectorAll('.ch-decision-choices button')].map(b => b.textContent)).toEqual(labels)
    expect(heldLabels(v)).toEqual(labels)
    expect([...el.querySelectorAll('.ch-decision-choices button')].map(b => b.className)).toEqual(['primary', '', 'danger'])
    expect(el.querySelector('input, details')).toBeNull()
    expect(el.textContent).not.toContain('Stop this attempt')
    expect(el.querySelector('a')?.textContent).toBe('Open the conversation →')
  })
  it('selects only one offered forward act and never retries a reviewer rejection', () => {
    const v = held()
    expect(heldLabels(v)).toEqual(['Accept anyway', 'Send back…', 'Abandon change'])
    v.detail!.next = v.detail!.next.filter(a => a.act !== 'override')
    expect(heldLabels(v)).toEqual(['Send back…', 'Abandon change'])
    v.next = []
    v.detail!.next = []
    expect(heldLabels(v)).toEqual([])
    expect(markup(v).querySelector('button')).toBeNull()
  })
  it('uses the existing primary when more than one forward act is eligible', () => {
    const v = held(0)
    v.next = []
    v.detail!.steps[1].type = 'artifact/require'
    expect(heldLabels(v)).toEqual(['Re-check', 'Send back…'])
  })

  it('keeps the full failure output and requested human actions in the send-back note', () => {
    const v = held()
    v.detail!.steps[1] = { ...v.detail!.steps[1], type: 'script', evidence: { log: 'The form failed validation.\n' + 'full log\n'.repeat(60) } } as Step
    v.detail!.steps[0] = { ...v.detail!.steps[0], status: 'needs_human', evidence: { actions: ['Bind the missing secret'] } } as Step
    const note = recoveryNote(v.detail!)
    expect(note).toContain('The form failed validation.')
    expect(note.match(/full log/g)).toHaveLength(60)
    expect(note).toContain('Bind the missing secret')
  })
  it('prefills recovery, cancels exactly, sends feedback and keeps stuck-agent continuation behind the link', async () => {
    const sent: { url: string; body: unknown }[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') sent.push({ url, body: init.body ? JSON.parse(String(init.body)) : undefined })
      return new Response(JSON.stringify({ session: { id: 'chat' } }))
    }))
    const el = document.createElement('div'), root = createRoot(el), client = newQueryClient()
    document.body.append(el)
    const v = held()
    const draw = async () => act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><HeldDecision view={v} onChange={() => {}} /></QueryClientProvider></MemoryRouter>))
    const click = async (text: string) => act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === text)!.click())
    try {
      await draw()
      const initial = el.querySelector('.ch-decision-choices')!.innerHTML
      await click('Send back…')
      expect(el.querySelector('textarea')!.value).toBe(recoveryNote(v.detail!))
      expect(document.activeElement).toBe(el.querySelector('textarea'))
      await click('Cancel')
      expect(el.querySelector('.ch-decision-choices')!.innerHTML).toBe(initial)
      await click('Send back…'); await click('Send to agent')
      expect(sent.pop()).toEqual({ url: `/api/v1/changes/${CHANGE}/sendback`, body: { note: recoveryNote(v.detail!) } })
      await click('Cancel')
      await click('Accept anyway')
      expect(el.querySelector('button.primary')!.hasAttribute('disabled')).toBe(true)
      expect(el.querySelectorAll('button')).toHaveLength(2)
      await act(async () => {
        const input = el.querySelector<HTMLInputElement>('input[placeholder="Why (required)"]')!
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'Checked by hand')
        input.dispatchEvent(new Event('input', { bubbles: true }))
      })
      await click('Accept anyway')
      expect(sent.pop()).toMatchObject({ url: '/api/v1/steps/review/override', body: { reason: 'Checked by hand' } })
      await act(async () => el.querySelector('a')!.click())
      expect(sent.pop()).toEqual({ url: '/api/v1/projects/fieldnotes/console', body: { changeId: CHANGE, continueStep: 'agent' } })
      // A rework-only offer uses the existing step-run feedback contract.
      v.next = []
      await draw(); await click('Send back…'); await click('Send to agent')
      expect(sent.pop()).toEqual({ url: '/api/v1/steps/agent/run', body: { feedback: recoveryNote(v.detail!) } })
      await click('Cancel')
      v.detail!.next = [{ act: 'retry', stepId: 'review' }] as ChangeView['next']
      Object.assign(v.detail!.steps[1], { type: 'script', status: 'error' })
      await draw(); await click('Try again')
      expect(sent.pop()?.url).toBe('/api/v1/steps/review/run')
      Object.assign(v.detail!.steps[1], { type: 'artifact/require', status: 'needs_human' })
      await draw(); await click('Re-check')
      expect(sent.pop()?.url).toBe('/api/v1/steps/review/run')
    } finally { await act(async () => root.unmount()); el.remove(); client.clear(); vi.unstubAllGlobals() }
  })

})

it.each([false, true])('retains the complete reviewer text and leads with structured findings when supplied (%s)', findings => {
  const text = 'The preview looked styled and the page loaded.\n\nThe adoption is incomplete. Every project needs the new rule.'
  const v = {
    change: { state: 'in_progress' }, state: 'waiting on you', next: [{ act: 'abandon' }],
    detail: { run: { currentStage: 'apply', status: 'running' }, next: [], events: [], steps: [
      { id: 'review', name: 'review-change', type: 'agent/review', stage: 'apply', status: 'fail', config: {}, evidence: { reason: `reviewer rejected: ${text}`, summary: text, ...(findings ? { findings: ['Two projects lack the rule.'] } : {}) } },
    ] },
  } as unknown as ChangeView
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={newQueryClient()}><HeldDecision view={v} onChange={() => {}} /></QueryClientProvider></MemoryRouter>)
  expect(el.querySelector('p')?.textContent).toBe('The reviewer rejected this change.')
  const reason = el.querySelector('.ch-review-text')!.textContent!
  expect(reason).toBe(findings ? 'Two projects lack the rule.\n\n' + text : text)
  expect(el.querySelector('.ch-technical[aria-hidden="true"]')?.textContent).toBe('review-change')
})

it.each(['landing', 'finished'])('omits Stop this attempt when it is not an act in this state (%s)', state => {
  const v = { change: { state: state === 'landing' ? 'landing' : 'in_progress' }, state: 'waiting on you', next: [{ act: 'abandon' }], detail: {
    run: { currentStage: 'apply', status: state === 'finished' ? 'pass' : 'running' }, next: [], events: [], steps: [
      { type: 'agent/review', name: 'review-change', stage: 'apply', status: 'fail', evidence: { reason: 'An objection.' } },
    ],
  } } as unknown as ChangeView
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={newQueryClient()}><HeldDecision view={v} onChange={() => {}} /></QueryClientProvider></MemoryRouter>)
  expect(html).not.toContain('Stop this attempt')
})
