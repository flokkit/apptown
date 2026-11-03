import type { Artifact, RunDetail, RunEvent, Step } from '../../api'
import { approversOf, reviewText } from './next'
import { firstLine } from './Head'
import { stepTitle, testsNote } from './Evidence'

export type StoryEntry = {
  key: string; at: string; text: string; step?: Step; actionStep?: Step; current?: boolean
  detail: RunDetail; records: Artifact[]; events: RunEvent[]; session?: string
}
const words = (value: unknown) => typeof value === 'string' ? value : ''
const object = (value: unknown): Record<string, unknown> => value && typeof value === 'object' ? value as Record<string, unknown> : {}

export function checkResult(s: Step): string {
  if (s.overrideBy) return `overridden by ${s.overrideBy}: ${s.overrideReason}`
  if (s.statusLabel === 'unknown' || s.statusLabel === 'excepted') return `${s.statusLabel}: ${s.evidence.reason || 'The recorded result needs review.'}`
  if (s.checkReading?.unreadable) return `unknown: ${s.checkReading.unreadable}`
  if (s.status === 'not_applicable') return `doesn't apply${s.evidence.reason || (s.type === 'scan' && s.evidence.notApplicable) ? `: ${s.evidence.reason || (s.type === 'scan' && s.evidence.notApplicable)}` : ''}`
  if (s.status === 'pending' || s.status === 'skipped') return `not run${s.evidence.reason ? ` · ${s.evidence.reason}` : ''}`
  if (s.status === 'running') return 'running'
  const unmet = s.checkReading?.checks?.some(c => c.verdict === 'fail' && c.gates !== false)
  const unknownTests = s.type === 'tests' && (!s.evidence.testCounts || !!s.evidence.testReportUnreadable)
  const result = s.status === 'pass' && !unmet ? unknownTests ? 'unknown' : '✓' : '✗'
  const notes = [s.evidence.reason, s.type === 'tests' && testsNote(s.evidence).note,
    s.checkReading?.checkErrors && `${s.checkReading.checkErrors} checks could not run`,
    ...(s.checkReading?.undecided ?? []).map(c => `${c}: unknown`),
    ...(s.checkReading?.checks ?? []).filter(c => c.verdict !== 'pass').map(c => `${c.check}: ${c.verdict === 'not-applicable' ? "doesn't apply" : c.verdict}${c.detail ? ` · ${c.detail}` : ''}`),
  ].filter(Boolean)
  return `${result}${notes.length ? ` · ${notes.join(' · ')}` : ''}`
}

export function stepSentence(s: Step): string {
  if (s.type === 'agent/pi') {
    const duration = s.evidence.durationMs
    const took = duration != null ? `, ${duration < 60000 ? `${Math.round(duration / 1000)}s` : `${Math.round(duration / 60000)} min`}` : ''
    const files = s.evidence.files
    return `Agent ${s.status === 'running' ? 'working' : s.status === 'pending' ? 'not started' : 'worked'}${took}${files ? ` · ${files.length} ${files.length === 1 ? 'file' : 'files'} changed` : ''}${['fail', 'error', 'needs_human'].includes(s.status) ? ` · ${s.evidence.reason || 'stopped without finishing'}` : ''}`
  }
  if (s.type === 'agent/review' && ['fail', 'error', 'needs_human'].includes(s.status) && !s.overrideBy) return `Reviewer: ✗ Rejected · ${reviewText(s)}`
  if (s.type === 'agent/review') return `Reviewer: ${s.evidence.verdict || checkResult(s)}${s.status !== 'pass' && s.evidence.verdict ? ` · ${checkResult(s)}` : ''}${s.evidence.summary ? ` · ${firstLine(s.evidence.summary)}` : ''}`
  if (s.type === 'approval') {
    if (s.evidence.evalAuthorised) return `Approval: authorised for evaluation: ${s.evidence.evalAuthorised}`
    if (s.status === 'pending') return `Approval: waiting for ${approversOf(s)}`
    return `Approval: ${s.evidence.decision === 'approve' ? 'accepted' : s.evidence.decision === 'reject' ? 'rejected' : 'not decided'}${s.evidence.approver ? ` by ${s.evidence.approver}` : ''}${s.evidence.comment ? `: “${s.evidence.comment}”` : ''}`
  }
  return `${['reconcile', 'policy/current', 'scm/merged', 'deploy'].includes(s.type) ? 'Landing' : 'Checks'}: ${stepTitle(s)} ${checkResult(s)}`
}

