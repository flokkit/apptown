import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import type { ChangeView, Run, RunDetail, Step } from '../../api'
import { Evidence, receiptOf, verificationSummary } from './Evidence'
import { claimOf, disputeOf } from './Disagreement'

const step = (status: Step['status'], overrideBy = '') => ({ type: 'tests', status, overrideBy } as Step)

describe('recorded verification', () => {
 it('distinguishes passing checks from overrides, skipped checks, and unfinished work', () => {
  expect(verificationSummary([step('pass'), step('pass', 'reviewer'), step('skipped'), step('pending'), step('error')]))
   .toBe('1 passed · 1 overridden · 1 need attention · 1 unfinished · 1 skipped')
 })
 it('does not present agent execution as a verification check', () => {
  expect(verificationSummary([{ type: 'agent/pi', status: 'pass' } as Step])).toContain('No verification checks')
 })
})

describe('not applicable', () => {
 it('counts a not-applicable step apart from passes', () => {
  expect(verificationSummary([step('pass'), step('not_applicable')])).toBe('1 passed · 1 not applicable')
 })
 it('names every not-applicable check and never counts it among the rules met', () => {
  const checks = [
   { block: 'b', version: 1, check: 'builds', evaluator: 'sh', verdict: 'pass' },
   { block: 'b', version: 1, check: 'go-vet-clean', evaluator: 'tool', verdict: 'not-applicable', detail: 'no part is built with go' },
   { block: 'b', version: 1, check: 'gofmt-clean', evaluator: 'tool', verdict: 'not-applicable', detail: 'no part is built with go' },
   { block: 'b', version: 1, check: 'no-volume', evaluator: 'sh', verdict: 'not-applicable', detail: 'no part is built from a Dockerfile' },
   { block: 'b', version: 1, check: 'readme', evaluator: 'sh', verdict: 'not-applicable', detail: 'x' },
  ]
  const s = { id: '00000000-0009-7000-8000-000000000000', type: 'guidance/conformance', name: 'guidance', status: 'pass', config: {}, overrideBy: '', evidence: {}, checkReading: { checks } } as unknown as Step
  const r = receiptOf(s)
  expect(r.sentence).toBe('1 of 1 rule met. Not applicable here: go-vet-clean, gofmt-clean, no-volume, readme.')
 })
 it('says why a scan or tests step is not applicable, and never that it passed', () => {
  const scan = { id: '00000000-000a-7000-8000-000000000000', type: 'scan', name: 'scan-image:trivy', status: 'not_applicable', config: {}, overrideBy: '', evidence: { notApplicable: 'the public part is a static site: there is no image' } } as unknown as Step
  expect(receiptOf(scan).result).toBe('not applicable')
  expect(receiptOf(scan).sentence).toBe('the public part is a static site: there is no image')
 })
})

describe('receipts', () => {
 it('reads an unfinished step of an ended run as never decided, not as waiting', () => {
  const approval = { id: '00000000-0001-7000-8000-000000000000', type: 'approval', name: 'accept', status: 'pending', config: { approvers: ['bls'] }, evidence: {}, overrideBy: '' } as unknown as Step
  expect(receiptOf(approval, true).result).toBe('waiting')
  expect(receiptOf(approval, false).result).toBe('never decided')
 })
 it('says who approved and in their words', () => {
  const s = { id: '00000000-0002-7000-8000-000000000000', type: 'approval', name: 'accept', status: 'pass', config: {}, evidence: { approver: 'bls', decision: 'approve', comment: 'fine', decisions: [{ approver: 'bls', decision: 'approve' }] }, overrideBy: '' } as unknown as Step
  const r = receiptOf(s)
  expect(r.result).toBe('approved')
  expect(r.sentence).toBe('Approved by bls: “fine”')
  expect(r.by).toBe('bls')
 })
 it("says an eval's start settled an approval in its copy, and names no person", () => {
  const s = { id: '00000000-0003-7000-8000-000000000000', type: 'approval', name: 'accept', status: 'pass', config: {}, evidence: { decision: 'approve', evalAuthorised: 'eval/7 started by bls', decisions: [] }, overrideBy: '' } as unknown as Step
  const r = receiptOf(s)
  expect(r.sentence).toBe("Eval-authorised, not a person's approval: eval/7 started by bls.")
  expect(r.by).toBe('eval-authorised')
 })
 it('keeps what main already fails, and what could not be compared with main, apart from what the change did not meet', () => {
  const s = { id: '00000000-0004-7000-8000-000000000000', type: 'guidance/conformance', name: 'guidance', status: 'pass', config: {}, overrideBy: '', evidence: {}, checkReading: { checks: [
   { check: 'tidy', verdict: 'pass', block: 'b', version: 1, evaluator: 'sh' },
   { check: 'front-page-answers', verdict: 'fail', block: 'b', version: 1, evaluator: 'runtime', undecided: true, note: "main's deployment runs 2222, not this change's base 1111" },
   { check: 'readme-plain', verdict: 'fail', block: 'b', version: 1, evaluator: 'llm', preexisting: true },
  ] } } as unknown as Step
  expect(receiptOf(s).sentence).toBe('1 of 3 rules met. 1 already failing on main. 1 undecided (not known whether this change introduced it).')
 })
 // Evidence the server could not read is unknown, never "no rules".
 it('says when a conformance step\'s check evidence could not be read', () => {
  const s = { id: '00000000-0006-7000-8000-000000000000', type: 'guidance/conformance', name: 'guidance', status: 'pass', config: {}, overrideBy: '', evidence: { checks: [{ check: 'tone', verdict: '' }] },
   checkReading: { unreadable: 'unreadable check evidence: check "tone": invalid check verdict: ""' } } as unknown as Step
  expect(receiptOf(s).sentence).toBe('Its check evidence could not be read.')
 })
 it("reads a review AppTown's file check failed as AppTown's failure, not a quote from the reviewer", () => {
  const s = { id: '00000000-0003-7000-8000-000000000000', type: 'agent/review', name: 'review', status: 'fail', config: {}, overrideBy: '', evidence: {
   reason: "AppTown's file check failed: 1 changed file the report doesn't mention", summary: "AppTown's file check failed: 1 changed file the report doesn't mention",
   reviewerVerdict: 'pass', reviewerSummary: 'looks good' } } as unknown as Step
  expect(receiptOf(s).sentence).toBe("AppTown's file check failed: 1 changed file the report doesn't mention; the reviewer itself passed it.")
 })
})

