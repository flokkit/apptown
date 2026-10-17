import { useState } from 'react'
import { currentUser, post } from './api'
import { useAction, useGet } from './data'
import type { ApprovalQuestion, ChangeAdds } from './wire.gen'

// AddsSection is what the approval's commit brings into the app, as the
// forge read it from the app's declarations. The note names what was read,
// so "nothing new" never claims more than the forge looked at; a
// declaration it could not read is its own line, never folded into nothing.
export function AddsSection({ adds }: { adds: ChangeAdds }) {
  return (
    <div className="adds">
      <div className="adds-head">This change adds</div>
      <div className="muted small">Approving accepts these into the app.{adds.readFrom ? ` Read from ${adds.readFrom}.` : ''}</div>
      {adds.nothingNew && <div className="small">{adds.nothingNew}</div>}
      {adds.lines.length > 0 && (
        <ul className="adds-lines">
          {adds.lines.map((l) => <li key={l.kind + l.text} className={`small adds-${l.kind}`}>{l.text}</li>)}
        </ul>
      )}
    </div>
  )
}

// DecisionForm: one approve/reject form for every place a person decides —
// the decision card, a step card, a called workflow's step (debt review,
// finding 25: three copies, one of which sent no comment). What approving
// MEANS and any instructions for the approver come from the step's config
// and are shown right above the words that travel with the decision.
// landingMeansFor is what a change's landing would ask, beside its waiting
// acceptance. The server sends the sentences only while an acceptance waits,
// and the acceptance is the approval in the run's last stage.
export function landingMeansFor(detail: { stages: string[]; landingMeans: string[] }, step: { type: string; stage: string }): string[] | undefined {
  const last = detail.stages[detail.stages.length - 1]
  return step.type === 'approval' && step.stage === last && detail.landingMeans.length > 0 ? detail.landingMeans : undefined
}

export function DecisionForm({ stepId, means, alsoMeans, instructions, onDone, approveLabel = 'Approve', rejectLabel = 'Reject', placeholder = 'comment (optional)', compact }: {
  stepId: string
  means?: string; instructions?: string
  // alsoMeans: what the landing's approvals say. Accepting counts for them
  // where the rules let this person approve them, so they read them here.
  alsoMeans?: string[]
  onDone: () => void
  approveLabel?: string
  rejectLabel?: string
  placeholder?: string
  // compact: inline in a list row — subtle buttons, no headline.
  compact?: boolean
}) {
  const [comment, setComment] = useState('')
  const { run, busy, err } = useAction(onDone)
  // What approving agrees to, read before deciding: for an approval of a
  // commit, what that commit adds. Approving waits until it has been read.
  const question = useGet<ApprovalQuestion>(`/steps/${stepId}/question`)
  const adds = question.data?.adds ?? undefined
  const decide = (decision: 'approve' | 'reject') =>
    // What was shown goes back with the decision: the landing's words, and
    // the digest of what the change adds. The server records them only if
    // they are still what approving means; otherwise it refuses and the
    // question is read again, so the new list is what the person sees.
    run(async () => {
      try {
        return await post(`/steps/${stepId}/approve`, {
          user: currentUser(), decision, comment,
          ...(alsoMeans ? { alsoApproves: alsoMeans } : {}),
          ...(decision === 'approve' && adds ? { adds: adds.digest } : {}),
        })
      } catch (e) {
        void question.reload()
        throw e
      }
    })
  const unread = question.loading || (!question.data && !!question.err)
  return (
    <div className={compact ? undefined : 'decision-form'} style={compact ? { display: 'contents' } : undefined}>
      {!compact && means && <div className="muted small" style={{ marginTop: 6, fontStyle: 'italic' }}>Approving means: “{means}”</div>}
      {!compact && alsoMeans?.map((m) => (
        <div key={m} className="muted small" style={{ marginTop: 2, fontStyle: 'italic' }}>If you may approve the landing, this also approves it: “{m}”</div>
      ))}
      {!compact && instructions && <div className="muted small" style={{ marginTop: 2 }}>{instructions}</div>}
      {!compact && adds && <AddsSection adds={adds} />}
      {!compact && question.err && <div className="error small">What approving agrees to could not be read: {question.err}</div>}
      {/* On a phone the decision bar stays at the foot of the screen while
          the approval it belongs to is on screen (styles.css .decision-bar),
          so a long summary never pushes the decision out of reach. */}
      <div className={compact ? 'row' : 'row decision-bar'} style={compact ? { gap: 4 } : { marginTop: 8 }}>
        <input placeholder={placeholder} value={comment} onChange={(e) => setComment(e.target.value)}
          style={compact ? { width: 180 } : undefined} aria-label="comment" />
        <button className={compact ? 'subtle' : 'primary'} disabled={busy || unread} onClick={() => decide('approve')}
          title={unread ? 'Reading what approving agrees to' : undefined}>{approveLabel}</button>
        <button className={compact ? 'subtle danger' : 'danger'} disabled={busy} onClick={() => decide('reject')}>{rejectLabel}</button>
        {err && <span className="error small">{err}</span>}
      </div>
      {compact && adds && <div style={{ flexBasis: '100%' }}><AddsSection adds={adds} /></div>}
    </div>
  )
}
