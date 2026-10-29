import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import type { ChangeView, Step } from '../../api'
import { newQueryClient } from '../../data'
import { changeChip, nextStep, Primary } from './next'
import { StatePanel } from './StatePanel'

// The change page's head says where a change is and offers ONE primary act
// for whoever is looking. Each state the server can report gets its words
// and its act; a state the page cannot read is "not known", never success.

// Record ids are UUIDv7 strings; a fixture's are made from a small number.
const uuid = (n: number) => `00000000-${String(n).padStart(4, '0')}-7000-8000-000000000000`
const RUN = uuid(7)
let nextId = 1
const step = (over: Partial<Step> & { type: Step['type'] }): Step => ({
  id: uuid(100 + nextId++), runId: RUN, stage: 'build', name: over.type, status: 'pass', config: {}, evidence: {},
  overrideBy: '', overrideReason: '', startedAt: '2026-10-01T10:00:00Z', finishedAt: '2026-10-01T10:01:00Z', ...over,
} as Step)

type Act = { act: string; stepId: string | null; step: string }
type Opts = {
  change?: 'in_progress' | 'landing' | 'shipped' | 'abandoned'; closedAs?: string; stateLabel?: string
  state?: string; stateDetail?: string; runStatus?: 'running' | 'done' | 'abandoned'
  steps?: Step[]; events?: { kind: string; payload: Record<string, unknown> }[]
  landingMeans?: string[]; noRun?: boolean; next?: Act[]; changeNext?: Act[]; landingQueued?: boolean
  artifacts?: { kind: string; ref: string }[]; releases?: { number: number; deployment: string; state: string; serving: boolean }[]; merged?: boolean
}
const view = (o: Opts): ChangeView => ({
  change: { id: uuid(9), number: 9, title: 'Say the coin is fair', state: o.change ?? 'in_progress', closedAs: o.closedAs, stateLabel: o.stateLabel ?? (o.change ?? 'in_progress').replace('_', ' '), body: '', branch: 'apptown/change-9', createdAt: '2026-10-01T09:00:00Z', editRevision: 0 },
  projectSlug: 'coin-flip', state: o.state ?? '', stateDetail: o.stateDetail ?? '',
  evidence: { targets: [], merged: !!o.merged, releases: o.releases ?? [] },
  runs: [], commits: [], adrs: [], editing: 0, report: '', diffstat: '1 file changed',
  committedDiff: { status: 'changed' }, next: o.changeNext ?? [],
  detail: o.noRun ? null : {
    run: { id: RUN, number: 7, workflowName: o.change === 'landing' ? 'land' : 'quick-change', workflowVersion: 3, status: o.runStatus ?? 'running', currentStage: 'build', createdAt: '' },
    stages: ['build'], steps: o.steps ?? [], events: (o.events ?? []).map((e, i) => ({ id: uuid(200 + i), runId: RUN, at: '', ...e })),
    artifacts: (o.artifacts ?? []).map((a, i) => ({ id: uuid(300 + i), runId: RUN, name: a.kind, body: '', createdAt: '', ...a })),
    landingMeans: o.landingMeans ?? [], next: o.next ?? [], landingQueued: o.landingQueued,
  },
} as unknown as ChangeView)

const render = (v: ChangeView, stale = false) => renderToStaticMarkup(
  <MemoryRouter><QueryClientProvider client={newQueryClient()}>
    <StatePanel view={v} onChange={() => {}} workTab={() => '/changes/9?tab=work'} reading={{ data: {}, refreshFailed: stale, answeredAt: 0, err: '', reason: stale ? 'network down' : '', loading: false, denied: false }} />
  </QueryClientProvider></MemoryRouter>)
// The panel's primary buttons and links, by their words.
const primaries = (html: string) => [...html.matchAll(/class="(?:btn )?primary"[^>]*>([^<]*)</g)].map((m) => m[1])

