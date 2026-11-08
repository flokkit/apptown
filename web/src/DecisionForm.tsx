import { InlineHelp } from './InlineHelp'
import { useContext, useRef, useState } from 'react'
import { DecisionChoices, type DecisionChoice } from './DecisionChoices'
import { PRODUCT } from './brand'
import { currentUser, post } from './api'
import { useAction, useGet } from './data'
import type { ApprovalQuestion, ChangeAdds } from './wire.gen'

const contractRows = (adds: ChangeAdds) => adds.rows.filter(r => ['secrets', 'services', 'app parts'].includes(r.name))
export const contractUnread = (adds?: ChangeAdds) => !adds || contractRows(adds).length !== 3 || contractRows(adds).some(r => r.unknown || r.baseUnread)
export const contractChanged = (adds?: ChangeAdds) => !!adds && contractRows(adds).some(r => r.added.length || r.removed.length)

export function AddsSection({ adds }: { adds: ChangeAdds }) {
  const inlineHelp = useContext(InlineHelp)
  const [help, setHelp] = useState(false)
  if (!contractChanged(adds)) return null
  const words = (name: string, value: string, removed: boolean) => {
    if (removed) return `− no longer needs ${value.replace(/^part /, 'the part ')}`
    if (name === 'secrets') return `+ needs the secret ${value}, which someone must set before it can run`
    if (name === 'services') return value === 'postgres' ? '+ needs a database (postgres)' : value === 's3' ? '+ needs file storage (s3)' : `+ needs ${value}`
    return value.startsWith('part ') ? `+ a new part: ${value.slice(5)}` : `+ ${value}`
  }
  return <div className="adds adds-contract">
    <div className="adds-head">Changes the app contract {!inlineHelp && <button className="subtle" aria-label="About the app contract" aria-expanded={help} onClick={() => setHelp(!help)}>?</button>}</div>
    {(inlineHelp || help) && <p className="small">The app contract is what this app asks {PRODUCT} for to run: its parts, the services it needs (a database, file storage) and the secrets someone must provide. {PRODUCT} supplies exactly what it asks for.</p>}
    <ul className="adds-lines">{contractRows(adds).flatMap(r => [
      ...r.added.map(v => <li className="small adds-added" key={r.name + '+' + v}>{words(r.name, v, false)}</li>),
      ...r.removed.map(v => <li className="small adds-removed" key={r.name + '-' + v}>{words(r.name, v, true)}</li>),
    ])}</ul>
  </div>
}

export function ContractAttention({ adds }: { adds?: ChangeAdds }) {
  if (!contractUnread(adds)) return null
  return <div className="ch-check-exception"><strong>Not checked yet: changes to the app contract</strong>
    <div>{adds ? contractRows(adds).map(r => r.unknown || r.baseUnread).filter(Boolean).join(' · ') : 'The app declarations have not been read.'}</div>
    <div>Read the app declarations and compare them with the starting commit to check for changes.</div>
  </div>
}

// DecisionForm: one approve/reject form for every place a person decides —
// the decision card, a step card, a called workflow's step (debt review,
// finding 25: three copies, one of which sent no comment). What approving
// MEANS and any instructions for the approver come from the step's config
// and stay beside the controls that record the decision.
// landingMeansFor is what a change's landing would ask, beside its waiting
// acceptance. The server sends the sentences only while an acceptance waits,
// and the acceptance is the approval in the run's last stage.
export function landingMeansFor(detail: { stages: string[]; landingMeans: string[] }, step: { type: string; stage: string }): string[] | undefined {
  const last = detail.stages[detail.stages.length - 1]
  return step.type === 'approval' && step.stage === last && detail.landingMeans.length > 0 ? detail.landingMeans : undefined
}

