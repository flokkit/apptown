import { waitingTone } from '../../Status'
import type { ApprovalConfig, ChangeView, Step } from '../../api'
import type { NextAct, SentBack } from '../../wire.gen'

// Workflow supplies the state, holding identity and reader eligibility.
// This adapter keeps button wording and placement beside that shared answer.

export type Tone = 'warn' | 'busy' | 'bad' | 'ok' | 'unknown' | 'neutral'

// The one primary action, by name. 'none' means the right move is to wait,
// and the panel says who or what it is waiting on.
export type Primary =
  | 'approve'          // decide a pending approval (approve, or reject and send back)
  | 'take-to-chat'     // a stuck step: the same agent continues with a person steering
  | 'chat'             // open the project's chat on this change
  | 'pick-up'          // run an interrupted step again
  | 'try-again'        // run a failed step again, the only way on once the landing merged
  | 'send-back'        // send the work back to its agent with a note
  | 'restart-landing'  // land again under the current rules
  | 'continue'         // advance a stage that has everything it needs
  | 'land'             // start landing finished work
  | 'open-release'     // open what shipped
  | 'override'
  | 'open-decision'    // a decision that lives in the work (a called workflow's approval)
  | 'none'

export type NextStep = {
  tone: Tone
  // A short word for the stamp beside the headline.
  stamp: string
  headline: string
  sentence?: string
  // Who or what the change waits on, when it waits.
  waitingOn?: string
  primary: Primary
  // The step the state is about (the approval, the stuck check, the running step).
  step?: Step
  // A person's (or a check's) words the agent is reworking from.
  recovery?: boolean
  sentBack?: SentBack
}

// offered: the act AppTown offers, on stepId when given (Work's next acts:
// the run's on detail.next, the change's on view.next). What can be done is
// AppTown's answer; the page only words and places it.
export const offered = (acts: NextAct[] | undefined, act: string, stepId?: string | null) =>
  (acts ?? []).find((a) => a.act === act && (stepId == null || a.stepId === stepId))
// Findings are the reviewer's structured objections. Without them, retain the
// complete recorded text, including positive observations before an objection.
export function reviewText(s: Step): string {
  if (s.type !== 'agent/review') return s.evidence.reason || 'Read what the check found.'
  return [s.evidence.findings?.join('\n'), (s.evidence.reason || s.evidence.summary)?.replace(/^reviewer rejected:\s*/i, '')].filter(Boolean).join('\n\n') || 'No review conclusion recorded.'
}

// rerunLabel: how a step run again reads — a check that reads a record is
// checked again, anything else is tried again.
export const rerunLabel = (s: Step) => ['artifact/require', 'adr/require', 'scm/merged'].includes(s.type) ? 'Re-check' : 'Try again'
export const interrupted = (s: Step) => (s.evidence?.reason ?? '').includes('orphaned by apptownd restart')
// Who may decide an approval, in plain words from the workflow's approver
// list: people by name, group:name as "anyone in name", role:name as
// "anyone with the name role" (hyphens spoken as spaces); else nobody named.
export function approversOf(step: Step): string {
  const cfg = step.config as ApprovalConfig
  const list = (cfg.approvers ?? []).map((a) => a.startsWith('group:') ? `anyone in ${a.slice(6)}`
    : a.startsWith('role:') ? `anyone with the ${a.slice(5).replace(/-/g, ' ')} role` : a)
  if (list.length === 0) return 'a person allowed to approve it'
  return list.length === 1 ? list[0] : `${list.slice(0, -1).join(', ')} or ${list[list.length - 1]}`
}

// changeChip: the words and tone of the change's chip beside its title. A
// change waiting on an approval says so (and says "your" only to someone who
// may give it); otherwise the change's state.
export function changeChip(view: ChangeView): { text: string; cls: string } {
  return { text: view.waitingOn.says, cls: waitingTone(view.waitingOn.tone) }
}

// Button wording and placement follow the owner's holding identity and acts.
export function nextStep(view: ChangeView): NextStep {
  const w = view.waitingOn, d = view.detail
  const step = d?.steps.find(s => s.id === w.step)
  const base: NextStep = { tone: w.tone as Tone, stamp: w.says, headline: w.says, primary: 'none', step, sentBack: d?.sentBack ?? undefined }
  if (w.kind === 'chat') return { ...base, primary: 'chat', sentence: d?.ending || view.stateDetail }
  if (w.kind === 'closed') {
    const release = d?.artifacts.some(a => a.kind === 'release' && a.ref)
    return { ...base, primary: release ? 'open-release' : 'none', sentence: view.stateDetail }
  }
  if (w.kind === 'approval') {
    return { ...base, primary: step ? 'approve' : 'open-decision', waitingOn: w.you === 'yes' ? 'you' : w.who.map(x => x.name).join(' or '), sentence: view.stateDetail }
  }
  if (w.kind === 'ready') return { ...base, primary: offered(d?.next, 'advance') ? 'continue' : 'none' }
  if (w.kind !== 'check' && w.kind !== 'person') return base
  const events = d?.events ?? []
  const lastRetry = events.map(e => e.kind).lastIndexOf('step.retried')
  const rounds = events.slice(lastRetry + 1).filter(e => e.kind === 'step.rework').length
  const chat = offered(d?.next, 'rework')
  const primary: Primary = offered(d?.next, 'restart-landing') ? 'restart-landing'
    : rounds >= 3 && chat ? 'take-to-chat'
    : offered(view.next, 'send-back') ? 'send-back'
    : step && offered(d?.next, 'retry', step.id) ? interrupted(step) ? 'pick-up' : 'try-again'
    : step && offered(d?.next, 'override', step.id) ? 'override'
    : chat ? 'take-to-chat' : 'none'
  return { ...base, primary, recovery: true, sentence: step ? reviewText(step) : view.stateDetail }
}

// An offered decision waits for the person, even when the stored pending
// step label still describes checks. Other pending approvals keep that label.
export function decisionStatus(step: Step, awaitingDecision = false): Step['statusLabel'] {
  return step.type === 'approval' && step.status === 'pending' && awaitingDecision ? 'waiting for you' : step.statusLabel
}

// Pending work in a later stage does not hold an earlier decision. Running
// work anywhere does: accepting must wait for the result being produced.
export function checksRunning(steps: Step[], stage?: string): boolean {
  return steps.some(s => s.type !== 'approval' && (s.status === 'running' || (s.status === 'pending' && (!stage || s.stage === stage)
    && ['tests', 'scan', 'script', 'agent/pi', 'agent/review', 'guidance/conformance', 'preview', 'workflow/call'].includes(s.type))))
}