describe('receipts in plain words', () => {
 // The worker's internal address is plumbing: the receipt says where in
 // words, and the full record keeps the address.
 it('says a check ran on the worker, not at its internal address', () => {
  const s = { id: '00000000-0004-7000-8000-000000000000', type: 'tests', name: 'tests', status: 'pass', config: {}, overrideBy: '', evidence: {}, checkReading: { placement: { worker: 'https://10.150.0.3:8443' } } } as unknown as Step
  expect(receiptOf(s).by).toBe('on the worker')
  expect(receiptOf(s).by).not.toContain('10.150')
 })
 // A conformance check that passes with rules not met never reads as a
 // plain pass beside "not met".
 it('reads a conformance pass with unmet rules as passed with notes', () => {
  const checks = [{ check: 'a', verdict: 'pass' }, { check: 'readme-plain-tone', verdict: 'fail' }]
  const s = { id: '00000000-0005-7000-8000-000000000000', type: 'guidance/conformance', name: 'guidance-conformance', status: 'pass', config: {}, overrideBy: '', evidence: {}, checkReading: { checks } } as unknown as Step
  expect(receiptOf(s).result).toBe('passed with notes')
  expect(receiptOf(s).sentence).toBe('1 of 2 rules met. Not met, and not blocking: readme-plain-tone.')
  const clean = { ...s, checkReading: { checks: [checks[0]] } } as unknown as Step
  expect(receiptOf(clean).result).toBe('passed')
  const failed = { ...s, status: 'fail' } as unknown as Step
  expect(receiptOf(failed).sentence).toBe('1 of 2 rules met. Not met: readme-plain-tone.')
 })
 it('names a role approver in plain words', () => {
  const s = { id: '00000000-0006-7000-8000-000000000000', type: 'approval', name: 'approve-landing', status: 'pending', config: { approvers: ['role:project-approver'] }, evidence: {}, overrideBy: '' } as unknown as Step
  expect(receiptOf(s).by).toBe('needed from anyone with the project approver role')
 })
})