// Start receipts define attempts. A later reset receipt preserves that attempt's
// evidence; never give an earlier round the current round's result or files.
export function runStory(detail: RunDetail): StoryEntry[] {
  const entries: StoryEntry[] = []
  const events = [...detail.events].sort((a, b) => a.at.localeCompare(b.at))
  for (const step of detail.steps) {
    const starts = events.filter(e => e.kind === 'step.started' && e.payload.step === step.name)
    const attempts = starts.length ? starts : [undefined]
    attempts.forEach((start, i) => {
      const next = starts[i + 1]
      const recordedAt = step.type === 'approval' ? step.evidence.at || step.finishedAt || step.startedAt : step.startedAt || step.finishedAt
      const from = start?.at || recordedAt || (step.status === 'pending' ? [...events.map(e => e.at), ...detail.steps.flatMap(s => [s.startedAt, s.finishedAt])].filter(Boolean).sort().at(-1) : undefined) || detail.run.createdAt
      const reset = events.find(e => e.at >= from && (!next || e.at <= next.at) && e.payload.step === step.name && e.payload.previousEvidence != null)
      const current = i === attempts.length - 1 && !reset
      const finish = events.find(e => e.at >= from && (!next || e.at < next.at) && e.kind === 'step.finished' && e.payload.step === step.name)
      const historical = current ? step : { ...step, evidence: object(reset?.payload.previousEvidence), status: words(reset?.payload.previousStatus || finish?.payload.status) || 'running', overrideBy: words(reset?.payload.previousOverrideBy), overrideReason: '', startedAt: from, finishedAt: finish?.at || '' } as Step
      // Pending steps in future stages are not events that happened yet.
      if (!start && step.status === 'pending' && (detail.run.status !== 'running' || step.stage !== detail.run.currentStage)) return
      const related = events.filter(e => e.payload.step === step.name && e.at >= from && (!next || e.at < next.at))
      const sentBack = related.find(e => e.kind === 'step.sendback') || (i === 0 ? events.find(e => e.kind === 'step.sendback' && e.payload.step === step.name && e.at <= from) : undefined)
      const text = stepSentence(historical) + (sentBack ? ` · Sent back by ${words(sentBack.payload.by)}: “${words(sentBack.payload.note)}”` : '')
      entries.push({ key: `${step.id}-${start?.id || i}`, at: from, text, step: historical, actionStep: i === attempts.length - 1 ? step : undefined, current, detail, records: [], events: related, session: words(start?.payload.sessionId) })
    })
  }
  // Preserve run-level evidence on a work entry, including records without an
  // explicit step association. The evidence labels retain their original names.
  let work = entries.find(e => e.step?.type === 'agent/pi')
  if (!work) {
    work = { key: `work-${detail.run.id}`, at: detail.run.createdAt, text: 'Work started', detail, records: [], events: events.filter(e => !e.payload.step) }
    entries.unshift(work)
  } else work.events = [...work.events, ...events.filter(e => !e.payload.step)]
  const attachedEvents = new Set(entries.flatMap(e => e.events))
  work.events.push(...events.filter(e => !attachedEvents.has(e)))
  for (const record of detail.artifacts) {
    const matched = entries.filter(e => e.step && (record.id === object(e.step.evidence).reportArtifactId || record.name === `step-${e.step.id}` || record.name === `steering-${e.step.name}` || record.name === e.step.name || record.name === object(e.step.config.output).name || (e.step.type === 'preview' && e.step.evidence.screenshots?.shots?.some(s => s.artifact === record.id))))
      .filter(e => e.at <= record.createdAt).sort((a, b) => a.at.localeCompare(b.at)).at(-1)
    ;(matched || work).records.push(record)
  }
  for (const e of events.filter(e => ['run.abandoned', 'main.merged', 'main.conflict'].includes(e.kind))) {
    entries.push({ key: `event-${e.id}`, at: e.at, text: e.kind === 'run.abandoned' ? `Abandoned${e.payload.by ? ` by ${e.payload.by}` : ''}: ${words(e.payload.reason)}` : e.kind === 'main.merged' ? 'Landing: up to date with main ✓' : `Landing: conflicts with main${Array.isArray(e.payload.conflicts) ? ` · ${e.payload.conflicts.join(', ')}` : ''}`, detail, records: [], events: [e] })
  }
  return entries
}
