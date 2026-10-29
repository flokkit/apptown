import { PRODUCT } from '../../brand'
import type { ApprovalConfig, ChangeView, Step } from '../../api'
import type { NextAct, SentBack } from '../../wire.gen'
import { chipFor, closedNothingToChange } from '../../states'

// What a change is doing, said once, with the one next action for whoever
// is looking. The page's state panel renders this; the tests read it. It is
// derived from the change page's own payload (the server's activity words,
// its run and steps), never stored and never guessed: where the payload
// does not say, the answer is "not known".

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
  sentBack?: SentBack
}

// offered: the act AppTown offers, on stepId when given (Work's next acts:
// the run's on detail.next, the change's on view.next). What can be done is
// AppTown's answer; the page only words and places it.
export const offered = (acts: NextAct[] | undefined, act: string, stepId?: string | null) =>
  (acts ?? []).find((a) => a.act === act && (stepId == null || a.stepId === stepId))
// rerunLabel: how a step run again reads — a check that reads a record is
// checked again, anything else is tried again.
export const rerunLabel = (s: Step) => ['artifact/require', 'adr/require', 'scm/merged'].includes(s.type) ? 'Re-check' : 'Try again'
export const interrupted = (s: Step) => (s.evidence?.reason ?? '').includes('orphaned by apptownd restart')
const failed = (s: Step) => s.status === 'fail' || s.status === 'error' || s.status === 'needs_human'
const KNOWN = ['waiting on you', 'blocked', 'agent working', 'working', 'queued', 'ready to advance']

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

// viewerMayApprove: the server checked the viewer against this approval's
// rules and they may decide it. A refusal, an unread answer or an answer
// about another step is not "you".
export function viewerMayApprove(view: ChangeView, step: Step): boolean {
  return view.approval?.stepId === step.id && view.approval.viewer === 'yes'
}

// changeLanded: whether a change's work reached main or a release, which
// is what makes a closed change shipped rather than finished.
export function changeLanded(view: ChangeView): boolean {
  return !!view.evidence?.merged || (view.evidence?.releases ?? []).length > 0
}

// changeChip: the words and tone of the change's chip beside its title. A
// change waiting on an approval says so (and says "your" only to someone who
// may give it); otherwise the change's state.
export function changeChip(view: ChangeView): { text: string; cls: string } {
  const next = view.change.state === 'landing' || view.change.state === 'in_progress' ? nextStep(view) : undefined
  if (next?.primary === 'approve') return { text: next.stamp, cls: 'pending' }
  return { text: view.change.stateLabel, cls: chipFor(view.change.state) }
}

// isLandingApproval: the approval whose yes lands the change — the one in a
// landing run, or the acceptance in a working run's last stage that the
// landing will take (the server sends landingMeans only then).
export function isLandingApproval(view: ChangeView, step: Step): boolean {
  if (step.type !== 'approval') return false
  if (view.change.state === 'landing') return true
  const d = view.detail
  if (!d) return false
  return step.stage === d.stages[d.stages.length - 1] && d.landingMeans.length > 0
}

// Around: what the page knows beside the change's own payload. chat is
// the turn of a live chat on this change, from the project's chat list:
// 'working' while its agent has the turn, 'handed-back' once it answered,
// undefined when no live chat is known (none, or the list could not be read).
export type Around = { chat?: string }

// inChat: while no run is going, a live chat is where the work is; the
// panel says so instead of "nothing is running".
function inChat(around: Around): NextStep | undefined {
  if (around.chat === 'working') {
    return {
      tone: 'busy', stamp: 'agent working', headline: 'The agent is working in the chat',
      sentence: 'What it commits is saved to this change.', waitingOn: 'the agent', primary: 'chat',
    }
  }
  if (around.chat === 'handed-back') {
    return {
      tone: 'neutral', stamp: 'in the chat', headline: 'The agent answered in the chat',
      sentence: 'It is waiting for your next message there; what it commits is saved to this change.', primary: 'chat',
    }
  }
  return undefined
}

