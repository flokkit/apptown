// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { newQueryClient } from '../../data'
import type { ChangeView, Step } from '../../api'
import { StatePanel, recoveryNote } from './StatePanel'
import { HeldDecision } from './HeldDecision'
import { viewerTurn } from './Turn'
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
    expect(el.textContent).toContain('Waiting for you: review-change: reviewer rejected: The page never loaded.')
    expect(el.textContent?.match(/The page never loaded\./g)).toHaveLength(1)
    expect(el.textContent?.match(/The screenshot shows the loading screen\./g)).toHaveLength(1)
    expect(el.textContent).not.toContain('loaded..')
    expect(el.textContent).not.toContain('Open the decision')
    expect(el.textContent).not.toContain('or abandon it')
    expect(el.querySelector('button.primary')?.textContent).toBe('Take it to chat')
    expect([...el.querySelectorAll('.ch-decision-choices button')].map((b) => b.textContent)).toEqual(['Take it to chat', 'Fix it', 'Accept it anyway', 'Try again', 'Abandon change'])
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
    expect(markup(v).querySelector('button.primary')?.textContent).toBe('Fix it')
    v.next = []
    v.detail!.next = [{ act: 'retry', stepId: 'review', step: 'review-change' }]
    const el = markup(v)
    expect(el.querySelector('button')?.textContent).toBe('Try again')
    for (const absent of ['Fix it', 'Take it to chat', 'Accept it anyway', 'Abandon change']) expect(el.textContent).not.toContain(absent)
    v.next = [{ act: 'abandon', stepId: null, step: '' }]
    v.detail!.next = []
    expect(viewerTurn(v)).toBe(true)
    expect(markup(v).querySelector('button')?.textContent).toBe('Abandon change')
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
    } finally {
      await act(async () => root.unmount())
      client.clear()
      vi.unstubAllGlobals()
    }
  })
})
