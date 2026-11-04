import { viewerTurn } from './Turn'
import { STATUS_LIGHT } from '../../Status'
import { PRODUCT } from '../../brand'
import { ChangeView, Step, StepOf, isStep } from '../../api'
import { useGet } from '../../data'
import { StaleNote, type Gated } from '../../ReadGate'
import type { ConsoleSessionList, DeploymentView, MainConflict } from '../../wire.gen'
import { ChangeHeader } from './Head'
import { NextStep, nextStep, checksRunning } from './next'
import { checksVerdict } from './Overview'
import { ShieldAlert, ShieldCheck, ShieldQuestion, Shield } from 'lucide-react'

// The state names the viewer's next action when it is their turn.
// The decision card holds the actions and their meaning.
// The panel reads the page's own payload through nextStep;
// when the page's last refresh failed, it says the answer may be old
// instead of presenting it as current.

export function StatePanel({ view, reading }: { view: ChangeView; reading?: Gated }) {
  // A live chat on this change is where its work happens while no run
  // goes; the project's chat list says whether its agent has the turn. An
  // unread or failed list claims nothing.
  const open = view.change.state === 'in_progress'
  const chats = useGet<ConsoleSessionList>(open ? `/projects/${view.projectSlug}/console` : null, { poll: 5000 })
  const live = chats.refreshFailed ? undefined
    : chats.data?.sessions.find((x) => x.changeId === view.change.id && x.state !== 'dead' && !!x.turn)
  const next = nextStep(view, { chat: live?.turn })
  const handover = handoverState(view, next)
  const busy = checksRunning(view.detail?.steps ?? [], view.detail?.run.currentStage)
  const release = view.evidence?.releases?.at(-1)
  const deployments = useGet<DeploymentView[]>(view.change.state === 'shipped' ? `/projects/${view.projectSlug}/deployments` : null)
  const address = release?.state === 'released' && release.serving && deployments.current ? deployments.data?.find(e => e.name === release.deployment)?.url : undefined
  const agentWorking = live?.turn === 'working' || view.detail?.steps.some(s => s.type === 'agent/pi' && s.status === 'running')
  const turn = !reading?.refreshFailed && viewerTurn(view) && !agentWorking
  const needed = handover?.actions[0]
    || (handover?.headline.includes('the agent tried 3 times') ? handover.headline.replace(/^Waiting for you: /, '') : undefined)
    || (next.primary === 'pick-up' ? 'pick the interrupted work back up' : next.primary === 'restart-landing' ? 'restart landing under the current rules' : 'resolve the reported check problems')
  // A waiting decision needs no sentence: the amber banner and the breathing "Decide…" say it.
  const turnHeadline = next.recovery ? 'Waiting for a person' : next.primary === 'approve' ? '' : `Your turn: ${needed}`
  const headline = staleHeadline(reading) || (turn ? turnHeadline : view.change.state === 'abandoned' ? 'Abandoned'
    : address ? `Live at ${address}`
    : next.recovery ? 'Waiting for a person'
    : next.primary === 'open-decision' ? next.headline
    : handover?.headline?.startsWith('Waiting for you:') ? handover.headline
    : agentWorking || handover?.headline?.startsWith('The agent is working on it') ? 'The agent is working on it'
    : busy ? 'Checks are running'
    : next.primary === 'approve' ? next.headline
    : next.tone === 'bad' ? 'Waiting for you: resolve the reported check problems'
    : view.change.state === 'shipped' ? release ? `Release to ${release.deployment}: ${release.statusLabel}` : 'Added to the app’s main version'
    : next.headline)

  const stale = !!reading?.refreshFailed
  const light = stale || next.tone === 'unknown' ? 'unknown' : STATUS_LIGHT[view.statusLabel]
  const tone = turn ? 'warn' : ({pass:'ok', pending:'neutral', fail:'bad', unknown:'unknown', '':'neutral'} as Record<string, string>)[light]
  // A failed check the agent can fix leads with what the check found, in
  // its own first words, instead of the step's recorded reason.
  return (
    <section className={`ch-state tone-${tone}`} aria-label="Where it stands">
      <ChangeHeader view={view} compact />
      {(headline || stale || reading) && <div className="ch-state-words">
        {headline && <div className="ch-state-head">
          <span className={`ch-dot ${tone}`} aria-hidden="true" />
          <span className="ch-headline">{headline}</span>
          <ChecksShield view={view} />
        </div>}
        {stale && <div className="ch-sentence">Last recorded state: {next.headline}</div>}
        {reading && <StaleNote q={reading} />}
      </div>}

    </section>
  )
}