describe('the disagreement', () => {
 const detail = (steps: Partial<Step>[], artifacts: { kind: string; body: string }[] = []) => ({
  run: { id: '00000000-0007-7000-8000-000000000000', number: 7, currentStage: 'build' }, stages: ['build'], events: [], landingMeans: [],
  steps: steps.map((s, i) => ({ id: `00000000-${String(i + 1).padStart(4, '0')}-7000-8000-000000000000`, stage: 'build', config: {}, evidence: {}, overrideBy: '', ...s })),
  artifacts: artifacts.map((a, i) => ({ id: `00000000-${String(100 + i).padStart(4, '0')}-7000-8000-000000000000`, name: a.kind, ref: '', ...a })),
 } as unknown as RunDetail)
 it('lays out each failed verdict once, and the claim its reviewer audits', () => {
  const d = detail([
   { name: 'boot', type: 'preview', status: 'fail', evidence: { reason: 'build failed', logTail: 'same log' } },
   { name: 'tests', type: 'tests', status: 'fail', evidence: { reason: 'tests failed', logTail: 'same log' } },
   { name: 'review', type: 'agent/review', status: 'fail', config: { of: 'change-report' }, evidence: { summary: 'scope creep', findings: ['one', 'two'] } },
   { name: 'accept', type: 'approval', status: 'fail' },
   { name: 'scan', type: 'scan', status: 'fail', overrideBy: 'bls' },
  ], [{ kind: 'change-report', body: 'I did it' }, { kind: 'scan-report', body: '{}' }])
  const v = disputeOf(d)
  expect(v.map((x) => x.step.name)).toEqual(['boot', 'tests', 'review'])
  expect(v[0].log).toBe('same log')
  expect(v[1].log).toBeUndefined()
  expect(v[1].repeatOf).toBe('boot')
  expect(v[2].who).toBe('reviewer')
  expect(v[2].findings).toEqual(['one', 'two'])
  expect(claimOf(d)?.kind).toBe('change-report')
 })
 it("keeps AppTown's file check apart from a reviewer who passed the work", () => {
  const [v] = disputeOf(detail([{ name: 'review', type: 'agent/review', status: 'fail', evidence: {
   verdict: 'fail', reviewerVerdict: 'pass', reviewerSummary: 'all claims verified',
   summary: "AppTown's file check failed: 2 changed files the report doesn't mention", findings: ['unclaimed change: Gemfile, bin/rails'] } }]))
  expect(v.who).toBe("AppTown's file check")
  expect(v.text).toBe("AppTown's file check failed: 2 changed files the report doesn't mention")
  expect(v.reviewerPassed).toBe('all claims verified')
  expect(disputeOf(detail([{ name: 'review', type: 'agent/review', status: 'fail', evidence: { summary: 'scope creep' } }]))[0].who).toBe('reviewer')
 })
 it('names no claim when no review declares what it audits', () => {
  expect(claimOf(detail([{ name: 'r', type: 'agent/review', status: 'fail' }], [{ kind: 'change-report', body: 'x' }]))).toBeUndefined()
 })
})

// A finished run whose conformance evidence could not be read never folds
// into "all passed": the panel says so before anything is opened.
describe('a finished run with unreadable check evidence', () => {
 it('shows the unreadable receipt and does not count it as a pass', () => {
  const step = { id: '00000000-0006-7000-8000-000000000000', runId: '00000000-0002-7000-8000-000000000000', stage: 'check', name: 'guidance', type: 'guidance/conformance', status: 'pass', attempt: 1, config: {}, overrideBy: '',
   evidence: { checks: [{ check: 'tone', verdict: '' }] }, checkReading: { unreadable: 'unreadable check evidence: invalid check verdict: ""' } } as unknown as Step
  const run = { id: '00000000-0002-7000-8000-000000000000', number: 2, status: 'done', workflowName: 'checks', workflowVersion: 1 } as Run
  const view = { detail: { steps: [step] }, projectSlug: 'p' } as unknown as ChangeView
  const html = renderToStaticMarkup(<MemoryRouter><Evidence view={view} runs={[{ run, steps: [step] }]} workTab={() => '#'} /></MemoryRouter>)
  expect(html).not.toContain('all passed')
  expect(html).toContain('evidence unreadable')
  expect(html).toContain('Its check evidence could not be read.')
  expect(verificationSummary([step])).toBe('1 passed with unreadable evidence')
 })
})