export function nextStep(view: ChangeView, around: Around = {}): NextStep {
  const ch = view.change
  const d = view.detail
  if (ch.state === 'abandoned' && closedNothingToChange(ch.closedAs)) {
    return { tone: 'neutral', stamp: 'closed', headline: 'Closed: nothing to change', sentence: view.stateDetail || `${PRODUCT} found nothing to change.`, primary: 'none' }
  }
  if (ch.state === 'abandoned') {
    return { tone: 'neutral', stamp: 'abandoned', headline: 'Abandoned', sentence: view.stateDetail || 'No reason was recorded.', primary: 'none' }
  }
  if (ch.state === 'shipped') {
    const rel = view.evidence?.releases ?? []
    const last = rel[rel.length - 1]
    const merged = view.evidence?.merged
    const sentence = last
      ? `${merged ? 'Merged, and r' : 'R'}eleased as release #${last.number} to ${last.deployment}.`
      : merged ? 'Merged. No release is recorded for this change.' : 'Finished. Nothing was merged or released for this change.'
    const releaseArt = d ? [...d.artifacts].reverse().find((a) => a.kind === 'release' && a.ref) : undefined
    // A change closed on an accepted record (research, a spec) lands
    // nothing: it finished, it did not ship.
    const headline = changeLanded(view) ? 'Shipped' : 'Finished'
    return { tone: 'ok', stamp: headline.toLowerCase(), headline, sentence, primary: releaseArt ? 'open-release' : 'none' }
  }
  if (!d) {
    if (ch.state === 'landing') {
      return { tone: 'unknown', stamp: 'not known', headline: 'Landing, but no landing run was read', sentence: view.stateDetail, primary: 'none' }
    }
    return inChat(around) ?? {
      tone: 'neutral', stamp: 'idle', headline: 'Nothing is running on this change',
      sentence: view.stateDetail || 'No workflow has started yet.', primary: 'chat',
    }
  }
  const run = d.run
  if (run.status !== 'running') {
    if (ch.state === 'landing') {
      return { tone: 'unknown', stamp: 'not known', headline: 'Landing, but its run is not running', sentence: view.stateDetail, primary: 'none' }
    }
    if (run.status === 'done' && d.landingQueued) {
      return {
        tone: 'neutral', stamp: 'queued', headline: 'Queued to land',
        sentence: 'The work was accepted; its landing starts on its own.', waitingOn: PRODUCT, primary: 'none',
      }
    }
    if (run.status === 'done' && offered(view.next, 'land')) {
      return {
        tone: 'warn', stamp: 'ready to land', headline: 'The work is done and has not landed',
        sentence: `The ${run.workflowName} run finished. Check and land runs the landing checks, asks for approval, then merges and ships.`,
        waitingOn: 'someone to land it', primary: 'land',
      }
    }
    if (run.status === 'done') {
      return { tone: 'neutral', stamp: 'done', headline: `The ${run.workflowName} run finished`, sentence: view.stateDetail, primary: 'none' }
    }
    return inChat(around) ?? {
      tone: 'neutral', stamp: 'stopped', headline: 'The last attempt was stopped',
      sentence: view.stateDetail || 'The change stays open; nothing was deleted.', primary: 'chat',
    }
  }

  const stage = d.steps.filter((s) => s.stage === run.currentStage)
  const landing = ch.state === 'landing'
  const state = view.state
  if (!KNOWN.includes(state)) {
    return {
      tone: 'unknown', stamp: 'not known', headline: 'What this run is doing is not known',
      sentence: view.stateDetail || `${PRODUCT} did not say what the run is doing.`, primary: 'none',
    }
  }
  if (state === 'waiting on you') {
    // The approval to decide is the one AppTown offers (its next act),
    // never one the page picks from the steps.
    const offered = d.next.find((a) => a.act === 'approve')
    const approval = offered && stage.find((s) => s.id === offered.stepId)
    if (approval?.type === 'approval') {
      const toLand = isLandingApproval(view, approval)
      const ev = approval.evidence
      const quorum = (ev.quorum ?? 0) > 1 ? `${ev.approvals ?? 0} of ${ev.quorum} approvals so far.` : ''
      const before = ev.lastRejection ? `Sent back before by ${ev.lastRejection.approver}: “${ev.lastRejection.comment}”.` : ''
      // What approving does is said once, in the workflow's declared
      // meaning beside the decision; the panel adds a sentence only when
      // the workflow declares none.
      const declared = !!(approval.config as ApprovalConfig).means?.trim()
      const what = declared ? '' : toLand ? 'Approving lands it and ships it.' : 'Approving lets the work go on.'
      const mine = viewerMayApprove(view, approval)
      return {
        tone: 'warn', stamp: mine ? 'waiting for your approval' : 'waiting for approval', step: approval,
        headline: toLand ? (mine ? 'Waiting for your approval to land' : 'Waiting for approval to land') : `Waiting for approval: ${approval.name}`,
        sentence: [what, quorum, before].filter(Boolean).join(' ') || undefined,
        waitingOn: mine ? 'you' : approversOf(approval), primary: 'approve',
      }
    }
    return {
      tone: 'warn', stamp: 'waiting', headline: 'Waiting for a person',
      sentence: view.stateDetail, waitingOn: 'a person', primary: 'open-decision',
    }
  }
  if (state === 'blocked') {
    const failing = stage.find(failed)
    if (!failing) {
      return {
        tone: 'bad', stamp: 'blocked', headline: "This stage can't finish as recorded",
        sentence: view.stateDetail, primary: 'none',
      }
    }
    // Once the landing has merged, the work is on main: it cannot be sent
    // back, taken to chat or abandoned (AppTown refuses each), however
    // the step stopped; the step runs again, or a person overrides it.
    if (landing && view.evidence?.merged) {
      return {
        tone: 'bad', stamp: 'stopped', step: failing, headline: `Landing stopped after the merge: “${failing.name}” did not pass`,
        sentence: failing.evidence?.reason ?? '', primary: offered(d.next, 'retry', failing.id) ? 'try-again' : 'none',
      }
    }
    if (offered(d.next, 'restart-landing')) {
      // The step that read the rules says which changed.
      const law = stage.find((s) => s.type === 'policy/current' && failed(s)) ?? failing
      return {
        tone: 'bad', stamp: 'blocked', step: law, headline: 'The rules changed while this landing waited',
        sentence: law.evidence?.reason ?? '', primary: 'restart-landing',
      }
    }
    if (interrupted(failing)) {
      return {
        tone: 'warn', stamp: 'interrupted', step: failing,
        headline: `“${failing.name}” was interrupted`, sentence: `${PRODUCT} restarted while it ran. Nothing was lost.`,
        primary: offered(d.next, 'retry', failing.id) ? 'pick-up' : 'take-to-chat',
      }
    }
    const reason = failing.evidence?.reason ?? ''
    if (failing.type === 'approval') {
      const ev = failing.evidence
      return {
        tone: 'bad', stamp: 'sent back', step: failing,
        headline: `Rejected: ${ev.approver ?? 'a person'} sent “${failing.name}” back`,
        sentence: ev.comment ? `“${ev.comment}”` : reason, primary: offered(view.next, 'send-back') ? 'send-back' : 'take-to-chat',
      }
    }
    if (landing) {
      return {
        tone: 'bad', stamp: 'stopped', step: failing, headline: `Landing stopped: “${failing.name}” did not pass`,
        sentence: reason, primary: offered(view.next, 'send-back') ? 'send-back' : 'none',
      }
    }
    return {
      tone: 'bad', stamp: 'blocked', step: failing, headline: `Blocked: “${failing.name}” did not pass`,
      sentence: reason, primary: 'take-to-chat',
    }
  }
  // The server works out the rework (RunSnapshot.sentBack).
  const sentBack = view.detail?.sentBack ?? undefined
  if (state === 'queued') {
    return {
      tone: 'neutral', stamp: 'queued',
      headline: landing ? 'Queued to land' : sentBack ? 'Sent back: queued for the agent' : 'Queued',
      sentence: view.stateDetail, waitingOn: 'a free agent slot', primary: 'none', sentBack,
    }
  }
  if (state === 'ready to advance' && offered(d.next, 'advance')) {
    return {
      tone: 'ok', stamp: 'ready', headline: `Ready to continue from “${run.currentStage}”`,
      sentence: 'Continue moves the run to its next stage.', primary: 'continue',
    }
  }
  // agent working / working
  const running = stage.find((s) => s.status === 'running')
  if (!running) {
    return {
      tone: 'busy', stamp: 'working', headline: landing ? 'Starting the landing' : 'Working',
      sentence: view.stateDetail, waitingOn: PRODUCT, primary: 'none', sentBack,
    }
  }
  const agent = state === 'agent working'
  return {
    tone: 'busy', stamp: agent ? 'agent working' : 'working', step: running,
    headline: sentBack ? 'Sent back: the agent is reworking it'
      : landing ? `Landing: ${running.name} is running`
      : agent ? `The agent is working: ${running.name}` : `Running: ${running.name}`,
    sentence: sentBack ? undefined : view.stateDetail && view.stateDetail !== running.name ? view.stateDetail : undefined,
    waitingOn: agent ? 'the agent' : PRODUCT, primary: 'none', sentBack,
  }
}
