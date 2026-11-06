import { post } from '../../api'
import { useAction } from '../../data'
import { fmtStamp } from '../../format'
import type { Release, ReleaseDecision } from '../../wire.gen'

export function ReleaseAssent({ approval }: { approval: Release['approval'] }) {
  if (!approval) return null
  return <p>Approved by {approval.actor.name} via {approval.actor.via} · {fmtStamp(approval.at)}<br />{approval.meaning}</p>
}

// The displayed sentence is sent unchanged and retained with the person's act.
export function ReleaseApprovalActions({ slug, release, decision, current = true }: { slug: string; release: Release; decision?: ReleaseDecision; current?: boolean }) {
  const { run, busy, err } = useAction(undefined, { affects: ['/activity', '/attention'] })
  if (release.state !== 'waiting for approval') return <ReleaseAssent approval={release.approval} />
  const path = `/projects/${slug}/releases/${release.number}`
  return <div className="stack stack-2">
    <p>Waiting for approval from {release.approvalRole}.</p>
    {decision?.canApprove && <p>{decision.meaning}</p>}
    <div className="row row-2">
      {decision?.canApprove && <button className="primary" disabled={busy || !current} onClick={() => run(() => post(`${path}/approve`, { meaning: decision.meaning }))}>Approve</button>}
      {decision?.canCancel && <button disabled={busy || !current} onClick={() => run(() => post(`${path}/cancel`, {}))}>Cancel</button>}
    </div>
    {err && <p className="error" role="alert">{err}</p>}
  </div>
}