// requestShortfall: what the change's summary says it does not do of what
// was asked, when the summary's writer read the work as falling short.
// 'yes' and 'unknown' say nothing here: unknown is not a warning, and it is
// never shown as met either.
export function requestShortfall(ch: { requestMet?: string; requestGap?: string }): string | undefined {
  if (ch.requestMet !== 'no' && ch.requestMet !== 'partly') return undefined
  const gap = ch.requestGap?.trim()
  if (!gap) return ch.requestMet === 'no' ? 'it does not do what was asked.' : 'it does only part of what was asked.'
  return /[.!?]$/.test(gap) ? gap : gap + '.'
}

// requestUnchecked: when the summary's writer could not check all of the
// work against the request, what it could not check and what would settle it.
// This is unknown, not a shortfall: the page says "could not check", never "falls short".
export function requestUnchecked(ch: { requestMet?: string; requestGap?: string }): string | undefined {
  if (ch.requestMet !== 'unknown') return undefined
  return ch.requestGap?.trim() || undefined
}

// latestBoot is the run's newest preview step: its screenshots are what the
// person approving sees of the app. Only this run's: never an earlier one's.
export function latestBoot(steps: Step[]): StepOf<'preview'> | undefined {
  const boots = steps.filter(isStep('preview')).filter((s) => s.status !== 'running' && s.evidence?.screenshots)
  return boots.length ? boots[boots.length - 1] : undefined
}

// MainConflictLine: main AppTown could not merge into the change before a
// step, while the branch still does not contain it, and how to catch up.
// It goes once the change has caught up.
export function MainConflictLine({ conflict }: { conflict: MainConflict }) {
  return (
    <div className="ch-sentence">
      {conflict.mainBranch} has moved and {PRODUCT} could not merge it in before step {conflict.step}: {conflict.files.join(', ')}{' '}
      {conflict.files.length === 1 ? 'conflicts' : 'conflict'}. Catch up in the change's chat:{' '}
      <code style={{ overflowWrap: 'anywhere' }}>{conflict.catchUp}</code>
    </div>
  )
}

// Recovery carries the recorded failure and requested actions in full to the agent.
export function recoveryNote(d: NonNullable<ChangeView['detail']>): string {
  return d.steps.filter((s) => s.stage === d.run.currentStage && ['fail', 'error', 'needs_human'].includes(s.status))
    .map((s) => {
      const ev = s.evidence as { reason?: string; actions?: string[]; summary?: string; findings?: string[]; log?: string; logTail?: string }
      const words = [...new Set([ev.reason, ev.summary, ...(ev.findings ?? []), ev.log, ev.logTail, ...(ev.actions ?? [])].filter(Boolean))]
      return `${s.name}:\n${words.join('\n')}`
    })
    .join('\n\n')
}