// A tests receipt has three readings: counts with no skips (a plain pass),
// counts with skips, and counts not known. Only the first folds a finished
// run into "all passed"; the others show their note before anything is
// opened (soak finding 23).
describe('a finished run with a tests receipt', () => {
 const run = { id: '00000000-0002-7000-8000-000000000000', number: 2, status: 'done', workflowName: 'land', workflowVersion: 1 } as Run
 const tests = (evidence: object) => ({ id: '00000000-0008-7000-8000-000000000000', runId: run.id, stage: 'check', name: 'tests', type: 'tests', status: 'pass', statusLabel: 'passed', attempt: 1, config: {}, overrideBy: '',
  evidence: { command: 'go test -json ./... > "$APPTOWN_TEST_REPORT"', ...evidence } }) as unknown as Step
 const rendered = (s: Step) => renderToStaticMarkup(<MemoryRouter><Evidence view={{ detail: { steps: [s] }, projectSlug: 'p' } as unknown as ChangeView} runs={[{ run, steps: [s] }]} workTab={() => '#'} /></MemoryRouter>)
 it('folds when the report counted no skips', () => {
  const html = rendered(tests({ testCounts: { passed: 44, failed: 0, skipped: 0 } }))
  expect(html).toContain('all passed')
  expect(receiptOf(tests({ testCounts: { passed: 44, failed: 0, skipped: 0 } })).sentence).toContain('44 passed')
 })
 for (const [name, evidence, note] of [
  ['skips', { testCounts: { passed: 41, failed: 0, skipped: 3 }, skippedTests: ['ex/store TestImport'] }, '41 passed, 3 skipped'],
  ['no report', { testReport: 'the test command wrote no report to $APPTOWN_TEST_REPORT, so how many tests ran or were skipped is not known' }, 'test counts not reported'],
  ['an unreadable report', { testReport: 'the test report could not be read (…)', testReportUnreadable: 'line 3 of the report is not a go test -json event' }, 'test report unreadable: line 3 of the report is not a go test -json event'],
  ['a receipt from before reports', {}, 'test counts not reported'],
 ] as const) {
  it(`shows ${name} without folding`, () => {
   const html = rendered(tests(evidence))
   expect(html).not.toContain('all passed')
   expect(html).toContain('passed with notes')
   expect(html).toContain(note)
  })
 }
 it('reads a pass with no tests found by its recorded reason, with notes', () => {
  const s = { ...tests({}), evidence: { reason: 'no tests found — nothing to run', tree: 'abc' } } as unknown as Step
  expect(receiptOf(s).result).toBe('passed with notes')
  expect(receiptOf(s).sentence).toBe('no tests found — nothing to run')
 })
})

// A vote cast through a CLI token names the token beside the person; a
// vote from a signed-in session reads as before (soak finding 27).
describe('an approval cast through a token', () => {
 const approval = (via: string) => ({ id: '00000000-0009-7000-8000-000000000000', type: 'approval', name: 'approve-landing', status: 'pass', config: {}, overrideBy: '',
  evidence: { approver: 'bls', decision: 'approve', comment: 'delegated', decisions: [{ approver: 'bls', via, decision: 'approve' }] } }) as unknown as Step
 it('names the credential', () => {
  const r = receiptOf(approval('token-cli:claude-migrations-bls'))
  expect(r.sentence).toBe('Approved by bls through CLI token claude-migrations-bls: “delegated”')
  expect(r.by).toBe('bls through CLI token claude-migrations-bls')
 })
 it('says nothing more for a session', () => {
  expect(receiptOf(approval('session')).sentence).toBe('Approved by bls: “delegated”')
  expect(receiptOf(approval('session')).by).toBe('bls')
 })
})

it('retains the owner label when a raw passing step has unread evidence', () => {
 const s = { type: 'tests', status: 'pass', statusLabel: 'unknown', overrideBy: '', evidence: {}, checkReading: { unreadable: 'missing report' } } as unknown as Step
 expect(receiptOf(s).statusLabel).toBe('unknown')
})

it('shows an offered approval as waiting for you, independently of check labels', () => {
 const s = { type: 'approval', status: 'pending', statusLabel: 'waiting for checks', config: {}, evidence: {} } as unknown as Step
 expect(receiptOf(s, true, true).statusLabel).toBe('waiting for you')
 expect(receiptOf(s, true, true).sentence).toBe('')
 expect(receiptOf(s, true, false).statusLabel).toBe('waiting for checks')
})

 it.each(['in_progress', 'landing', 'shipped', 'abandoned'] as const)('records absent delivery for a closed change: %s', state => {
  const view = { change: { state }, evidence: { targets: [], merged: false, releases: [] } } as unknown as ChangeView
  const html = renderToStaticMarkup(<MemoryRouter><Evidence view={view} runs={[]} workTab={() => '#'} /></MemoryRouter>)
  expect(html.includes('Not merged and not released.')).toBe(state === 'shipped' || state === 'abandoned')
 })

it('names script checks individually and makes each receipt the disclosure', () => {
 const steps = ['tests', 'style'].map((name, i) => ({ id: String(i), name, type: 'script', status: 'fail', statusLabel: 'failed', evidence: { command: 'private-command' }, config: {} } as unknown as Step))
 expect(steps.map(s => receiptOf(s).title)).toEqual(['Tests', 'Style'])
 const view = { change: { state: 'in_progress' } } as ChangeView
 const html = renderToStaticMarkup(<MemoryRouter><Evidence view={view} runs={[{ run: { id: 'run', status: 'running' } as Run, steps }]} workTab={() => '#record'} /></MemoryRouter>)
 expect(html).toMatch(/<details[^>]*id="step-0"[^>]*><summary/)
 expect(html).not.toContain('<summary>Details</summary>')
 expect(html).toContain('Kind: Script')
})