// AppTown's offer of an approval step, as the run snapshot carries it.
const offer = (s: Step) => [{ act: 'approve', stepId: s.id, step: s.name }]
const retry = (s: Step) => [{ act: 'retry', stepId: s.id, step: s.name }]
const act = (a: string) => [{ act: a, stepId: null, step: '' }]
const trivy = step({ type: 'scan', name: 'scan-image:trivy', status: 'error', evidence: { reason: 'the report is over the limit' } })
const orphaned = step({ type: 'tests', name: 'tests', status: 'error', evidence: { reason: 'orphaned by apptownd restart' } })
const law = step({ type: 'policy/current', name: 'law', status: 'needs_human', evidence: { reason: 'the review policy changed' } })
const accept = step({ type: 'approval', name: 'accept', status: 'pending', config: { approvers: ['bls'], means: 'I glanced.' } })

const cases: { name: string; v: ChangeView; primary: Primary; headline: RegExp; button?: string; waitingOn?: RegExp }[] = [
  {
    name: 'the agent is working',
    v: view({ state: 'agent working', stateDetail: 'apply-change', steps: [step({ type: 'agent/pi', name: 'apply-change', status: 'running' })] }),
    primary: 'none', headline: /^The agent is working: apply-change$/, waitingOn: /the agent/,
  },
  {
    name: 'waiting on a person to approve the landing',
    v: view({ state: 'waiting on you', landingMeans: ['I answer for this landing.'], steps: [accept], next: offer(accept) }),
    primary: 'approve', headline: /^Waiting for approval to land$/, button: 'Approve and land', waitingOn: /^bls$/,
  },
  {
    name: 'sent back by a person, the agent reworking it',
    v: view({
      state: 'agent working', steps: [step({ type: 'agent/pi', name: 'apply-change', status: 'running', config: { sendback: 'use crypto/rand' } })],
      events: [{ kind: 'step.sendback', payload: { by: 'bls', note: 'use crypto/rand', step: 'apply-change' } }],
    }),
    primary: 'none', headline: /^Sent back: the agent is reworking it$/, waitingOn: /the agent/,
  },
  {
    name: 'a rejected approval stops the stage',
    v: view({ state: 'blocked', changeNext: act('send-back'), steps: [step({ type: 'approval', name: 'accept', status: 'fail', evidence: { approver: 'bls', decision: 'reject', comment: 'not this way' } })] }),
    primary: 'send-back', headline: /^Rejected: bls sent “accept” back$/, button: 'Send it back with a note',
  },
  {
    name: 'landing queued',
    v: view({ change: 'landing', state: 'queued', stateDetail: 'tests — waiting for a slot on docker, one ahead' }),
    primary: 'none', headline: /^Queued to land$/, waitingOn: /free agent slot/,
  },
  {
    name: 'the landing starting with the change’s acceptance',
    v: view({ change: 'landing', state: 'working', stateDetail: "starting the landing with the change's acceptance" }),
    primary: 'none', headline: /^Starting the landing$/, waitingOn: /AppTown/,
  },
  {
    name: 'landing blocked by a gate',
    v: view({ change: 'landing', state: 'blocked', changeNext: act('send-back'), steps: [step({ type: 'scan', name: 'scan-image:trivy', status: 'fail', evidence: { reason: '2 high findings' } })] }),
    primary: 'send-back', headline: /^Landing stopped: “scan-image:trivy” did not pass$/, button: 'Send it back with a note',
  },
  {
    name: 'landing stopped after its merge',
    v: view({ change: 'landing', state: 'blocked', merged: true, steps: [trivy], next: retry(trivy) }),
    primary: 'try-again', headline: /^Landing stopped after the merge: “scan-image:trivy” did not pass$/, button: 'Try again',
  },
  {
    name: 'stuck while working',
    v: view({ state: 'blocked', steps: [step({ type: 'agent/review', name: 'review', status: 'fail', evidence: { summary: 'out of scope' } })] }),
    primary: 'take-to-chat', headline: /^Blocked: “review” did not pass$/, button: 'Take it to chat',
  },
  {
    name: 'interrupted by a restart',
    v: view({ state: 'blocked', steps: [orphaned], next: retry(orphaned) }),
    primary: 'pick-up', headline: /interrupted$/, button: 'Pick it back up',
  },
  {
    name: 'finished work not yet landed',
    v: view({ runStatus: 'done', changeNext: act('land') }),
    primary: 'land', headline: /has not landed/, button: 'Check and land', waitingOn: /someone to land it/,
  },
  {
    name: 'nothing has run',
    v: view({ noRun: true, stateDetail: 'No workflow has started yet.' }),
    primary: 'chat', headline: /^Nothing is running/, button: 'Chat about it',
  },
  {
    name: 'shipped',
    v: view({ change: 'shipped', runStatus: 'done', merged: true, releases: [{ number: 21, deployment: 'main', state: 'released', serving: true }], artifacts: [{ kind: 'release', ref: 'https://coin.example' }] }),
    primary: 'open-release', headline: /^Shipped$/, button: 'Open the release ↗',
  },
  {
    name: 'abandoned',
    v: view({ change: 'abandoned', runStatus: 'abandoned', state: 'abandoned', stateDetail: 'superseded by #12' }),
    primary: 'none', headline: /^Abandoned$/,
  },
  {
    name: 'closed by AppTown: nothing to change',
    v: view({ change: 'abandoned', closedAs: 'nothing-to-change', stateLabel: 'closed: nothing to change', runStatus: 'abandoned', state: 'abandoned', stateDetail: 'Nothing to update: the project declares no dependencies' }),
    primary: 'none', headline: /^Closed: nothing to change$/,
  },
  {
    name: 'closed on an accepted record, nothing landed',
    v: view({ change: 'shipped', runStatus: 'done' }),
    primary: 'none', headline: /^Finished$/,
  },
  {
    name: 'a state the page does not know',
    v: view({ state: 'mystery' }),
    primary: 'none', headline: /not known/,
  },
  {
    // Ready says what the run can do, never that every check was read and
    // passed: a pass whose evidence could not be read is still ready.
    name: 'ready to continue, with a check whose evidence could not be read',
    v: view({ state: 'ready to advance', next: act('advance'), steps: [step({ type: 'guidance/conformance', name: 'guidance', checkReading: { unreadable: 'unreadable check evidence' } })] }),
    primary: 'continue', headline: /^Ready to continue from “build”$/, button: 'Continue',
  },
  // What AppTown does not offer, the page does not offer.
  {
    name: 'accepted work whose landing AppTown queued',
    v: view({ runStatus: 'done', landingQueued: true, state: 'queued', stateDetail: 'landing' }),
    primary: 'none', headline: /^Queued to land$/, waitingOn: /AppTown/,
  },
  {
    name: 'finished work AppTown offers no landing for',
    v: view({ runStatus: 'done' }),
    primary: 'none', headline: /run finished$/,
  },
  {
    name: 'a merged landing whose step AppTown will not run again',
    v: view({ change: 'landing', state: 'blocked', merged: true, steps: [trivy] }),
    primary: 'none', headline: /^Landing stopped after the merge/,
  },
  {
    name: 'a rejection AppTown will not send back',
    v: view({ state: 'blocked', steps: [step({ type: 'approval', name: 'accept', status: 'fail', evidence: { approver: 'bls', decision: 'reject' } })] }),
    primary: 'take-to-chat', headline: /^Rejected/, button: 'Take it to chat',
  },
  {
    name: 'a landing whose rules changed',
    v: view({ change: 'landing', state: 'blocked', steps: [law], next: act('restart-landing'), changeNext: act('send-back') }),
    primary: 'restart-landing', headline: /^The rules changed/, button: 'Restart landing',
  },
  {
    name: 'a stopped rules check AppTown offers no restart for',
    v: view({ change: 'landing', state: 'blocked', steps: [law], changeNext: act('send-back') }),
    primary: 'send-back', headline: /^Landing stopped/, button: 'Send it back with a note',
  },
  {
    name: 'a stage that cannot finish as recorded',
    v: view({ state: 'blocked', stateDetail: 'AppTown cannot judge this stage: no gate' }),
    primary: 'none', headline: /can't finish/,
  },
  {
    name: 'ready to advance, but AppTown offers no advance',
    v: view({ state: 'ready to advance' }),
    primary: 'none', headline: /^Working$/,
  },
  {
    name: 'an interrupted step AppTown will not run again',
    v: view({ state: 'blocked', steps: [orphaned] }),
    primary: 'take-to-chat', headline: /interrupted$/, button: 'Take it to chat',
  },
]