export function DecisionForm({ stepId, means, alsoMeans, instructions, onDone, approveLabel = 'Approve', rejectLabel = 'Reject', placeholder = 'comment (optional)', compact, overview, canSendBack, abandon, externalBusy = false, onAccepted }: {
  onAccepted?: () => void
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
  canSendBack?: boolean
  abandon?: DecisionChoice
  externalBusy?: boolean
  overview?: boolean
  compact?: boolean
}) {
  const [comment, setComment] = useState('')
  const meaning = useRef<HTMLDivElement>(null)
  const [readMeaning, setReadMeaning] = useState(false)
  const { run, busy, err } = useAction(onDone)
  // What approving agrees to, read before deciding: for an approval of a
  // commit, what that commit adds. Approving waits until it has been read.
  const question = useGet<ApprovalQuestion>(`/steps/${stepId}/question`)
  const adds = question.current ? question.data?.adds ?? undefined : undefined
  const decide = (decision: 'approve' | 'reject') =>
    // What was shown goes back with the decision: the landing's words, and
    // the digest of what the change adds. The server records them only if
    // they are still what approving means; otherwise it refuses and the
    // question is read again, so the new list is what the person sees.
    run(async () => {
      try {
        const result = await post(`/steps/${stepId}/approve`, {
          user: currentUser(), decision, comment: overview && decision === 'approve' ? '' : comment,
          ...(alsoMeans ? { alsoApproves: alsoMeans } : {}),
          ...(decision === 'approve' && adds ? { adds: adds.digest } : {}),
        })
        if (decision === 'approve') onAccepted?.()
        return result
      } catch (e) {
        void question.reload()
        throw e
      }
    })
  const unread = !question.current
  return (
    <div className="decision-form">
      <ContractAttention adds={adds} />
      {adds && <AddsSection adds={adds} />}
      {!overview && <>
      {means && <div className="decision-meaning" style={{ marginTop: 6 }}>Approving means: “{means}”{contractChanged(adds) && ' and the app contract changes above'}</div>}
      {alsoMeans?.map((m) => (
        <div key={m} className="decision-meaning" style={{ marginTop: 6 }}>If you may approve the landing, this also approves it: “{m}”</div>
      ))}
      </>}
      {instructions && <div style={{ marginTop: 2 }}>{instructions}</div>}
      {question.err && <div className="error small">What approving agrees to could not be read: {question.err}</div>}
      {overview && <div className="decision-bottom"><div ref={meaning} className="small muted decision-meaning">By accepting, you confirm: “{means}”{alsoMeans?.map(m => <span key={m}> You also confirm: “{m}”</span>)}{contractChanged(adds) && ' and the app contract changes above'}</div></div>}

      {overview ? <DecisionChoices note={comment} setNote={setComment} busy={busy || externalBusy}
        accept={{ label: approveLabel, run: () => {
          const bounds = meaning.current?.getBoundingClientRect()
          if (bounds && (bounds.top < 70 || bounds.bottom > window.innerHeight)) {
            meaning.current?.scrollIntoView({ block: 'end' }); setReadMeaning(true); return
          }
          decide('approve')
        }, disabled: unread }}
        rework={canSendBack ? { label: 'Send back…', run: () => decide('reject') } : undefined} abandon={abandon} /> : <div className={compact ? 'row' : 'row decision-bar'} style={compact ? { gap: 4 } : { marginTop: 8 }}>
        <input placeholder={placeholder} value={comment} onChange={(e) => setComment(e.target.value)}
          style={compact ? { width: 180 } : undefined} aria-label="comment" />
        <button className={compact ? 'subtle' : 'primary'} disabled={busy || unread} onClick={() => decide('approve')}
          title={unread ? 'Reading what approving agrees to' : undefined}>{approveLabel}</button>
        <button className={compact ? 'subtle danger' : 'danger'} disabled={busy} onClick={() => decide('reject')}>{rejectLabel}</button>
      </div>}
      {readMeaning && <p role="status">Read what accepting records, then choose {approveLabel}.</p>}
      {err && <span className="error small">{err}</span>}

    </div>
  )
}
