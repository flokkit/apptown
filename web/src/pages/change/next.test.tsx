// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import type { ChangeView } from '../../api'
import type { WaitingOn } from '../../wire.gen'
import { newQueryClient } from '../../data'
import { ChangeStatus } from '../../Status'
import { StatePanel } from './StatePanel'
import { changeChip, nextStep } from './next'

it.each([
  ['check', 'review failed', 'bad', 'fail'],
  ['approval', 'waiting for you or Reviewers', 'warn', 'pending'],
  ['chat', 'chat open', 'busy', 'running'],
  ['ready', 'ready to continue', 'warn', 'pending'],
])('prints the owner wording and tone for %s in the row and change head', (kind, says, tone, cls) => {
  const waitingOn: WaitingOn = { kind, says, tone, run: 'child', step: 'review', checks: 1, who: [], you: 'no' }
  const v = { checks: { tone: 'neutral', text: 'Not checked yet.', started: false }, waitingOn, change: { id: 'change', number: 9, title: 'Fix photos', state: 'in_progress' }, statusLabel: 'working', state: 'working', projectSlug: 'photos', runs: [], detail: null } as unknown as ChangeView
  expect(changeChip(v)).toEqual({ text: says, cls })
  expect(nextStep(v).headline).toBe(says)
  const row = renderToStaticMarkup(<ChangeStatus waiting={waitingOn} />)
  expect(row).toContain(says)
  expect(row).toContain(`chip ${cls}`)
  const head = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={newQueryClient()}><StatePanel view={v} /></QueryClientProvider></MemoryRouter>)
  expect(head).toContain(says)
  expect(head).toContain(`tone-${tone}`)
})

it('places actions at the holding step supplied by the owner', () => {
  const v = { checks: { tone: 'neutral', text: 'Not checked yet.', started: false }, waitingOn: { kind: 'check', says: 'review failed', tone: 'bad', run: 'run', step: 'review', who: [], you: 'yes' }, change: { state: 'in_progress' }, next: [{ act: 'send-back' }], detail: {
    run: { id: 'run' }, events: [], next: [{ act: 'retry', stepId: 'review' }], steps: [
      { id: 'old', type: 'script', status: 'fail', evidence: {} },
      { id: 'review', type: 'agent/review', status: 'fail', evidence: { reason: 'A real problem.' } },
      { id: 'accept', type: 'approval', status: 'pending', evidence: {} },
    ],
  } } as unknown as ChangeView
  expect(nextStep(v).step?.id).toBe('review')
  expect(nextStep(v).primary).toBe('send-back')
  v.waitingOn.run = 'child'; v.waitingOn.step = 'child-approval'; v.waitingOn.kind = 'approval'
  expect(nextStep(v).primary).toBe('open-decision')
  expect(nextStep(v).step).toBeUndefined()
})