// Rework events keep each attempt's reason even after the step is checked
// again. A person's retry starts a fresh allowance; history remains visible.
function handoverState(view: ChangeView, next: NextStep): { headline: string; actions: string[]; attempts: string[] } | undefined {
  const d = view.detail
  if (!d || view.change.state === 'landing' || view.change.state === 'shipped' || view.change.state === 'abandoned' || d.run.status !== 'running' || next.tone === 'unknown') return undefined
  const steps = d.steps.filter((s) => s.stage === d.run.currentStage)
  const events = d.events ?? []
  const attempts = events.filter((e) => e.kind === 'step.rework').map((e) => {
    const reason = typeof e.payload.reason === 'string' && !e.payload.reason.includes('evidence fed back') && !e.payload.reason.includes('their words fed back')
      ? e.payload.reason : 'The reason was not kept.'
    return `${String(e.payload.step ?? 'Check')}: ${reason}`
  })
  const lastRetry = events.map((e) => e.kind).lastIndexOf('step.retried')
  const rounds = events.slice(lastRetry + 1).filter((e) => e.kind === 'step.rework').length
  const held = steps.filter((s) => (s.status === 'needs_human' || s.status === 'error'))
  const meaningful = (s: string) => !/^(?:blockers?:\s*)?none[.!]?$/i.test(s.trim())
  const actions = held.flatMap(s => {
    const actions = (s.evidence?.actions ?? []).filter(meaningful)
    return actions.length ? actions : [`${s.name}: ${s.evidence?.reason && meaningful(s.evidence.reason) ? s.evidence.reason : 'needs a person. Open Details to see the record.'}`]
  })
  const busy = steps.some((s) => s.type === 'agent/pi' && (s.status === 'pending' || s.status === 'running'))
  if (busy && !held.length) return { headline: `The agent is working on it${rounds ? ` (round ${Math.min(rounds, 3)} of 3)` : ''}`, actions, attempts }
  if (held.length) return { headline: `Waiting for you: ${actions[0]}`, actions, attempts }
  const failed = steps.find((s) => s.status === 'fail')
  if (failed && rounds >= 3) return { headline: `Waiting for you: the agent tried 3 times; ${failed.name} still fails: ${failed.evidence?.reason || 'read what the check found'}. Take it to chat, or abandon it.`, actions, attempts }
  if (failed) return { headline: `Waiting for you: ${failed.name} failed: ${failed.evidence?.reason || 'read what the check found'}`, actions, attempts }
  if (next.primary === 'approve') return { headline: `Waiting for ${next.waitingOn === 'you' ? 'you' : next.waitingOn || 'a person'}: approve this change`, actions, attempts }
  if (next.primary === 'continue') return { headline: 'Waiting for you: continue to the next stage', actions, attempts }
  return { headline: next.headline, actions, attempts }
}

function staleHeadline(reading?: Gated): string | undefined {
  return reading?.refreshFailed ? 'Current state could not be read. The last recorded state is shown below.' : undefined
}

// The state card stays short; the complete account and recovery commands
// remain alongside the run in Details.
export function StateRecord({ view }: { view: ChangeView }) {
  const next = nextStep(view)
  const handover = handoverState(view, next)
  return <div className="ch-state-record">
    {next.sentBack && <p>{next.sentBack.by} sent it back: “{next.sentBack.note}”</p>}
    {next.sentence && <p>{next.sentence}</p>}
    {!!handover?.actions.length && <ul>{handover.actions.map((a, i) => <li key={i}>{a}</li>)}</ul>}
    {!!handover?.attempts.length && <section><h4>What the agent tried ({handover.attempts.length})</h4><ol>{handover.attempts.map((a, i) => <li key={i}>{a}</li>)}</ol></section>}
    {view.mainConflict && <MainConflictLine conflict={view.mainConflict} />}
  </div>
}

// The checks shield sits in the same place on every change. It is a mark, not a link.
function ChecksShield({ view }: { view: ChangeView }) {
  const v = checksVerdict(view.detail?.steps ?? [])
  if (!v) return null
  const Icon = v.tone === 'ok' ? ShieldCheck : v.tone === 'bad' ? ShieldAlert : v.tone === 'unknown' ? ShieldQuestion : Shield
  return <span className={`ch-shield ${v.tone}`}><Icon aria-hidden size={18} />{v.text}</span>
}
