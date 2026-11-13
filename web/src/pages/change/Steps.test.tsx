// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../data'
import type { Step } from '../../api'
import { ChildRunSteps, StepCard } from './Steps'

// A status's details ride in its title, not in the page text.
const details = (root: Element) => [...root.querySelectorAll('[data-help], [title]')].map(e => e.getAttribute('data-help') ?? e.getAttribute('title')).join(' | ')

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
        decisions: [{ approver: 'bls', decision: 'approve', adds: { base: 'b', head: 'h', digest: 'd', rows: [{ name: 'secrets', added: ['STRIPE_KEY'], removed: [] }, { name: 'services', added: [], removed: [] }, { name: 'app parts', added: [], removed: [] }], readFrom: 'secrets, services and app parts', nothingNew: '', lines: [{ kind: 'added', text: 'Secrets: STRIPE_KEY' }] } }],
      },
    } as unknown as Step
    const el = document.createElement('div')
    const root = createRoot(el)
    act(() => root.render(<QueryClientProvider client={newQueryClient()}><MemoryRouter>
      <StepCard step={step} onChange={() => {}} />
    </MemoryRouter></QueryClientProvider>))
    const saved = el.querySelector('.saved-adds')
    expect(saved?.textContent).toContain('shown to bls when approving')
    expect(saved?.textContent).toContain('+ needs the secret STRIPE_KEY, which someone must set before it can run')
    expect(saved?.textContent).toContain('Changes the app contract')
    act(() => root.unmount())
  })
})

// A conformance step lists the checks the server read from its evidence, and
// says so when it could not read them, never showing an empty or blank list.
describe('a conformance step', () => {
  const render = (checkReading: object) => {
    ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    const step = {
      id: STEP, runId: RUN, stage: 'check', name: 'guidance', type: 'guidance/conformance', status: 'pass', statusLabel: 'unknown', attempt: 1, overrideBy: '', config: {},
      evidence: { checks: [{ check: 'tone', verdict: '' }] }, checkReading,
    } as unknown as Step
    const el = document.createElement('div')
    const root = createRoot(el)
    act(() => root.render(<QueryClientProvider client={newQueryClient()}><MemoryRouter>
      <StepCard step={step} onChange={() => {}} />
    </MemoryRouter></QueryClientProvider>))
    const text = el.textContent ?? ''
    act(() => root.unmount())
    return text
  }
  it('shows a passing step with readable checks openly', () => {
    const text = render({ checks: [{ check: 'tidy', verdict: 'pass', statusLabel: 'passed', block: 'b', version: 1, evaluator: 'sh' }] })
    expect(text).toContain('tidy')
  })
  it('opens a passing step whose evidence could not be read, and says so', () => {
    const text = render({ unreadable: 'unreadable check evidence: check "tone": invalid check verdict: ""' })
    expect(text).toContain('unknown')
    expect(text).toContain('invalid check verdict')
  })
})

