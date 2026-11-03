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
import { viewerTurn } from './Turn'
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
  it('shows the reason once and the offered buttons after three rounds, despite a later pending approval', () => {
    expect(viewerTurn(held())).toBe(true)
    const el = markup(held())
    expect(el.querySelector('.ch-headline')?.textContent).toBe('Waiting for a person')
    expect(el.textContent).toContain('The reviewer rejected this change.')
    expect(el.querySelector('p')?.textContent).not.toContain('review-change')
    expect(el.textContent?.match(/The page never loaded\./g)).toHaveLength(1)
    expect(el.textContent?.match(/The screenshot shows the loading screen\./g)).toHaveLength(1)
    expect(el.textContent).not.toContain('loaded..')
    expect(el.textContent).not.toContain('Open the decision')
    expect(el.textContent).not.toContain('or abandon it')
    expect(el.querySelector('.ch-decision-choices')?.lastElementChild?.textContent).toBe('Abandon change')
    expect([...el.querySelectorAll('button.primary')].map(b => b.textContent)).toEqual(['Accept it anyway'])
    expect([...el.querySelectorAll('button')].find(b => b.textContent === 'Accept it anyway')?.className).toBe('primary')
    expect([...el.querySelectorAll('.ch-decision-choices button')].map((b) => b.textContent)).toEqual(['Accept it anyway', 'Take it to chat', 'Fix it', 'Try again', 'Stop this attempt', 'Abandon change'])
  })
  it.each([['chat', 3, 'Take it to chat'], ['fix', 0, 'Fix it'], ['retry', 0, 'Try again'], ['recheck', 0, 'Re-check']] as const)('keeps one existing primary without acceptance: %s', (kind, rounds, primary) => {
    const v = held(rounds)
    v.detail!.next = v.detail!.next.filter(a => a.act !== 'override')
    if (kind === 'retry' || kind === 'recheck') v.next = [{ act: 'abandon' }] as ChangeView['next']
    if (kind === 'recheck') v.detail!.steps[1].type = 'artifact/require'
    const el = markup(v)
    expect([...el.querySelectorAll('button.primary')].map(b => b.textContent)).toEqual([primary])
    const labels = ['Take it to chat', ...(kind === 'chat' || kind === 'fix' ? ['Fix it'] : []), kind === 'recheck' ? 'Re-check' : 'Try again', 'Stop this attempt', 'Abandon change']
    expect([...el.querySelectorAll('.ch-decision-choices button')].map(b => b.textContent)).toEqual(labels)
    expect(heldLabels(v)).toEqual(labels)
    expect(el.querySelector('button.danger')?.textContent).toBe('Abandon change')
  })

  it('highlights only the first acceptance when several checks offer exceptions', () => {
    const v = held()
    v.detail!.next.reverse()
    v.detail!.next.push({ act: 'override', stepId: 'agent', step: 'apply-change' })
    const el = markup(v)
    expect([...el.querySelectorAll('button.primary')].map(b => b.textContent)).toEqual(['Accept it anyway'])
    const labels = ['Accept it anyway', 'Accept it anyway', 'Take it to chat', 'Fix it', 'Try again', 'Stop this attempt', 'Abandon change']
    expect([...el.querySelectorAll('.ch-decision-choices button')].map(b => b.textContent)).toEqual(labels)
    expect(heldLabels(v)).toEqual(labels)
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
  it('keeps a long remaining reason open and offers only the acts supplied', () => {
    const v = held(0)
    v.detail!.steps[1].evidence.reason += ' More details.'.repeat(50)
    expect(markup(v).querySelector('details')).toBeNull()
    expect(markup(v).textContent?.match(/More details\./g)).toHaveLength(50)
    expect([...markup(v).querySelectorAll('button.primary')].map(b => b.textContent)).toEqual(['Accept it anyway'])
    v.next = []
    v.detail!.next = [{ act: 'retry', stepId: 'review', step: 'review-change' }]
    const el = markup(v)
    expect(el.querySelector('button')?.textContent).toBe('Try again')
    for (const absent of ['Fix it', 'Take it to chat', 'Accept it anyway', 'Abandon change']) expect(el.textContent).not.toContain(absent)
    v.next = [{ act: 'abandon', stepId: null, step: '' }]
    v.detail!.next = []
    expect(viewerTurn(v)).toBe(true)
    expect([...markup(v).querySelectorAll('button')].at(-1)?.textContent).toBe('Abandon change')
    v.next = []
    v.detail!.next = [{ act: 'override', stepId: 'review', step: 'review-change' }]
    expect(markup(v).querySelector('button')?.textContent).toBe('Accept it anyway')
  })

  it('sends the failure as the note, retries the offered step, and continues the offered agent in chat', async () => {
    const sent: { url: string; body: unknown }[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') sent.push({ url, body: init.body ? JSON.parse(String(init.body)) : undefined })
      return new Response(JSON.stringify(init?.method === 'POST' ? { session: { id: 'chat' } } : { sessions: [] }))
    }))
    const el = document.createElement('div'), root = createRoot(el), client = newQueryClient()
    try {
      await act(async () => { root.render(<MemoryRouter><QueryClientProvider client={client}>
        <HeldDecision view={held()} onChange={() => {}} />
      </QueryClientProvider></MemoryRouter>) })
      const click = async (text: string) => {
        await act(async () => { await new Promise(r => setTimeout(r, 0)) })
        await act(async () => { [...el.querySelectorAll('button')].find((b) => b.textContent === text)!.click() })
        await act(async () => { await new Promise(r => setTimeout(r, 0)) })
      }
      await click('Fix it')
      expect(sent[0].url).toContain(`/changes/${CHANGE}/sendback`)
      expect(sent[0].body).toEqual({ note: recoveryNote(held().detail!) })
      await click('Try again')
      expect(sent[1].url).toContain('/steps/review/run')
      await click('Accept it anyway')
      await act(async () => {
        const input = el.querySelector<HTMLInputElement>('input[placeholder="Why (required)"]')!
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'Checked by hand')
        input.dispatchEvent(new Event('input', { bubbles: true }))
      })
      await click('Accept it anyway')
      expect(sent[2].url).toContain('/steps/review/override')
      expect(sent[2].body).toMatchObject({ reason: 'Checked by hand' })
      await click('Take it to chat')
      expect(sent[3]).toEqual({ url: '/api/v1/projects/fieldnotes/console', body: { changeId: CHANGE, continueStep: 'agent' } })
      await click('Abandon change')
      expect(sent).toHaveLength(4)
      await click('Abandon')
      expect(sent[4]).toEqual({ url: `/api/v1/changes/${CHANGE}/abandon`, body: {} })
      await click('Stop this attempt')
      await click('Yes')
      expect(sent[5]).toEqual({ url: '/api/v1/runs/run/abandon', body: { reason: 'stopped from the change page' } })
    } finally {
      await act(async () => root.unmount())
      client.clear()
      vi.unstubAllGlobals()
    }
  })
})

it.each([false, true])('retains the complete reviewer text and leads with structured findings when supplied (%s)', findings => {
  const text = 'The preview looked styled and the page loaded.\n\nThe adoption is incomplete. Every project needs the new rule.'
  const v = {
    change: { state: 'in_progress' }, state: 'waiting on you', next: [{ act: 'abandon' }],
    detail: { run: { currentStage: 'apply', status: 'running' }, next: [], events: [], steps: [
      { id: 'review', name: 'review-change', type: 'agent/review', stage: 'apply', status: 'fail', config: {}, evidence: { summary: text, ...(findings ? { findings: ['Two projects lack the rule.'] } : {}) } },
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
