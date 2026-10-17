// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../data'
import type { Step } from '../../api'
import { ChildRunSteps, StepCard } from './Steps'

// Record ids are UUIDv7 strings.
const RUN = '00000000-0001-7000-8000-000000000000'
const CHILD = '00000000-0002-7000-8000-000000000000'
const STEP = '00000000-0009-7000-8000-000000000000'

// A decided approval of a commit shows what each vote was shown the commit
// adds, from the list saved with the vote (review E-03).
describe('a decided approval', () => {
  it('shows the list saved with each vote', () => {
    ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    const step = {
      id: STEP, runId: RUN, stage: 'approval', name: 'approve-landing', type: 'approval', status: 'pass', attempt: 1, overrideBy: '', config: { means: 'I am answerable' },
      evidence: {
        approver: 'bls', decision: 'approve', comment: '',
        decisions: [{ approver: 'bls', decision: 'approve', adds: { base: 'b', head: 'h', digest: 'd', rows: [], readFrom: 'secrets, services and app parts', nothingNew: '', lines: [{ kind: 'added', text: 'Secrets: STRIPE_KEY' }] } }],
      },
    } as unknown as Step
    const el = document.createElement('div')
    const root = createRoot(el)
    act(() => root.render(<QueryClientProvider client={newQueryClient()}><MemoryRouter>
      <StepCard step={step} onChange={() => {}} expanded />
    </MemoryRouter></QueryClientProvider>))
    const saved = el.querySelector('.saved-adds')
    expect(saved?.textContent).toContain('shown to bls when approving')
    expect(saved?.textContent).toContain('Secrets: STRIPE_KEY')
    expect(saved?.textContent).toContain('Read from secrets, services and app parts')
    act(() => root.unmount())
  })
})

// A conformance step lists the checks the server read from its evidence, and
// says so when it could not read them, never showing an empty or blank list.
describe('a conformance step', () => {
  const render = (checkReading: object, expanded = false) => {
    ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    const step = {
      id: STEP, runId: RUN, stage: 'check', name: 'guidance', type: 'guidance/conformance', status: 'pass', attempt: 1, overrideBy: '', config: {},
      evidence: { checks: [{ check: 'tone', verdict: '' }] }, checkReading,
    } as unknown as Step
    const el = document.createElement('div')
    const root = createRoot(el)
    act(() => root.render(<QueryClientProvider client={newQueryClient()}><MemoryRouter>
      <StepCard step={step} onChange={() => {}} expanded={expanded} />
    </MemoryRouter></QueryClientProvider>))
    const text = el.textContent ?? ''
    act(() => root.unmount())
    return text
  }
  it('keeps a passing step with readable checks to one line', () => {
    const text = render({ checks: [{ check: 'tidy', verdict: 'pass', block: 'b', version: 1, evaluator: 'sh' }] })
    expect(text).not.toContain('tidy')
    expect(render({ checks: [{ check: 'tidy', verdict: 'pass', block: 'b', version: 1, evaluator: 'sh' }] }, true)).toContain('tidy')
  })
  it('opens a passing step whose evidence could not be read, and says so', () => {
    const text = render({ unreadable: 'unreadable check evidence: check "tone": invalid check verdict: ""' })
    expect(text).toContain('evidence unreadable')
    expect(text).toContain('invalid check verdict')
  })
})

// A called workflow's child steps say when their check evidence could not be
// read, beside the recorded status.
describe('a child run', () => {
  it('marks a step whose check evidence could not be read', () => {
    ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    const step = {
      id: STEP, runId: CHILD, stage: 'check', name: 'guidance', type: 'guidance/conformance', status: 'pass', attempt: 1, config: {}, overrideBy: '',
      evidence: { checks: [{ check: 'tone', verdict: '' }] }, checkReading: { unreadable: 'unreadable check evidence' },
    } as unknown as Step
    const client = newQueryClient()
    client.setQueryData(getKey(`/runs/${CHILD}/trace`), { steps: [step] })
    const el = document.createElement('div')
    const root = createRoot(el)
    act(() => root.render(<QueryClientProvider client={client}><MemoryRouter>
      <ChildRunSteps childId={CHILD} workflow="checks v1" live={false} onChange={() => {}} />
    </MemoryRouter></QueryClientProvider>))
    expect(el.textContent).toContain('evidence unreadable')
    act(() => root.unmount())
  })
})

// A folded passing tests card still shows what its report said, and an
// approval card names the credential a vote came through.
describe('step cards say what the record holds', () => {
  const render = (step: Step) => {
    ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    const el = document.createElement('div')
    const root = createRoot(el)
    act(() => root.render(<QueryClientProvider client={newQueryClient()}><MemoryRouter>
      <StepCard step={step} onChange={() => {}} />
    </MemoryRouter></QueryClientProvider>))
    return { el, done: () => act(() => root.unmount()) }
  }
  const tests = (evidence: object) => ({ id: STEP, runId: RUN, stage: 'check', name: 'tests', type: 'tests', status: 'pass', attempt: 1, overrideBy: '', config: {},
    evidence: { command: 'go test -json ./...', durationMs: 1200, logTail: 'ok', ...evidence } }) as unknown as Step
  it('shows skips and unknown counts on the folded card', () => {
    for (const [evidence, note] of [
      [{ testCounts: { passed: 41, failed: 0, skipped: 3 } }, '41 passed, 3 skipped'],
      [{ testReport: 'no report' }, 'test counts not reported'],
      [{ testCounts: { passed: 44, failed: 0, skipped: 0 } }, '44 passed'],
    ] as const) {
      const { el, done } = render(tests(evidence))
      expect(el.querySelector('.step-head')?.textContent).toContain(note)
      expect(el.querySelector('.step-head .chip.stale') != null).toBe(note !== '44 passed')
      done()
    }
  })
  it('names the CLI token an approval came through', () => {
    const { el, done } = render({ id: STEP, runId: RUN, stage: 'approval', name: 'approve-landing', type: 'approval', status: 'pass', attempt: 1, overrideBy: '', config: {},
      evidence: { approver: 'bls', decision: 'approve', comment: '', decisions: [{ approver: 'bls', via: 'token-cli:claude-migrations-bls', decision: 'approve' }] } } as unknown as Step)
    act(() => (el.querySelector('button.subtle') as HTMLButtonElement)?.click())
    expect(el.textContent).toContain('approved by bls through CLI token claude-migrations-bls')
    done()
  })
})