describe('the change head', () => {
  it.each(cases.map((c) => [c.name, c] as const))('%s: one state, one next action', (_, c) => {
    const n = nextStep(c.v)
    expect(n.primary).toBe(c.primary)
    expect(n.headline).toMatch(c.headline)
    if (c.waitingOn) expect(n.waitingOn ?? '').toMatch(c.waitingOn)
    const html = render(c.v)
    const p = primaries(html)
    // At most one primary act on the panel, and it is the right one.
    expect(p.length).toBeLessThanOrEqual(1)
    if (c.button) expect(p).toEqual([c.button])
    else expect(p).toEqual([])
  })

  it('says the sender and their words when work was sent back', () => {
    const html = render(cases[2].v)
    expect(html).toContain('bls sent it back')
    expect(html).toContain('use crypto/rand')
  })

  it('names who an approval waits on and what approving means', () => {
    const html = render(cases[1].v)
    expect(html).toContain('Waiting on: <strong>bls</strong>')
    expect(html).toContain('I glanced.')
    expect(html).toContain('I answer for this landing.')
  })

  it('shows an answer whose refresh failed as not current, never as plain success', () => {
    const html = render(cases.find((c) => c.name === 'shipped')!.v, true)
    expect(html).toContain('tone-unknown')
    expect(html).toContain('not current')
    expect(html).toContain('network down')
    expect(html).not.toContain('tone-ok')
  })

  // While no run goes, a live chat is where the change's work happens: the
  // page says the agent is working there, not "nothing is running".
  it('says the agent is working in the chat when a live chat has the turn', () => {
    const idle = view({ noRun: true, stateDetail: 'No workflow has started yet.' })
    const working = nextStep(idle, { chat: 'working' })
    expect(working.headline).toBe('The agent is working in the chat')
    expect(working.waitingOn).toBe('the agent')
    expect(working.primary).toBe('chat')
    expect(nextStep(idle, { chat: 'handed-back' }).headline).toBe('The agent answered in the chat')
    expect(nextStep(view({ runStatus: 'abandoned' }), { chat: 'working' }).headline).toBe('The agent is working in the chat')
    // No live chat known: the old words, never a claim about a chat.
    expect(nextStep(idle).headline).toMatch(/^Nothing is running/)
    expect(nextStep(idle, { chat: '' }).headline).toMatch(/^Nothing is running/)
  })

  // "Waiting on: you" only when the server checked the viewer may decide;
  // otherwise the approvers in plain words. What approving does is said
  // once, in the workflow's declared meaning, not again by the panel.
  it('says the approval waits on you only when you may give it, and its meaning once', () => {
    const approval = step({ type: 'approval', name: 'approve-landing', status: 'pending', config: { approvers: ['role:project-approver'], means: 'I answer for this landing.' } })
    const waiting = view({ change: 'landing', state: 'waiting on you', steps: [approval], next: offer(approval) })
    const theirs = nextStep(waiting)
    expect(theirs.waitingOn).toBe('anyone with the project approver role')
    expect(theirs.stamp).toBe('waiting for approval')
    expect(theirs.sentence).toBeUndefined()
    const mine = nextStep({ ...waiting, approval: { stepId: approval.id, viewer: 'yes' } } as ChangeView)
    expect(mine.waitingOn).toBe('you')
    expect(mine.stamp).toBe('waiting for your approval')
    expect(mine.headline).toBe('Waiting for your approval to land')
    for (const other of [{ stepId: approval.id, viewer: 'no' }, { stepId: approval.id, viewer: 'unknown' }, { stepId: uuid(999), viewer: 'yes' }]) {
      expect(nextStep({ ...waiting, approval: other } as ChangeView).waitingOn).not.toBe('you')
    }
    const html = render({ ...waiting, approval: { stepId: approval.id, viewer: 'yes' } } as ChangeView)
    expect(html).toContain('Waiting on: <strong>you</strong>')
    expect(html.match(/I answer for this landing\./g)?.length).toBe(1)
    expect(html).not.toContain('Approving lands it')
    expect(changeChip({ ...waiting, approval: { stepId: approval.id, viewer: 'yes' } } as ChangeView)).toEqual({ text: 'waiting for your approval', cls: 'pending' })
    expect(changeChip(view({ change: 'landing', state: 'working' })).text).toBe('landing')
  })

  // A pending approval AppTown does not offer is not the page's to put
  // in front of anyone: the page says a person is awaited, without a button.
  it('offers only the approval AppTown offers', () => {
    const second = step({ type: 'approval', name: 'sign-off', status: 'pending' })
    const offered = nextStep(view({ state: 'waiting on you', steps: [accept, second], next: offer(second) }))
    expect(offered.primary).toBe('approve')
    expect(offered.step?.id).toBe(second.id)
    expect(nextStep(view({ state: 'waiting on you', steps: [accept] })).primary).toBe('open-decision')
  })

  // After the merge AppTown refuses a send-back, a chat and abandoning
  // the change: the panel offers none of them, only the step again.
  it('offers nothing AppTown refuses once the landing has merged', () => {
    const merged = cases.find((c) => c.name === 'landing stopped after its merge')!.v
    // A step a restart interrupted after the merge is the same: run it again.
    const cut = step({ type: 'scan', name: 'scan-image:trivy', status: 'error', evidence: { reason: 'orphaned by apptownd restart' } })
    const interrupted = view({ change: 'landing', state: 'blocked', merged: true, steps: [cut], next: retry(cut) })
    expect(nextStep(interrupted).primary).toBe('try-again')
    for (const v of [merged, interrupted]) {
      const html = render(v)
      for (const refused of ['Send it back', 'Take it to chat', 'Abandon']) expect(html).not.toContain(refused)
      expect(html).toContain('Override or see the full record')
      expect(primaries(html)).toEqual(['Try again'])
    }
    const before = render(cases.find((c) => c.name === 'landing blocked by a gate')!.v)
    expect(before).toContain('Take it to chat')
  })

  // A merge check that read no merge needs a person; AppTown offers to
  // read it again, and the panel words it as a check, quietly. Without the
  // offer there is no such button.
  it('offers a re-check of a merge check only as AppTown offers it', () => {
    const merged = step({ type: 'scm/merged', name: 'merged', status: 'needs_human', evidence: { reason: 'not merged yet' } })
    expect(render(view({ state: 'blocked', steps: [merged], next: retry(merged) }))).toContain('>Re-check<')
    expect(render(view({ state: 'blocked', steps: [merged] }))).not.toContain('Re-check')
    expect(render(view({ state: 'blocked', steps: [trivy] }))).not.toContain('Try again')
  })

  it('keeps the abandonment reason on the first screen', () => {
    expect(render(cases.find((c) => c.name === 'abandoned')!.v)).toContain('superseded by #12')
  })

  it('names a nothing-to-change close in the chip, and an abandonment as one', () => {
    const closed = cases.find((c) => c.name === 'closed by AppTown: nothing to change')!.v
    expect(changeChip(closed).text).toBe('closed: nothing to change')
    expect(render(closed)).toContain('the project declares no dependencies')
    expect(changeChip(cases.find((c) => c.name === 'abandoned')!.v).text).toBe('abandoned')
  })
})