// A called workflow's child steps say when their check evidence could not be
// read, in the recorded status's details.
describe('a child run', () => {
  it('marks a step whose check evidence could not be read', () => {
    ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    const step = {
      id: STEP, runId: CHILD, stage: 'check', name: 'guidance', type: 'guidance/conformance', status: 'pass', statusLabel: 'unknown', attempt: 1, config: {}, overrideBy: '',
      evidence: { checks: [{ check: 'tone', verdict: '' }] }, checkReading: { unreadable: 'unreadable check evidence' },
    } as unknown as Step
    const client = newQueryClient()
    client.setQueryData(getKey(`/runs/${CHILD}/trace`), { steps: [step] })
    const el = document.createElement('div')
    const root = createRoot(el)
    act(() => root.render(<QueryClientProvider client={client}><MemoryRouter>
      <ChildRunSteps childId={CHILD} workflow="checks v1" live={false} onChange={() => {}} />
    </MemoryRouter></QueryClientProvider>))
    expect(details(el)).toContain('unreadable check evidence')
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
  const tests = (evidence: object) => ({ id: STEP, runId: RUN, stage: 'check', name: 'tests', type: 'tests', status: 'pass', statusLabel:'passed', attempt: 1, overrideBy: '', config: {},
    evidence: { command: 'go test -json ./...', durationMs: 1200, logTail: 'ok', ...evidence } }) as unknown as Step
  it('shows the recorded test inputs in the opened evidence', () => {
    const step = tests({ inputs: { tree: 'actual-tree', declarations: [{ part: 'web', folder: '.', command: 'go test ./...' }], apptownVersion: 'dev-recorded', executions: [{ part: 'web', folder: '.', command: 'go test ./...', imageDigest: 'sha256:observed', toolchain: 'unknown', services: { postgres: 'unknown' } }] } })
    const { el, done } = render(step)
    expect(el.textContent).toContain('Test inputs')
    expect(el.textContent).toContain('actual-tree')
    expect(el.textContent).toContain('sha256:observed')
    expect(el.textContent).toContain('dev-recorded')
    expect(el.textContent).toContain('unknown')
    expect(el.querySelector('.step-head')?.textContent).not.toContain('actual-tree')
    done()
  })
  it('shows skips and unknown counts on the folded card', () => {
    for (const [evidence, note] of [
      [{ testCounts: { passed: 41, failed: 0, skipped: 3 } }, '41 passed, 3 skipped'],
      [{ testReport: 'no report' }, 'Number of tests unknown'],
      [{ testCounts: { passed: 44, failed: 0, skipped: 0 } }, '44 passed'],
    ] as const) {
      const { el, done } = render(tests(evidence))
      expect(el.querySelector('.step-head')?.textContent).toContain(note)
      if (!('testCounts' in evidence)) {
        expect(el.querySelector('.step-head .chip.unknown')?.textContent).toContain('unknown')
        expect(el.querySelector('.step-head .chip.pass')).toBeNull()
      }
      done()
    }
  })
  it('keeps a failed tests step failed while marking its unknown count', () => {
    const {el,done}=render({...tests({testReport:'no report'}),status:'fail',statusLabel:'failed'} as Step)
    expect(el.querySelector('.step-head .chip.fail')?.textContent).toContain('failed')
    expect(el.querySelector('.step-head .chip.unknown')).not.toBeNull()
    expect(el.querySelector('.step-head')?.textContent).toContain('Number of tests unknown')
    done()
  })
  it('names the CLI token an approval came through', () => {
    const { el, done } = render({ id: STEP, runId: RUN, stage: 'approval', name: 'approve-landing', type: 'approval', status: 'pass', attempt: 1, overrideBy: '', config: {},
      evidence: { approver: 'bls', decision: 'approve', comment: '', decisions: [{ approver: 'bls', via: 'token-cli:claude-migrations-bls', decision: 'approve' }] } } as unknown as Step)
    act(() => (el.querySelector('button.subtle') as HTMLButtonElement)?.click())
    expect(el.textContent).toContain('approved by bls through CLI token claude-migrations-bls')
    done()
  })
})

it('uses the owner word for an excepted step in Work', () => {
  const step = { id: STEP, runId: RUN, stage: 'checks', name: 'tests', type: 'tests', status: 'fail', statusLabel: 'excepted', overrideBy: 'Pat', overrideReason: 'Accepted for this change', config: {}, evidence: {} } as unknown as Step
  const el = document.createElement('div')
  const root = createRoot(el)
  act(() => root.render(<QueryClientProvider client={newQueryClient()}><MemoryRouter><StepCard step={step} onChange={() => {}} /></MemoryRouter></QueryClientProvider>))
  expect(el.querySelector('.chip')?.textContent).toBe('● excepted')
  expect(el.querySelector('.chip')?.className).not.toContain('fail')
  act(() => root.unmount())
})

it('labels the currently offered approval as waiting for you in Work', () => {
 const el = document.createElement('div'), root = createRoot(el), client = newQueryClient()
 const step = { id: STEP, runId: RUN, stage: 'check', name: 'accept', type: 'approval', status: 'pending', statusLabel: 'waiting for checks', overrideBy: '', evidence: {}, config: {} } as unknown as Step
 act(() => root.render(<QueryClientProvider client={client}><MemoryRouter><StepCard step={step} awaitingDecision hideApproveForm onChange={() => {}} /></MemoryRouter></QueryClientProvider>))
 expect(el.querySelector('.step-head .chip')?.textContent).toContain('waiting for you')
 expect(el.querySelector('.step-head .chip')?.textContent).not.toContain('waiting for checks')
 act(() => root.unmount()); client.clear()
})

// While a check has failed, approving would not land the change: the form
// waits and the step names the way past the failed check instead.
it('holds the approval form while a check has failed', () => {
 const el = document.createElement('div'), root = createRoot(el), client = newQueryClient()
 const step = { id: STEP, runId: RUN, stage: 'check', name: 'accept', type: 'approval', status: 'pending', statusLabel: 'waiting for checks', overrideBy: '', evidence: {}, config: {} } as unknown as Step
 act(() => root.render(<QueryClientProvider client={client}><MemoryRouter><StepCard step={step} failedCheck="Style" onChange={() => {}} /></MemoryRouter></QueryClientProvider>))
 expect(el.textContent).toContain('Approval opens once the checks pass. “Style” failed')
 expect([...el.querySelectorAll('button')].some(b => b.textContent === 'Approve')).toBe(false)
 act(() => root.unmount()); client.clear()
})

it('holds a child decision while its checks are running', async () => {
  const approval = { id: STEP, runId: CHILD, stage: 'check', name: 'accept', type: 'approval', status: 'pending', statusLabel: 'waiting for you', config: { means: 'I accept the complete child report.', instructions: 'Read its findings before accepting.' }, evidence: {}, overrideBy: '' } as unknown as Step
  const check = { ...approval, id: 'check', name: 'tests', type: 'script', status: 'running', config: {} } as Step
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  client.setQueryData(getKey(`/runs/${CHILD}/trace`), { run: { status: 'running', currentStage: 'check' }, steps: [check, approval] })
  act(() => root.render(<QueryClientProvider client={client}><MemoryRouter><ChildRunSteps childId={CHILD} workflow="child" live onChange={() => {}} /></MemoryRouter></QueryClientProvider>))
  expect(el.textContent).toContain('Checks are running; you can accept when they finish.')
  expect(el.querySelector('.decision-form')).toBeNull()
  act(() => root.unmount()); client.clear()
})

// A completed scan keeps the route to its owning Security page even when
// there are no findings to act on in this run.
it('opens Security from a scan’s details with its zero findings count', () => {
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  client.setQueryData(getKey(`/runs/${RUN}/findings`), { projectSlug: 'game', findings: [], unread: [] })
  const step = { id: STEP, runId: RUN, name: 'secret-scan', stage: 'check', type: 'scan', status: 'pass', overrideBy: '', config: {}, evidence: { binding: 'gitleaks', findings: { total: 0, actionable: 0, dispositioned: 0 } } } as unknown as Step
  try {
    act(() => root.render(<QueryClientProvider client={client}><MemoryRouter><StepCard evidenceOnly step={step} onChange={() => {}} /></MemoryRouter></QueryClientProvider>))
    expect(el.querySelector('a[href="/projects/game/security/findings"]')?.textContent).toBe('Security › Findings (0) →')
    expect(el.textContent).not.toContain('Fix…')
    expect(el.textContent).not.toContain('Suppress…')
  } finally { act(() => root.unmount()); client.clear() }
})

// The Checks record lists several checks; each one says its result.
it('shows a check’s result word on its evidence card', () => {
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  const step = { id: STEP, runId: RUN, name: 'style', stage: 'check', type: 'check', isCheck: true, status: 'fail', statusLabel: 'failed', overrideBy: '', config: {}, evidence: {}, finishedAt: '2026-10-03T09:00:00Z' } as unknown as Step
  try {
    act(() => root.render(<QueryClientProvider client={client}><MemoryRouter><StepCard evidenceOnly step={step} onChange={() => {}} /></MemoryRouter></QueryClientProvider>))
    expect(el.querySelector('.step-head .chip-word')?.textContent).toBe('failed')
    expect(el.querySelector('.ch-check-about')?.textContent).toContain('Finished')
  } finally { act(() => root.unmount()); client.clear() }
})

// How a scanner ran (binding, image, exit, config hash) is for administrators:
// it shows only with the technical switch, as the script line does.
it.each([false, true])('shows how a scan ran only with the technical switch (%s)', technical => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  const user = `scan-viewer-${technical}`
  if (technical) localStorage.setItem(`apptown.technical-detail.${user}`, '1')
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  client.setQueryData(getKey('/auth/me'), { mode: 'internal', user })
  client.setQueryData(getKey(`/runs/${RUN}/findings`), { projectSlug: 'game', findings: [], unread: [] })
  const step = { id: STEP, runId: RUN, name: 'secret-scan', stage: 'check', type: 'scan', status: 'pass', overrideBy: '', config: {}, evidence: { binding: 'gitleaks', bindingVersion: 3, image: 'zricethezav/gitleaks:v8', exitCode: 0, durationMs: 412, configHash: 'abc123', findings: { total: 0, actionable: 0, dispositioned: 0 } } } as unknown as Step
  try {
    act(() => root.render(<QueryClientProvider client={client}><MemoryRouter><StepCard evidenceOnly step={step} onChange={() => {}} /></MemoryRouter></QueryClientProvider>))
    const raw = ['gitleaks v3 (zricethezav/gitleaks:v8)', 'config sha256:abc123']
    for (const r of raw) expect(el.textContent?.includes(r)).toBe(technical)
    expect(el.textContent).toContain('0 actionable / 0 findings')
  } finally { act(() => root.unmount()); client.clear(); localStorage.clear() }
})

it('shows the person and revision behind a janitor vote without exposing its internal rule identity', () => {
 const step = { id: STEP, runId: RUN, stage: 'accept', name: 'accept', type: 'approval', status: 'pass', overrideBy: '', config: {}, evidence: { approver: 'the janitor', decision: 'approve', comment: 'the janitor under standing approval hidden-rule', decisions: [{ approver: 'the janitor', via: 'standing', decision: 'approve', standing: { rule: { kind: 'Dependency updates', scope: 'all projects' }, ruleId: 'hidden-rule', revision: 5, issuer: { name: 'Nina' } } }] } } as unknown as Step
 const html = renderToStaticMarkup(<QueryClientProvider client={newQueryClient()}><MemoryRouter><StepCard step={step} onChange={() => {}} /></MemoryRouter></QueryClientProvider>)
 expect(html).toContain('Dependency updates approval for all projects (revision 5), granted by Nina')
 expect(html).not.toContain('hidden-rule')
})
