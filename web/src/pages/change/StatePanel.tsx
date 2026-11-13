import { ChangeView, Step, StepOf, isStep } from '../../api'
import { StaleNote, type Gated } from '../../ReadGate'
import type { MainConflict } from '../../wire.gen'
import { ChangeHeader } from './Head'
import { nextStep } from './next'
import { ShieldAlert, ShieldCheck, ShieldQuestion, Shield } from 'lucide-react'

// The state names the viewer's next action when it is their turn.
// The decision card holds the actions and their meaning.
// The panel reads the page's own payload through nextStep;
// when the page's last refresh failed, it says the answer may be old
// instead of presenting it as current.

export function StatePanel({ view, reading }: { view: ChangeView; reading?: Gated }) {
  const next = nextStep(view)
  const headline = staleHeadline(reading) || view.waitingOn.says
  const stale = !!reading?.refreshFailed
  const tone = stale ? 'unknown' : view.waitingOn.tone
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
        {view.returnedBy && <div className="ch-sentence">Sent back by {view.returnedBy.by}: {view.returnedBy.note}</div>}
        {reading && <StaleNote q={reading} />}
      </div>}

    </section>
  )
}

// requestShortfall: what the change's summary says it does not do of what
// was asked, when the summary's writer read the work as falling short.
// 'yes' and 'unknown' say nothing here: unknown is not a warning, and it is
// never shown as met either.
export function requestShortfall(ch: { requestMet?: string; requestGap?: string; requestGapQuote?: string }): string | undefined {
  if ((ch.requestMet !== 'no' && ch.requestMet !== 'partly') || !ch.requestGapQuote?.trim()) return undefined
  const gap = ch.requestGap?.trim()
  if (!gap) return undefined
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

// MainConflictLine: main has moved and changes the same files as the
// change, so neither AppTown nor a landing can merge it in; how to catch
// up in the chat. It goes once the change has caught up.
export function MainConflictLine({ conflict }: { conflict: MainConflict }) {
  return (
    <div className="ch-sentence">
      {conflict.mainBranch} has moved and conflicts with this change in {conflict.files.join(', ')}. Catch up in the change's chat:{' '}
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
function recoveryRecord(view: ChangeView): { actions: string[]; attempts: string[] } | undefined {
  const d = view.detail
  if (!d || view.change.state === 'landing' || view.change.state === 'shipped' || view.change.state === 'abandoned' || d.run.status !== 'running') return undefined
  const steps = d.steps.filter((s) => s.stage === d.run.currentStage)
  const events = d.events ?? []
  const attempts = events.filter((e) => e.kind === 'step.rework').map((e) => {
    const reason = typeof e.payload.reason === 'string' && !e.payload.reason.includes('evidence fed back') && !e.payload.reason.includes('their words fed back')
      ? e.payload.reason : 'The reason was not kept.'
    return `${String(e.payload.step ?? 'Check')}: ${reason}`
  })
  const held = steps.filter((s) => (s.status === 'needs_human' || s.status === 'error'))
  const meaningful = (s: string) => !/^(?:blockers?:\s*)?none[.!]?$/i.test(s.trim())
  const actions = held.flatMap(s => {
    const actions = (s.evidence?.actions ?? []).filter(meaningful)
    return actions.length ? actions : [`${s.name}: ${s.evidence?.reason && meaningful(s.evidence.reason) ? s.evidence.reason : 'needs a person. Open Details to see the record.'}`]
  })
  return { actions, attempts }
}

function staleHeadline(reading?: Gated): string | undefined {
  return reading?.refreshFailed ? 'Current state could not be read. The last recorded state is shown below.' : undefined
}

// The state card stays short; the complete account and recovery commands
// remain alongside the run in Details.
export function StateRecord({ view }: { view: ChangeView }) {
  const next = nextStep(view)
  const handover = recoveryRecord(view)
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
  const v = view.checks
  if (!v.started) return null
  const Icon = v.tone === 'ok' ? ShieldCheck : v.tone === 'bad' ? ShieldAlert : v.tone === 'unknown' ? ShieldQuestion : Shield
  return <span className={`ch-shield ${v.tone}`}><Icon aria-hidden size={18} />{v.text}</span>
}
